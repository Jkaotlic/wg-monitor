package dnswatch

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
)

func hasOwn(lines []string) bool {
	for _, l := range lines {
		if l == ownLine {
			return true
		}
	}
	return false
}

// hasForeignLine: a line of the foreign pool is on the router — something
// other than the own resolver carries the bulk of the traffic.
func hasForeignLine(lines []string) bool {
	for _, f := range foreignOf(rawFallbackSet()) {
		if contains(lines, echoForm(f)) {
			return true
		}
	}
	return false
}

// breakNDMCAfterFirstAdd: the adds of a forward switch go through, then every
// read and every removal fails (e.g. the switch's time budget is gone).
func breakNDMCAfterFirstAdd(h *harness) {
	sawAdd := false
	h.r.onCall = func(cmd string) {
		if sawAdd && (cmd == "show running-config" || strings.HasPrefix(cmd, "dns-proxy no ")) {
			h.r.failOn[cmd] = true
		}
		if isAdd(cmd) {
			sawAdd = true
		}
	}
}

func (h *harness) healNDMC() {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	h.r.onCall = nil
	h.r.failOn = map[string]bool{}
}

// TestWatch_RestartMidForwardOwnDeadFinishesTheForward: the agent was killed
// (S99 gives 10 s, deploy runs killall -9) after the fallback set went in but
// before the own line came out — and the own resolver is DEAD, which is why
// the switch ran. The restart must finish the forward, not put the router back
// on the dead resolver behind a cooldown.
//
// Round 4 ruling: own line + foreign lines is classified like in-process —
// primary adopted (the own line proves it), the foreign lines are cleanup,
// and the machine decides with its threshold. The foreign lines keep serving
// while the own resolver fails; the second failed probe finishes the switch.
func TestWatch_RestartMidForwardOwnDeadFinishesTheForward(t *testing.T) {
	set := rawFallbackSet()
	cases := []struct {
		name string
		rec  persisted
	}{
		{"state file without pending (first build)", persisted{Mode: ModeFallback, LastSwitch: t0,
			RemovedPrimaryLines: []string{ownLine}, AppliedLines: set, Foreign: foreignOf(set), RU: DefaultRUCandidates[0]}},
		{"write-ahead record, pending forward", persisted{Mode: ModePrimary, Pending: pendingForward,
			RemovedPrimaryLines: []string{ownLine}, AppliedLines: set, Foreign: foreignOf(set), RU: DefaultRUCandidates[0]}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, newRouter(append([]string{ownLine, unrelated}, fullFallbackSet()...)...))
			h.writeState(tc.rec)
			h.p.setOwn(errDown)
			reached := 0
			for m := 1; m <= 7; m++ {
				s := h.tick(time.Minute)
				lines := h.r.snapshotLines()
				if !hasForeignLine(lines) {
					t.Fatalf("t0+%dm: own resolver dead — the foreign lines must keep serving, got mode=%s own_line=%v\nlogs:\n%s",
						m, s.Mode, hasOwn(lines), h.logs)
				}
				if m == 1 && (s.Mode != ModePrimary || !hasOwn(lines)) {
					t.Fatalf("t0+1m: one failed probe must not flip the router, got mode=%s own_line=%v", s.Mode, hasOwn(lines))
				}
				fallback := s.Mode == ModeFallback && !hasOwn(lines)
				if reached > 0 && !fallback {
					t.Fatalf("t0+%dm: left the fallback reached at t0+%dm: mode=%s own_line=%v", m, reached, s.Mode, hasOwn(lines))
				}
				if reached == 0 && fallback {
					reached = m
				}
			}
			// The write-ahead record keeps the previous stamp: the second failed
			// probe switches. The first build's record stamped the switch itself
			// (t0), so there the cooldown holds it until t0+5m — with the foreign
			// lines serving all along.
			if reached == 0 {
				t.Fatalf("never reached the fallback in 7 min\nlogs:\n%s", h.logs)
			}
			if tc.rec.Pending == pendingForward && reached != 2 {
				t.Fatalf("write-ahead record: fallback at t0+%dm, want t0+2m (fail threshold, no inherited cooldown)", reached)
			}
			sameSet(t, "router lines", h.r.snapshotLines(), append([]string{unrelated}, fullFallbackSet()...))
			if p := h.state(); p.Pending != "" || p.Mode != ModeFallback || len(p.AppliedLines) != len(set) {
				t.Fatalf("state = %+v, want a proven fallback that keeps the way back", p)
			}
			// The way back survived: the own resolver answers → primary.
			if s := h.goPrimary(); s.Mode != ModePrimary {
				t.Fatalf("return after recovery: %+v", s)
			}
			sameSet(t, "router lines after return", h.r.snapshotLines(), []string{ownLine, unrelated})
		})
	}
}

