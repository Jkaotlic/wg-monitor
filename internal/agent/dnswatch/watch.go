package dnswatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/actions"
)

// ndmcTimeout bounds one ndmc call: a hung ndmc must not stall the loop.
const ndmcTimeout = 15 * time.Second

// SwitchTimeout bounds the ndmc sequence of one switch. The sequence runs on
// a context detached from the loop's: a shutdown must not cut a switch
// between adding one resolver set and removing the other. main waits for the
// step in progress on exit — but a hard kill can still cut it, which the
// pending record and reconcile repair on the next start.
const SwitchTimeout = 60 * time.Second

// switchContext is the context of one switch's ndmc sequence: the values of
// ctx without its cancellation, ending after SwitchTimeout.
func switchContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), SwitchTimeout)
}

// Deps are the watchdog's side effects; nil fields get the real ones.
type Deps struct {
	Exec           actions.ExecFunc                                     // ndmc; default actions.DefaultExec
	ProbeOwn       func(ctx context.Context) error                      // default: DoH probe of Config.Endpoint
	ProbeCandidate func(ctx context.Context, line, domain string) error // default: DoH/DoT probe of the line
	Now            func() time.Time
	Logger         *slog.Logger
}

// Snapshot is the watchdog's state after its last tick — what the
// resolver_guard check reports. Lines are masked: the own endpoint's path is a
// credential and never leaves the router.
type Snapshot struct {
	Ready          bool   // the mode has been read from the router
	Idle           bool   // no own-resolver line in running-config: nothing to guard
	IdleReason     string // why idle, when it is not simply "no own line" (e.g. record_dropped_manual_cleanup)
	Mode           Mode
	Since          time.Time // the last switch; in fallback, when the fallback began
	NoLiveFallback bool      // own resolver down and no foreign candidate answered
	Foreign        []string  // foreign lines of the active fallback set
	RU             string    // RU candidate line of the active fallback set; "" when degraded
	RUDegraded     bool
	Leftover       []string // cleanup: lines that should be gone but are (or may be) still there
	Missing        []string // cleanup: lines that should be there but are not
	Fails          int      // current streak of failed own-resolver probes
	// ForeignLeftover: in a guarded primary, the leftover lines that serve
	// every zone (no `domain`) — the foreign part of the fallback set, racing
	// the own resolver. Non-empty is an unsafe state (reason foreign_leftover).
	ForeignLeftover []string
	LeftoverSince   time.Time // when ForeignLeftover was first seen
}

// persisted is dns-watchdog-state.json: the cooldown stamp and everything the
// way back needs. It holds the own line with its secret path, so it is written
// owner-only.
//
// Two separate things live here:
//   - Pending: the lines that DEFINE the current mode are not proven on the
//     router (primary: an own line present; fallback: own line absent and a
//     foreign line of the set present). Only this makes reconcile run.
//   - Leftover / Missing: the cleanup record — anything else that differs
//     from the current mode. It is retried once per tick in the normal path,
//     never changes the mode and never blocks the state machine.
//
// The record of lines (removed own lines, applied and shared lines) is kept
// while the router is in fallback (the return needs it), while Pending is set,
// and while cleanup is outstanding; in a clean, proven primary it is dropped.
type persisted struct {
	Mode       Mode      `json:"mode"`
	LastSwitch time.Time `json:"last_switch"`
	// Pending names the direction of a switch whose mode-defining lines are
	// not proven on the router (pendingForward / pendingReturn). "" = proven.
	Pending             string   `json:"pending,omitempty"`
	RemovedPrimaryLines []string `json:"removed_primary_lines,omitempty"` // own-resolver lines, re-added in full on return
	AppliedLines        []string `json:"applied_lines,omitempty"`         // fallback lines the watchdog added
	SharedLines         []string `json:"shared_lines,omitempty"`          // pre-existing lines sharing an identifier with applied ones
	Foreign             []string `json:"foreign,omitempty"`
	RU                  string   `json:"ru,omitempty"`
	RUDegraded          bool     `json:"ru_degraded,omitempty"`
	Leftover            []string `json:"leftover,omitempty"` // cleanup: to remove (or not verified gone)
	Missing             []string `json:"missing,omitempty"`  // cleanup: to add back
	// LeftoverSince: when a foreign leftover was first seen next to the own
	// line in primary — kept across restarts, so the report's `since` does not
	// restart with the agent.
	LeftoverSince time.Time `json:"leftover_since,omitzero"`
}

// Directions of an unproven switch (persisted.Pending).
const (
	pendingForward = "forward" // towards the fallback
	pendingReturn  = "return"  // towards the own resolver
	pendingMixed   = "mixed"   // a switch is recorded; what the router proves is not yet checked
)

// idleRecordDropped: the watchdog dropped a record because DNS was cleaned up
// by hand while it was away.
const idleRecordDropped = "record_dropped_manual_cleanup"

// idleOwnLineRemoved: the own line was removed by hand while the watchdog was
// still cleaning up after a return — it does not re-add it and does not take
// the lines left for a fallback.
const idleOwnLineRemoved = "own_line_removed_by_hand"

// Watcher is the DNS watchdog loop. Tick is not safe for concurrent use (Run
// calls it from one goroutine); Snapshot is.
type Watcher struct {
	cfg            Config
	exec           actions.ExecFunc
	probeOwn       func(ctx context.Context) error
	probeCandidate func(ctx context.Context, line, domain string) error
	now            func() time.Time
	log            *slog.Logger
	masked         string // Endpoint with its path masked
	ownID          string // removal identifier of the own-resolver line

	// Loop state, touched only by Tick.
	ready      bool
	idle       bool
	idleReason string
	noLive     bool
	st         State
	saved      persisted

	mu   sync.Mutex
	snap Snapshot
}