// TestWatch_WriteAheadRecordNamesTheDirection: the record written before the
// ndmc sequence says which way the switch goes and keeps the previous
// cooldown stamp — a restart must not inherit a cooldown from a switch that
// never finished.
func TestWatch_WriteAheadRecordNamesTheDirection(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	readRecord := func() persisted {
		body, err := os.ReadFile(h.statePath)
		if err != nil {
			t.Fatalf("no write-ahead record: %v", err)
		}
		var p persisted
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var fwd, ret persisted
	var gotFwd, gotRet bool
	h.r.onCall = func(cmd string) {
		switch {
		case cmd == ownRemoval && !gotFwd:
			gotFwd, fwd = true, readRecord()
		case cmd == "dns-proxy "+ownLine && !gotRet:
			gotRet, ret = true, readRecord()
		}
	}
	h.goFallback()
	if !gotFwd {
		t.Fatal("own line never removed")
	}
	if fwd.Pending != pendingForward || !fwd.LastSwitch.IsZero() ||
		!reflect.DeepEqual(fwd.RemovedPrimaryLines, []string{ownLine}) || len(fwd.AppliedLines) != len(rawFallbackSet()) {
		t.Fatalf("forward write-ahead record = %+v, want pending forward, the previous (zero) stamp, own + applied lines", fwd)
	}
	h.goPrimary()
	if !gotRet {
		t.Fatal("own line never re-added")
	}
	if ret.Pending != pendingReturn || !ret.LastSwitch.Equal(t0.Add(time.Minute)) || len(ret.AppliedLines) != len(rawFallbackSet()) {
		t.Fatalf("return write-ahead record = %+v, want pending return, the forward's stamp, the applied lines", ret)
	}
	if p := h.state(); p.Pending != "" || len(p.AppliedLines) != 0 {
		t.Fatalf("after a verified return the record is cleared, got %+v", p)
	}
}

// brokenRollback: own resolver dead, the forward's adds go in, then ndmc
// breaks — the rollback can neither remove nor verify anything.
func brokenRollback(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.p.setOwn(errDown)
	h.tick(0)
	breakNDMCAfterFirstAdd(h)
	s := h.tick(time.Minute)
	lines := h.r.snapshotLines()
	if !hasOwn(lines) || len(lines) != 2+len(rawFallbackSet()) {
		t.Fatalf("precondition: adds in, own line kept, got %q", lines)
	}
	if s.Mode != ModePrimary || len(s.Leftover) == 0 {
		t.Fatalf("lines the rollback could not verify gone must be reported, snapshot = %+v", s)
	}
	p := h.state()
	if p.Pending == "" || len(p.AppliedLines) != len(rawFallbackSet()) || !reflect.DeepEqual(p.RemovedPrimaryLines, []string{ownLine}) {
		t.Fatalf("an unverified rollback must keep the record of the lines it may have left, state = %+v", p)
	}
	c := Check{Source: h.w}.Run(context.Background(), checks.Deps{})
	if _, ok := c.Details["leftover"]; !ok {
		t.Fatalf("check must carry the leftover, got %s %#v", c.Status, c.Details)
	}
	return h
}

// TestWatch_RollbackThatCannotVerifyKeepsTheRecord: ndmc recovers, the agent
// restarts, the own resolver answers again — the fallback lines left behind
// are cleaned up instead of racing the own resolver forever.
func TestWatch_RollbackThatCannotVerifyKeepsTheRecord(t *testing.T) {
	h := brokenRollback(t)
	h.healNDMC()
	h.w = h.newWatcher()
	h.p.setOwn(nil)
	var s Snapshot
	for i := 0; i < 3; i++ {
		s = h.tick(time.Minute)
	}
	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine, unrelated})
	c := Check{Source: h.w}.Run(context.Background(), checks.Deps{})
	if s.Mode != ModePrimary || len(s.Leftover) != 0 || c.Status != "ok" {
		t.Fatalf("snapshot = %+v check = %s %#v", s, c.Status, c.Details)
	}
	if p := h.state(); p.Pending != "" || len(p.AppliedLines) != 0 {
		t.Fatalf("record must be cleared once verified, got %+v", p)
	}
}

// TestWatch_UnverifiedRollbackFinishesInProcess: no restart — ndmc recovers
// while the own resolver is still dead. The fallback lines that did go in
// carry DNS and stay; the forward is finished by the machine once the
// rollback's cooldown is over (round 4 ruling: own + foreign is a proven
// primary with cleanup, no single-probe flip).
func TestWatch_UnverifiedRollbackFinishesInProcess(t *testing.T) {
	h := brokenRollback(t)
	h.healNDMC()
	var s Snapshot
	for m := 1; m <= 6; m++ {
		s = h.tick(time.Minute)
		if lines := h.r.snapshotLines(); !hasForeignLine(lines) {
			t.Fatalf("t+%dm: own resolver dead — the foreign lines that went in must keep serving: %+v lines %q", m, s, lines)
		}
	}
	lines := h.r.snapshotLines()
	if s.Mode != ModeFallback || hasOwn(lines) || !hasForeignLine(lines) {
		t.Fatalf("snapshot = %+v, lines %q", s, lines)
	}
	sameSet(t, "router lines", lines, append([]string{unrelated}, fullFallbackSet()...))
}