// New builds a watcher; it reads nothing and changes nothing until Tick/Run.
func New(cfg Config, d Deps) *Watcher {
	w := &Watcher{
		cfg:            cfg,
		exec:           d.Exec,
		probeOwn:       d.ProbeOwn,
		probeCandidate: d.ProbeCandidate,
		now:            d.Now,
		log:            d.Logger,
		masked:         maskEndpoint(cfg.Endpoint),
		st:             State{Mode: ModePrimary},
	}
	w.ownID, _ = actions.DNSProxyRemovalCommand("https upstream " + cfg.Endpoint)
	if w.exec == nil {
		w.exec = actions.DefaultExec
	}
	if w.probeOwn == nil || w.probeCandidate == nil {
		np := newNetProber()
		if w.probeOwn == nil {
			w.probeOwn = func(ctx context.Context) error {
				return np.ProbeOwn(ctx, cfg.Endpoint, cfg.CanaryDomain, cfg.BootstrapIP)
			}
		}
		if w.probeCandidate == nil {
			w.probeCandidate = np.ProbeCandidate
		}
	}
	if w.now == nil {
		w.now = time.Now
	}
	if w.log == nil {
		w.log = slog.Default()
	}
	w.log = w.log.With("component", "dns_watchdog")
	w.publish()
	return w
}

// Run ticks immediately and then every Interval until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	interval := w.cfg.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	w.log.Info("dns watchdog started (runtime-only changes, never saved)",
		"endpoint", w.masked, "interval", interval, "fail_threshold", w.cfg.FailThreshold,
		"ok_threshold", w.cfg.OKThreshold, "cooldown", w.cfg.Cooldown)
	w.Tick(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx)
		}
	}
}

// Snapshot returns the state published by the last tick.
func (w *Watcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snap
}

// Tick runs one watchdog step: learn the mode from the router if not yet
// known; while the current mode is unproven, reconcile (which hands over to
// the normal path as soon as the router proves a mode by itself); then probe
// the own resolver, decide with hysteresis and cooldown, carry out a switch —
// or, when nothing switches, retry the cleanup once.
func (w *Watcher) Tick(ctx context.Context) {
	defer w.publish()
	defer w.noteForeignLeftover() // runs before publish
	if ctx.Err() != nil {
		return // shutting down: no new step, least of all a switch
	}
	if !w.ready && !w.init(ctx) {
		return
	}
	if w.saved.Pending != "" && !w.reconcile(ctx) {
		return
	}
	if w.idle && !w.recheckIdle(ctx) {
		return
	}

	probeErr := w.probeOwn(ctx)
	probeOK := probeErr == nil
	if probeOK {
		w.noLive = false
	}
	now := w.now()
	prev := w.st
	next, d := Decide(prev, probeOK, now, w.cfg)
	switch d.Kind {
	case KindToFallback:
		w.toFallback(ctx, prev, now, probeErr)
	case KindToPrimary:
		w.toPrimary(ctx, prev, now)
	default:
		w.st = next
		w.logNoSwitch(next, d, probeErr, now)
		w.cleanupStep(ctx, probeOK)
	}
}

// init derives the mode from the router's live config plus the state file:
//   - the record names lines the watchdog added, but neither they nor the own
//     line are on the router → DNS was changed by hand: record dropped, idle;
//   - own line present, nothing of the watchdog's on the router, nothing
//     pending → primary (after a reboot the saved config is back, whatever
//     the file says);
//   - nothing recorded and no own line → idle;
//   - anything else (a fallback, a pending switch, the watchdog's lines on
//     the router) → reconcile checks, in this same tick, which mode the router
//     proves — and acts only if it proves none.
func (w *Watcher) init(ctx context.Context) bool {
	lines, err := w.readUpstreams(ctx)
	if err != nil {
		w.log.Warn("dns watchdog: cannot read running-config — mode unknown, not acting", "err", err)
		return false
	}
	p, perr := w.load()
	if perr != nil {
		w.log.Warn("dns watchdog: state file unreadable — ignoring it", "path", w.cfg.StatePath, "err", perr)
	}
	own := w.ownLines(lines)
	applied := presentOf(lines, p.AppliedLines)
	w.ready = true
	switch {
	case goneByHand(own, applied, p):
		w.abandonRecord(p)
	case len(own) > 0 && len(applied) == 0 && p.Pending == "":
		w.st = State{Mode: ModePrimary, LastSwitch: p.LastSwitch}
		if p.Mode == ModeFallback {
			w.log.Warn("dns watchdog: state file says fallback, but the own resolver line is in running-config (router rebooted?) — primary",
				"fallback_since", p.LastSwitch)
			w.saved = persisted{Mode: ModePrimary, LastSwitch: p.LastSwitch}
			w.save(w.saved)
		} else {
			w.saved = p
			w.saved.Mode = ModePrimary
		}
		w.log.Info("dns watchdog: guarding the own resolver", "mode", ModePrimary, "endpoint", w.masked, "own_lines", len(own))
	case len(own) == 0 && p.Pending == "" && len(p.RemovedPrimaryLines) == 0 && len(p.AppliedLines) == 0:
		w.goIdle(p.LastSwitch, "")
		w.log.Warn("dns watchdog: no own-resolver line in running-config — nothing to guard, idle", "endpoint", w.masked)
	default:
		w.saved = p
		if w.saved.Pending == "" {
			w.saved.Pending = pendingMixed
		}
		w.st = State{Mode: modeOrPrimary(p.Mode), LastSwitch: p.LastSwitch}
		w.log.Info("dns watchdog: state file records a switch — checking what the router shows",
			"mode", p.Mode, "pending", p.Pending, "own_lines", len(own), "watchdog_lines", len(applied))
	}
	return true
}

// reconcile runs at start-up and while the current mode is unproven. It
// classifies the router exactly as the normal path does:
//   - an own line → primary is proven, whatever else is there: it is adopted
//     without acting (Pending clears, the stamp stays), foreign lines next to
//     it are cleanup — reported as foreign_leftover — and the tick continues
//     into the machine (hysteresis + cooldown) (true);
//   - no own line, and the record was not going to the fallback (a proven
//     primary, not a forward) → the own line was removed by hand: idle,
//     nothing re-added (false);
//   - no own line and a foreign line of the set, the record going to the
//     fallback → the fallback is adopted the same way (true);
//   - neither (no mode-defining line at all) → only here a single probe
//     decides: own resolver down → finish the forward; up → finish the
//     return (false).
//
// Neither the own line nor any line the watchdog added on the router → DNS
// was changed by hand: the record is dropped, idle.
func (w *Watcher) reconcile(ctx context.Context) bool {
	p := w.saved
	lines, err := w.readUpstreams(ctx)
	if err != nil {
		w.log.Warn("dns watchdog: unproven mode — running-config unreadable, retrying next tick", "pending", p.Pending, "err", err)
		return false
	}
	own := w.ownLines(lines)
	if goneByHand(own, presentOf(lines, p.AppliedLines), p) {
		w.abandonRecord(p)
		return false
	}
	switch {
	case len(own) > 0:
		w.adopt(ModePrimary, p, lines)
		return true
	case !goingToFallback(p):
		w.ownLineRemovedByHand(p)
		return false
	case anyPresent(lines, p.Foreign):
		w.adopt(ModeFallback, p, lines)
		return true
	}

	state := "neither_own_nor_foreign"
	probeErr := w.probeOwn(ctx)
	if ctx.Err() != nil {
		return false
	}
	now := w.now()
	sctx, cancel := switchContext(ctx)
	defer cancel()
	if probeErr != nil {
		w.log.Warn("dns watchdog: router in no safe mode, own resolver down — finishing the switch to the fallback",
			"router", state, "pending", p.Pending, "probe_err", w.mask(probeErr.Error()))
		w.finishForward(sctx, p, lines, stampIfSwitched(w.st, ModeFallback, now),
			appendLines(p.AppliedLines, p.SharedLines), "reconcile_to_fallback")
		return false
	}
	w.log.Warn("dns watchdog: router in no safe mode, own resolver up — finishing the return",
		"router", state, "pending", p.Pending)
	w.finishReturn(sctx, p, lines, stampIfSwitched(w.st, ModePrimary, now), "reconcile_to_primary")
	return false
}

// adopt settles an unproven mode by what the router shows, without acting:
// Pending clears, the mode's stamp (and the streak, if the mode is the same)
// stays, and what differs besides the mode-defining lines becomes cleanup.
func (w *Watcher) adopt(mode Mode, p persisted, lines []string) {
	if w.st.Mode != mode {
		w.st = State{Mode: mode, LastSwitch: p.LastSwitch}
	}
	w.saved = p
	w.saved.Mode, w.saved.Pending = mode, ""
	removedIDs, want := modeTargets(mode, p, w.ownID)
	w.saved.Leftover, w.saved.Missing = diffLines(lines, removedIDs, want)
	w.dropRecordIfClean()
	w.save(w.saved)
	level := slog.LevelInfo
	if p.Pending != pendingMixed || len(w.saved.Leftover)+len(w.saved.Missing) > 0 {
		level = slog.LevelWarn
	}
	w.log.Log(context.Background(), level, "dns watchdog: the router proves the mode by itself — adopted without acting",
		"mode", mode, "pending", p.Pending, "cleanup_leftover", w.maskAll(w.saved.Leftover), "cleanup_missing", w.maskAll(w.saved.Missing))
}

// cleanupStep retries the cleanup record once, in the normal path only: lines
// to remove and lines to add besides the ones that define the mode. It never
// changes the mode. If the router no longer proves the current mode: a
// primary that lost its own line was edited by hand (idle, nothing re-added);
// a fallback becomes pending again and reconcile looks at it on the next tick.
//
// In primary it waits while the own resolver's probe fails: the lines left
// behind are fallback lines, and taking them out then could leave the router
// on a failing resolver alone for a whole cooldown. The machine decides — if
// the own resolver stays down, the next forward takes them over.
func (w *Watcher) cleanupStep(ctx context.Context, probeOK bool) {
	if w.idle || w.saved.Pending != "" || len(w.saved.Leftover)+len(w.saved.Missing) == 0 || ctx.Err() != nil {
		return
	}
	if w.saved.Mode == ModePrimary && !probeOK {
		w.log.Warn("dns watchdog: cleanup held — the own resolver probe failed, the fallback lines left behind stay for now",
			"leftover", w.maskAll(w.saved.Leftover), "missing", w.maskAll(w.saved.Missing))
		return
	}
	sctx, cancel := switchContext(ctx)
	defer cancel()
	lines, err := w.readUpstreams(sctx)
	if err != nil {
		w.log.Warn("dns watchdog: cleanup — running-config unreadable, retrying next tick", "err", err)
		return
	}
	if !w.proves(w.saved.Mode, lines) {
		if w.saved.Mode == ModePrimary {
			w.ownLineRemovedByHand(w.saved)
			return
		}
		w.saved.Pending = pendingFor(w.saved.Mode)
		w.save(w.saved)
		w.log.Warn("dns watchdog: the router no longer shows the current mode — re-checking it next tick", "mode", w.saved.Mode)
		return
	}
	removedIDs, want := modeTargets(w.saved.Mode, w.saved, w.ownID)
	leftover, missing, _, err := w.settle(sctx, removedIDs, want)
	if err != nil {
		w.log.Warn("dns watchdog: cleanup — could not verify, retrying next tick", "err", err)
		return
	}
	w.saved.Leftover, w.saved.Missing = leftover, missing
	w.dropRecordIfClean()
	w.save(w.saved)
	if len(leftover)+len(missing) > 0 {
		w.log.Warn("dns watchdog: cleanup still outstanding — lines differ from the current mode",
			"mode", w.saved.Mode, "leftover", w.maskAll(leftover), "missing", w.maskAll(missing))
		return
	}
	w.log.Info("dns watchdog: cleanup done", "mode", w.saved.Mode)
}