// TestWatch_ReturnThatCannotVerifyKeepsTheRecord: the return put the own line
// back and issued the removals, but could not re-read the config — it must
// not claim a clean primary. The record stays, the leftover is reported, and
// once ndmc answers the next tick verifies and finishes.
func TestWatch_ReturnThatCannotVerifyKeepsTheRecord(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	removed := false
	h.r.onCall = func(cmd string) {
		if removed && cmd == "show running-config" {
			h.r.failOn[cmd] = true
		}
		if strings.HasPrefix(cmd, "dns-proxy no ") {
			removed = true
		}
	}
	s := h.goPrimary()
	if s.Mode != ModePrimary || len(s.Leftover) == 0 {
		t.Fatalf("unverified return must report what may be left, snapshot = %+v", s)
	}
	// Round 3 ruling: the own line was seen on the router before the removals,
	// so primary is proven (pending clears); what may be left is cleanup, and
	// the record of the lines is kept for it.
	if p := h.state(); p.Pending != "" || len(p.AppliedLines) == 0 || len(p.Leftover) == 0 {
		t.Fatalf("unverified return must keep the record as cleanup, state = %+v", p)
	}

	h.healNDMC()
	s = h.tick(time.Minute)
	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine, unrelated})
	if s.Mode != ModePrimary || len(s.Leftover) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
	if p := h.state(); p.Pending != "" || len(p.AppliedLines) != 0 {
		t.Fatalf("record must be cleared once verified, got %+v", p)
	}
}

// TestWatch_RestartWithAllFallbackLinesGoneByHandGoesIdle: the operator
// cleaned DNS up by hand while the agent was down — neither the own line nor
// any watchdog line is left. The watchdog does not re-add 17 lines from an
// old record: it goes idle and says so loudly.
func TestWatch_RestartWithAllFallbackLinesGoneByHandGoesIdle(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	h.r.setLines(unrelated)
	h.r.takeCalls()
	h.w = h.newWatcher()
	s := h.tick(time.Minute)
	if m := mutating(h.r.takeCalls()); len(m) != 0 {
		t.Fatalf("nothing may be re-added, got %q", m)
	}
	if !s.Idle || s.Mode != ModePrimary {
		t.Fatalf("snapshot = %+v, want idle", s)
	}
	if p := h.state(); len(p.AppliedLines) != 0 || len(p.RemovedPrimaryLines) != 0 || p.Pending != "" {
		t.Fatalf("stale record must be dropped, got %+v", p)
	}
	if !strings.Contains(h.logs.String(), "level=ERROR") {
		t.Fatalf("must be logged loudly, logs:\n%s", h.logs)
	}
}

// TestWatch_ReconcileDoesNotRepeat: once a reconcile has verified the router,
// a further restart finds nothing to do.
func TestWatch_ReconcileDoesNotRepeat(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	h.r.setLines(append(h.r.snapshotLines(), ownLine)...)
	h.w = h.newWatcher()
	h.p.setOwn(nil)
	h.tick(time.Minute)
	h.r.takeCalls()
	h.w = h.newWatcher()
	h.tick(time.Minute)
	if m := mutating(h.r.takeCalls()); len(m) != 0 {
		t.Fatalf("second restart mutated: %q", m)
	}
}

// TestWatch_RollbackRestoresSharedLine: a pre-existing line sharing an
// identifier with an added RU line survives the rollback.
func TestWatch_RollbackRestoresSharedLine(t *testing.T) {
	pre := "tls upstream common.dot.dns.yandex.net domain ru"
	h := newHarness(t, newRouter(ownLine, unrelated, pre))
	for _, l := range foreignOf(rawFallbackSet()) {
		h.r.failOn["dns-proxy "+l] = true
	}
	h.p.setOwn(errDown)
	h.tick(0)
	s := h.tick(time.Minute)
	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine, unrelated, pre})
	if s.Mode != ModePrimary || !s.NoLiveFallback || len(s.Leftover) != 0 || len(s.Missing) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
}

// TestWatch_PartialForeignSwitchReportsMissing: one foreign line took hold,
// the others were rejected — the switch goes ahead (DNS is carried) and the
// rejected lines are reported as missing.
func TestWatch_PartialForeignSwitchReportsMissing(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	f := foreignOf(rawFallbackSet())
	for _, l := range f[1:] {
		h.r.failOn["dns-proxy "+l] = true
	}
	h.p.setOwn(errDown)
	h.tick(0)
	s := h.tick(time.Minute)
	lines := h.r.snapshotLines()
	if s.Mode != ModeFallback || hasOwn(lines) || !hasForeignLine(lines) {
		t.Fatalf("snapshot = %+v lines %q", s, lines)
	}
	sameSet(t, "missing", s.Missing, f[1:])
	// Round 3 ruling: a foreign line carries DNS and the own line is gone —
	// the fallback is proven; the rejected lines are cleanup, not pending.
	if p := h.state(); p.Pending != "" {
		t.Fatalf("fallback is proven, the missing lines are cleanup — got pending %q", p.Pending)
	}
	c := Check{Source: h.w}.Run(context.Background(), checks.Deps{})
	if c.Details["reason"] != DetailReasonFallback || c.Details["missing"] == nil {
		t.Fatalf("check = %s %#v", c.Status, c.Details)
	}
}