// recheckIdle leaves idle once the own-resolver line appears.
func (w *Watcher) recheckIdle(ctx context.Context) bool {
	lines, err := w.readUpstreams(ctx)
	if err != nil {
		w.log.Warn("dns watchdog: cannot read running-config", "err", err)
		return false
	}
	if len(w.ownLines(lines)) == 0 {
		w.log.Debug("dns watchdog: still idle — no own-resolver line in running-config")
		return false
	}
	w.idle, w.idleReason = false, ""
	w.st = State{Mode: ModePrimary, LastSwitch: w.st.LastSwitch}
	w.log.Info("dns watchdog: own resolver line appeared in running-config — guarding", "endpoint", w.masked)
	return true
}

func (w *Watcher) logNoSwitch(st State, d Decision, probeErr error, now time.Time) {
	switch {
	case d.Reason == ReasonCooldown:
		w.log.Warn("dns watchdog: switch held back by cooldown", "mode", st.Mode,
			"fails", st.Fails, "oks", st.OKs, "cooldown_left", w.cfg.Cooldown-now.Sub(st.LastSwitch),
			"probe_err", w.mask(errString(probeErr)))
	case probeErr != nil && st.Mode == ModePrimary:
		w.log.Warn("dns watchdog: own resolver probe failed", "fails", st.Fails,
			"fail_threshold", w.cfg.FailThreshold, "err", w.mask(probeErr.Error()))
	case probeErr != nil:
		w.log.Info("dns watchdog: own resolver still down — staying on fallback", "err", w.mask(probeErr.Error()))
	case st.Mode == ModeFallback:
		w.log.Info("dns watchdog: own resolver answers again", "oks", st.OKs, "ok_threshold", w.cfg.OKThreshold)
	default:
		w.log.Debug("dns watchdog: own resolver ok")
	}
}

// toFallback swaps the own-resolver line(s) for the split fallback set built
// from the candidates that answer, mirroring the return: the set goes in
// FIRST, and the own line comes out only once a foreign line of the set is on
// the router; otherwise what was added is rolled back and the own line stays
// (no_live_fallback). No live foreign candidate → nothing is touched at all.
func (w *Watcher) toFallback(ctx context.Context, prev State, now time.Time, probeErr error) {
	hold := observe(prev, false) // the state if the switch does not happen
	lines, err := w.readUpstreams(ctx)
	if err != nil {
		w.st = hold
		w.log.Error("dns watchdog: own resolver down, but running-config is unreadable — switch postponed",
			"fails", hold.Fails, "err", err)
		return
	}
	own := w.ownLines(lines)
	if len(own) == 0 {
		w.goIdle(prev.LastSwitch, "")
		w.log.Warn("dns watchdog: own resolver down, but its line is no longer in running-config — nothing to switch, idle",
			"endpoint", w.masked)
		return
	}

	liveRU, liveForeign, dead := w.probeCandidates(ctx)
	if ctx.Err() != nil {
		w.st = hold
		w.log.Info("dns watchdog: shutting down — switch to fallback not started")
		return
	}
	set, ruDegraded, ok := BuildFallbackSet(w.cfg, liveRU, liveForeign)
	if !ok {
		w.st = hold
		w.noLive = true
		w.log.Error("dns watchdog: own resolver down and NO live fallback resolver — config left untouched",
			"decision", KindNoLiveFallback, "fails", hold.Fails, "probe_err", w.mask(errString(probeErr)),
			"live_ru", liveRU, "dead", dead)
		return
	}
	w.noLive = false

	// Lines an earlier switch left behind (cleanup leftovers) are the
	// watchdog's own: they join this switch's applied lines instead of
	// passing for pre-existing ones. Lines it still owed the router (cleanup
	// missing) are carried over, so the return restores them.
	var prevLeft []string
	for _, l := range presentOf(lines, w.saved.Leftover) {
		if id, ok := actions.DNSProxyRemovalCommand(l); ok && id != w.ownID {
			prevLeft = append(prevLeft, l) // never the own line: the return would take it out
		}
	}
	var owedOwn, owedShared []string
	for _, l := range w.saved.Missing {
		if id, ok := actions.DNSProxyRemovalCommand(l); ok && id == w.ownID {
			owedOwn = append(owedOwn, l)
		} else {
			owedShared = append(owedShared, l)
		}
	}

	present := make(map[string]bool, len(lines))
	for _, l := range lines {
		present[normalizeLine(l)] = true
	}
	var add []string
	for _, l := range set {
		if !present[normalizeLine(l)] {
			add = append(add, l)
		}
	}
	applied := appendUnique(add, prevLeft)
	// A pre-existing line that shares an identifier with a line the watchdog
	// owns — added now or folded in from an earlier switch — would be wiped by
	// the identifier-only removal on the way back: remember it.
	appliedIDs := idSet(applied)
	ours := make(map[string]bool, len(prevLeft))
	for _, l := range prevLeft {
		ours[normalizeLine(l)] = true
	}
	var shared []string
	for _, l := range lines {
		if id, ok := actions.DNSProxyRemovalCommand(l); ok && appliedIDs[id] && id != w.ownID && !ours[normalizeLine(l)] {
			shared = append(shared, l)
		}
	}
	// The previous record's shared lines still on the router stay owed.
	shared = appendUnique(shared, presentOf(lines, w.saved.SharedLines))
	shared = appendUnique(shared, owedShared)
	var foreign []string
	for _, l := range set {
		if !strings.Contains(l, " domain ") {
			foreign = append(foreign, l)
		}
	}
	ru := ""
	if !ruDegraded {
		ru = liveRU[0]
	}

	// One switch is one ndmc sequence, on a detached context: a shutdown must
	// not cut it between adding the fallback and removing the own line.
	sctx, cancel := switchContext(ctx)
	defer cancel()

	// Write-ahead: the switch's direction and the lines it will touch, under
	// the mode and cooldown stamp the router still has — a restart must be
	// able to finish or undo it, and must not inherit a cooldown from a switch
	// that never completed.
	w.saved = persisted{Mode: ModePrimary, LastSwitch: prev.LastSwitch, Pending: pendingForward,
		RemovedPrimaryLines: appendUnique(own, owedOwn), AppliedLines: applied, SharedLines: shared,
		Foreign: foreign, RU: ru, RUDegraded: ruDegraded}
	w.save(w.saved)

	// 1. The fallback set goes in first, next to the own line.
	addStatus, addOut := actions.ApplyDNSProxyUpstreams(sctx, w.boundedExec, nil, add)
	// 2. Only a foreign line that is really on the router carries the traffic.
	after, rerr := w.readUpstreams(sctx)
	if rerr != nil || !anyPresent(after, foreign) {
		w.rollbackForward(ctx, prev, now, forwardAttempt{own: own, applied: applied, shared: shared,
			readErr: rerr, addStatus: addStatus, addOut: addOut, probeErr: probeErr})
		return
	}
	// 3. Only now does the own line come out.
	status := w.finishForward(sctx, w.saved, after, now, set, KindToFallback)
	w.log.Warn("dns watchdog: SWITCHED to fallback — own resolver down",
		"reason", ReasonOwnDown, "fails", prev.Fails+1, "probe_err", w.mask(errString(probeErr)),
		"foreign", foreign, "ru", ru, "ru_degraded", ruDegraded, "dead", dead,
		"removed", w.maskAll(own), "added", len(add), "add_apply", addStatus, "remove_apply", status)
	if addStatus != "ok" {
		w.log.Warn("dns watchdog: some fallback lines were rejected by the router", "transcript", w.mask(addOut))
	}
}

// toPrimary returns to the own resolver (finishReturn). If the own lines don't
// go back, the fallback stays and the return is retried on the next tick.
func (w *Watcher) toPrimary(ctx context.Context, prev State, now time.Time) {
	hold := observe(prev, true)
	lines, err := w.readUpstreams(ctx)
	if err != nil {
		w.st = hold
		w.log.Error("dns watchdog: own resolver answers again, but running-config is unreadable — return postponed", "err", err)
		return
	}
	if ctx.Err() != nil {
		w.st = hold
		w.log.Info("dns watchdog: shutting down — return to the own resolver not started")
		return
	}
	// One switch is one ndmc sequence, on a detached context (see SwitchTimeout).
	sctx, cancel := switchContext(ctx)
	defer cancel()

	// Write-ahead: the return is under way.
	w.saved.Pending = pendingReturn
	w.save(w.saved)
	p := w.saved
	if !w.finishReturn(sctx, p, lines, now, KindToPrimary) {
		w.st = hold
		return
	}
	w.log.Warn("dns watchdog: SWITCHED back to the own resolver",
		"reason", ReasonOwnBack, "oks", prev.OKs+1, "fallback_since", p.LastSwitch,
		"restored", w.maskAll(p.RemovedPrimaryLines), "removed", len(p.AppliedLines))
}

// finishForward completes a forward switch: the own-resolver line(s) found in
// lines come out and the result is settled against want (the fallback set).
// The mode is fallback; Pending stays only if the router does not prove it
// (an own line still there, no foreign line, or no re-read). Everything else
// that differs is cleanup. The record — the way back — is kept. Returns the
// removal's apply status.
func (w *Watcher) finishForward(sctx context.Context, p persisted, lines []string, lastSwitch time.Time, want []string, kind string) string {
	status, out := "ok", ""
	own := w.ownLines(lines)
	if len(own) > 0 {
		status, out = actions.ApplyDNSProxyUpstreams(sctx, w.boundedExec, own, nil)
	}
	leftover, missing, final, verr := w.settle(sctx, map[string]bool{w.ownID: true}, want)
	if verr != nil && len(leftover) == 0 {
		leftover = own // not verified gone
	}
	w.st = State{Mode: ModeFallback, LastSwitch: lastSwitch}
	w.noLive = false
	w.saved = p
	w.saved.Mode, w.saved.LastSwitch = ModeFallback, lastSwitch
	w.saved.Leftover, w.saved.Missing = leftover, missing
	w.saved.Pending = pendingForward
	if verr == nil && w.proves(ModeFallback, final) {
		w.saved.Pending = ""
	}
	w.save(w.saved)
	w.logResult(kind, status, out, leftover, missing, verr)
	return status
}

// finishReturn completes a return: the saved own-resolver lines go back FIRST
// (next to whatever fallback is there, so the router is never left without
// upstreams) and an own line is verified on the router — that proves the
// primary mode, since the removals that follow never touch it. Then the lines
// the watchdog added come out; what differs afterwards is cleanup, and the
// record is dropped only when there is none. False when no own line could be
// put back: nothing else is touched.
func (w *Watcher) finishReturn(sctx context.Context, p persisted, lines []string, lastSwitch time.Time, kind string) bool {
	var ownAdd []string
	for _, l := range p.RemovedPrimaryLines {
		if !anyPresent(lines, []string{l}) {
			ownAdd = append(ownAdd, l)
		}
	}
	if len(ownAdd) > 0 {
		status, out := actions.ApplyDNSProxyUpstreams(sctx, w.boundedExec, nil, ownAdd)
		var err error
		lines, err = w.readUpstreams(sctx)
		if err != nil || len(w.ownLines(lines)) == 0 {
			w.saved = p
			w.saved.Pending = pendingReturn
			if err == nil && w.proves(ModeFallback, lines) {
				w.saved.Pending = "" // the fallback is still what the router shows
			}
			w.save(w.saved)
			w.log.Error("dns watchdog: own resolver line could not be put back — the fallback stays, will retry",
				"switch", kind, "apply", status, "err", err, "transcript", w.mask(out))
			return false
		}
	} else if len(w.ownLines(lines)) == 0 {
		w.log.Error("dns watchdog: no own-resolver line recorded to put back — cannot return", "switch", kind)
		return false
	}

	toRemove := presentOf(lines, p.AppliedLines)
	status, out := "ok", ""
	if len(toRemove) > 0 {
		status, out = actions.ApplyDNSProxyUpstreams(sctx, w.boundedExec, toRemove, nil)
	}
	leftover, missing, _, verr := w.settle(sctx, idSet(p.AppliedLines), appendLines(p.RemovedPrimaryLines, p.SharedLines))
	if verr != nil && len(leftover) == 0 {
		leftover = toRemove // not verified gone
	}
	w.st = State{Mode: ModePrimary, LastSwitch: lastSwitch}
	w.saved = p
	w.saved.Mode, w.saved.LastSwitch, w.saved.Pending = ModePrimary, lastSwitch, ""
	w.saved.Leftover, w.saved.Missing = leftover, missing
	w.dropRecordIfClean()
	w.save(w.saved)
	w.logResult(kind, status, out, leftover, missing, verr)
	return true
}

// settle re-reads running-config after a switch and compares it with the
// intended result: no line of a removed identifier may remain unless it is
// wanted, and every wanted line must be present. Each line that should be
// gone gets one more removal, re-reading in between — nobody has verified
// that `no https upstream <URL>` drops every line of the URL at once. Lines
// still missing then get one more add. Whatever still differs is returned,
// with the last lines read.
func (w *Watcher) settle(ctx context.Context, removedIDs map[string]bool, want []string) (leftover, missing, lines []string, err error) {
	lines, err = w.readUpstreams(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	leftover, missing = diffLines(lines, removedIDs, want)
	for budget := len(leftover); budget > 0 && len(leftover) > 0; budget-- {
		w.log.Warn("dns watchdog: line still present after removal — retrying", "line", w.mask(leftover[0]))
		actions.ApplyDNSProxyUpstreams(ctx, w.boundedExec, leftover[:1], nil)
		if lines, err = w.readUpstreams(ctx); err != nil {
			return leftover, missing, nil, err
		}
		leftover, missing = diffLines(lines, removedIDs, want)
	}
	if len(missing) > 0 {
		w.log.Info("dns watchdog: adding intended lines not present", "lines", w.maskAll(missing))
		actions.ApplyDNSProxyUpstreams(ctx, w.boundedExec, nil, missing)
		if lines, err = w.readUpstreams(ctx); err != nil {
			return leftover, missing, nil, err
		}
		leftover, missing = diffLines(lines, removedIDs, want)
	}
	return leftover, missing, lines, nil
}

func (w *Watcher) logResult(kind, status, out string, leftover, missing []string, verr error) {
	if verr != nil {
		w.log.Error("dns watchdog: cannot re-read running-config to verify the switch — record kept, will retry",
			"switch", kind, "err", verr, "maybe_left", w.maskAll(leftover))
	}
	if len(leftover) > 0 || len(missing) > 0 {
		w.log.Error("dns watchdog: switch is PARTIAL — router config still differs from the intended set after a retry",
			"switch", kind, "result", "partial", "leftover", w.maskAll(leftover), "missing", w.maskAll(missing),
			"transcript", w.mask(out))
		return
	}
	if status != "ok" {
		w.log.Warn("dns watchdog: some ndmc commands failed, but the router config matches the intended set",
			"switch", kind, "transcript", w.mask(out))
	}
}

// probeCandidates probes every RU candidate (RUCanary) and foreign candidate
// (CanaryDomain) in parallel and returns the live ones in config order.
func (w *Watcher) probeCandidates(ctx context.Context) (liveRU, liveForeign, dead []string) {
	type job struct {
		line, domain string
		ru           bool
	}
	var jobs []job
	for _, l := range w.cfg.RUCandidates {
		jobs = append(jobs, job{l, w.cfg.RUCanary, true})
	}
	for _, l := range w.cfg.ForeignCandidates {
		jobs = append(jobs, job{l, w.cfg.CanaryDomain, false})
	}
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = w.probeCandidate(ctx, jobs[i].line, jobs[i].domain)
		}(i)
	}
	wg.Wait()
	for i, j := range jobs {
		switch {
		case errs[i] != nil:
			dead = append(dead, j.line+": "+errs[i].Error())
		case j.ru:
			liveRU = append(liveRU, j.line)
		default:
			liveForeign = append(liveForeign, j.line)
		}
	}
	return liveRU, liveForeign, dead
}

func (w *Watcher) readUpstreams(ctx context.Context) ([]string, error) {
	out, err := w.boundedExec(ctx, "ndmc", "-c", "show running-config")
	if err != nil {
		return nil, fmt.Errorf("show running-config: %w", err)
	}
	return actions.ParseDNSProxyUpstreams(string(out)), nil
}

func (w *Watcher) boundedExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, ndmcTimeout)
	defer cancel()
	return w.exec(c, name, args...)
}

// ownLines returns the running-config lines of the own resolver (its
// identifier: `https upstream <Endpoint>`, whatever qualifiers follow).
func (w *Watcher) ownLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if id, ok := actions.DNSProxyRemovalCommand(l); ok && id == w.ownID {
			out = append(out, l)
		}
	}
	return out
}

// proves reports whether lines carry the lines that define mode: primary —
// an own line; fallback — no own line and a foreign line of the set.
func (w *Watcher) proves(mode Mode, lines []string) bool {
	if mode == ModeFallback {
		return len(w.ownLines(lines)) == 0 && anyPresent(lines, w.saved.Foreign)
	}
	return len(w.ownLines(lines)) > 0
}

func (w *Watcher) save(p persisted) {
	path := w.cfg.StatePath
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.log.Error("dns watchdog: state dir", "path", path, "err", err)
		return
	}
	body, err := json.Marshal(p)
	if err != nil {
		w.log.Error("dns watchdog: state encode", "err", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		w.log.Error("dns watchdog: state write", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		w.log.Error("dns watchdog: state rename", "path", path, "err", err)
	}
}

func (w *Watcher) load() (persisted, error) {
	var p persisted
	if w.cfg.StatePath == "" {
		return p, nil
	}
	body, err := os.ReadFile(w.cfg.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return persisted{}, err
	}
	return p, nil
}

func (w *Watcher) publish() {
	s := Snapshot{
		Ready:          w.ready,
		Idle:           w.idle,
		Mode:           w.st.Mode,
		Since:          w.st.LastSwitch,
		NoLiveFallback: w.noLive,
		Leftover:       w.maskAll(w.saved.Leftover),
		Missing:        w.maskAll(w.saved.Missing),
		Fails:          w.st.Fails,
	}
	if w.idle {
		s.IdleReason = w.idleReason
	}
	if fl := w.foreignLeftover(); len(fl) > 0 {
		s.ForeignLeftover = w.maskAll(fl)
		s.LeftoverSince = w.saved.LeftoverSince
	}
	if s.Mode == "" {
		s.Mode = ModePrimary
	}
	if s.Mode == ModeFallback {
		s.Foreign = w.maskAll(w.saved.Foreign)
		s.RU = w.saved.RU
		s.RUDegraded = w.saved.RUDegraded
	}
	w.mu.Lock()
	w.snap = s
	w.mu.Unlock()
}

func (w *Watcher) mask(s string) string { return maskSecret(s, w.cfg.Endpoint) }

func (w *Watcher) maskAll(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = w.mask(l)
	}
	return out
}

// normalizeLine is the comparison form of a dns-proxy line: single spaces and
// a tls upstream's default :853 dropped — the router may echo it or not.
func normalizeLine(line string) string {
	f := strings.Fields(line)
	if len(f) >= 3 && f[0] == "tls" && f[1] == "upstream" {
		if host, port, err := net.SplitHostPort(f[2]); err == nil && port == "853" {
			f[2] = host
		}
	}
	return strings.Join(f, " ")
}

// diffLines compares live lines with the intended result of a switch.
func diffLines(lines []string, removedIDs map[string]bool, want []string) (leftover, missing []string) {
	wanted := make(map[string]bool, len(want))
	for _, l := range want {
		wanted[normalizeLine(l)] = true
	}
	have := make(map[string]bool, len(lines))
	for _, l := range lines {
		n := normalizeLine(l)
		have[n] = true
		if id, ok := actions.DNSProxyRemovalCommand(l); ok && removedIDs[id] && !wanted[n] {
			leftover = append(leftover, l)
		}
	}
	for _, l := range want {
		if !have[normalizeLine(l)] {
			missing = append(missing, l)
		}
	}
	return leftover, missing
}

func idSet(lines []string) map[string]bool {
	out := make(map[string]bool, len(lines))
	for _, l := range lines {
		if id, ok := actions.DNSProxyRemovalCommand(l); ok {
			out[id] = true
		}
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// goIdle: nothing to guard (no own-resolver line on the router). Whatever
// outage the watchdog reported before is no longer its to report.
func (w *Watcher) goIdle(lastSwitch time.Time, reason string) {
	w.idle, w.idleReason = true, reason
	w.noLive = false
	w.st = State{Mode: ModePrimary, LastSwitch: lastSwitch}
}

// goneByHand: the record names lines the watchdog added, but neither they nor
// the own line are on the router — someone changed DNS by hand.
func goneByHand(own, appliedPresent []string, p persisted) bool {
	return len(own) == 0 && len(p.AppliedLines) > 0 && len(appliedPresent) == 0
}

// abandonRecord: re-adding lines from an old record would fight the operator
// who cleaned DNS up by hand. The record is dropped and the watchdog goes
// idle — loudly, and with the reason in the check.
func (w *Watcher) abandonRecord(p persisted) {
	w.log.Error("dns watchdog: state file records a switch, but neither the own resolver line nor any fallback line it added is on the router — DNS changed by hand; nothing re-added, idle",
		"pending", p.Pending, "recorded_own", w.maskAll(p.RemovedPrimaryLines), "recorded_applied", len(p.AppliedLines))
	w.saved = persisted{Mode: ModePrimary, LastSwitch: p.LastSwitch}
	w.save(w.saved)
	w.goIdle(p.LastSwitch, idleRecordDropped)
}

// goingToFallback: the record is a fallback, or a forward switch towards it.
// Anything else is a primary — one that loses its own line was edited by hand.
func goingToFallback(p persisted) bool {
	return p.Mode == ModeFallback || p.Pending == pendingForward
}

// ownLineRemovedByHand: a primary whose own line is gone although the
// watchdog never removed it — someone edited DNS by hand. It is not a
// fallback, and the own line is not put back: idle, saying why. The record of
// lines is kept (nothing pending), so the cleanup resumes if the own line
// comes back.
func (w *Watcher) ownLineRemovedByHand(p persisted) {
	w.saved = p
	w.saved.Mode, w.saved.Pending = ModePrimary, ""
	w.save(w.saved)
	w.goIdle(p.LastSwitch, idleOwnLineRemoved)
	w.log.Warn("dns watchdog: the own resolver line was removed from running-config by hand — not a fallback, nothing re-added, idle",
		"endpoint", w.masked, "left_by_watchdog", w.maskAll(p.Leftover))
}

// foreignLeftover: in a guarded primary, the leftover lines that serve every
// zone (no `domain`) — the foreign part of a fallback set, racing the own
// resolver and bypassing its filtering.
func (w *Watcher) foreignLeftover() []string {
	if !w.ready || w.idle || w.st.Mode != ModePrimary {
		return nil
	}
	var out []string
	for _, l := range w.saved.Leftover {
		if !isDomainLine(l) {
			out = append(out, l)
		}
	}
	return out
}

// noteForeignLeftover stamps when a foreign leftover was first seen (kept in
// the state file across restarts), clears the stamp once it is gone, and says
// so at ERROR on every tick while it lasts — the removal is retried every tick
// by the cleanup.
func (w *Watcher) noteForeignLeftover() {
	fl := w.foreignLeftover()
	if len(fl) == 0 {
		if !w.saved.LeftoverSince.IsZero() {
			w.saved.LeftoverSince = time.Time{}
			w.save(w.saved)
		}
		return
	}
	if w.saved.LeftoverSince.IsZero() {
		w.saved.LeftoverSince = w.now().UTC()
		w.save(w.saved)
	}
	w.log.Error("dns watchdog: UNSAFE — fallback resolvers are still on the router next to the own one: part of the DNS queries bypass it and its filtering; removal retried every tick",
		"reason", DetailReasonForeignLeftover, "leftover", w.maskAll(fl), "since", w.saved.LeftoverSince)
}

// isDomainLine: a dns-proxy line bound to a zone (`… domain <zone>`).
func isDomainLine(line string) bool {
	for _, f := range strings.Fields(line) {
		if f == "domain" {
			return true
		}
	}
	return false
}

// dropRecordIfClean: a proven primary with no cleanup needs no record.
func (w *Watcher) dropRecordIfClean() {
	s := w.saved
	if s.Mode == ModePrimary && s.Pending == "" && len(s.Leftover) == 0 && len(s.Missing) == 0 {
		w.saved = persisted{Mode: ModePrimary, LastSwitch: s.LastSwitch}
	}
}

// forwardAttempt is what a forward switch had done when it found that no
// foreign line of the fallback set was on the router.
type forwardAttempt struct {
	own, applied, shared []string // applied: every line the watchdog owns (this switch's and earlier leftovers)
	readErr              error    // re-reading after the adds failed
	addStatus, addOut    string
	probeErr             error
}

// rollbackForward undoes a forward switch whose fallback set did not take
// hold: the watchdog's lines on the router come out again, pre-existing lines
// sharing their identifiers are put back, and the own line — never removed —
// keeps serving. It is reported as no_live_fallback and stamps the cooldown,
// so rejected adds don't churn the config every tick. It runs on its own
// switch budget (the forward's may be spent). Primary is proven only if the
// router was re-read (pending return otherwise); what may be left is cleanup,
// and the record is kept for it.
func (w *Watcher) rollbackForward(ctx context.Context, prev State, now time.Time, a forwardAttempt) {
	rctx, cancel := switchContext(ctx)
	defer cancel()
	record := w.saved // the forward's write-ahead record
	lines, err := w.readUpstreams(rctx)
	toRemove := a.applied
	if err == nil {
		toRemove = presentOf(lines, a.applied)
	}
	status, out := "ok", ""
	if len(toRemove) > 0 {
		status, out = actions.ApplyDNSProxyUpstreams(rctx, w.boundedExec, toRemove, nil)
	}
	leftover, missing, final, verr := w.settle(rctx, idSet(a.applied), appendLines(a.own, a.shared))
	if verr != nil && len(leftover) == 0 {
		leftover = toRemove // not verified gone
	}
	w.st = State{Mode: ModePrimary, Fails: prev.Fails + 1, LastSwitch: now}
	w.noLive = true
	w.saved = record
	w.saved.Mode, w.saved.LastSwitch = ModePrimary, now
	w.saved.Leftover, w.saved.Missing = leftover, missing
	w.saved.Pending = pendingReturn
	if verr == nil && len(w.ownLines(final)) > 0 {
		w.saved.Pending = ""
	}
	w.dropRecordIfClean()
	w.save(w.saved)
	w.log.Error("dns watchdog: own resolver down, but no fallback line took hold on the router — rolled back, own resolver line kept",
		"decision", KindNoLiveFallback, "fails", prev.Fails+1, "probe_err", w.mask(errString(a.probeErr)),
		"add_apply", a.addStatus, "read_err", errString(a.readErr), "rolled_back", len(toRemove),
		"transcript", w.mask(a.addOut))
	w.logResult("rollback", status, out, leftover, missing, verr)
}

// modeTargets is what a mode means for the watchdog's lines: the identifiers
// that must not stay (except wanted lines) and the lines that must be there.
// Primary: none of the added lines, the own and shared lines back.
// Fallback: no own line, the added and shared lines there.
func modeTargets(mode Mode, p persisted, ownID string) (map[string]bool, []string) {
	if mode == ModeFallback {
		return map[string]bool{ownID: true}, appendLines(p.AppliedLines, p.SharedLines)
	}
	return idSet(p.AppliedLines), appendLines(p.RemovedPrimaryLines, p.SharedLines)
}

func pendingFor(mode Mode) string {
	if mode == ModeFallback {
		return pendingForward
	}
	return pendingReturn
}

// stampIfSwitched is the cooldown stamp after reaching mode: now if that
// changes the mode, else the current stamp.
func stampIfSwitched(st State, mode Mode, now time.Time) time.Time {
	if st.Mode == mode {
		return st.LastSwitch
	}
	return now
}

func modeOrPrimary(m Mode) Mode {
	if m == ModeFallback {
		return ModeFallback
	}
	return ModePrimary
}

func appendLines(a, b []string) []string {
	return append(append([]string(nil), a...), b...)
}

// appendUnique appends the lines of b not already in a (normalized).
func appendUnique(a, b []string) []string {
	out := append([]string(nil), a...)
	seen := make(map[string]bool, len(a)+len(b))
	for _, l := range a {
		seen[normalizeLine(l)] = true
	}
	for _, l := range b {
		if n := normalizeLine(l); !seen[n] {
			seen[n] = true
			out = append(out, l)
		}
	}
	return out
}

// anyPresent reports whether any line of want is among lines.
func anyPresent(lines, want []string) bool {
	return len(presentOf(lines, want)) > 0
}

// presentOf returns the lines of want that are among lines.
func presentOf(lines, want []string) []string {
	have := make(map[string]bool, len(lines))
	for _, l := range lines {
		have[normalizeLine(l)] = true
	}
	var out []string
	for _, l := range want {
		if have[normalizeLine(l)] {
			out = append(out, l)
		}
	}
	return out
}
