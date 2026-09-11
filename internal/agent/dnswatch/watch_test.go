package dnswatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/actions"
)

const (
	ownEndpoint = "https://dns.example.com/secret-path"
	ownLine     = "https upstream https://dns.example.com/secret-path"
	maskedOwn   = "https upstream https://dns.example.com/***"
	// A pre-existing upstream the watchdog must never touch (router echo form).
	unrelated = "tls upstream 203.0.113.53:853 sni dot.example.com"
)

var errDown = errors.New("doh: context deadline exceeded")

// fakeRouter simulates the dns-proxy block of a Keenetic's live config:
// `show running-config` renders its lines (tls IP hosts echoed with :853, as
// the router does), `dns-proxy <line>` appends, and `dns-proxy no …` removes by
// identifier — every line of that identifier, unless onePerCall (then `no https
// upstream` drops only the first match: the unverified KeenOS behaviour) or the
// command is stuck (it "succeeds" and removes nothing). Every ndmc command is
// recorded, reads included.
type fakeRouter struct {
	mu         sync.Mutex
	lines      []string
	calls      []string
	onePerCall bool
	stuck      map[string]bool
	failOn     map[string]bool
	ctxAware   bool             // a command whose context is done fails unrun, like exec.CommandContext
	onCall     func(cmd string) // runs (lock held) after each command is recorded
}

func newRouter(lines ...string) *fakeRouter {
	return &fakeRouter{lines: lines, stuck: map[string]bool{}, failOn: map[string]bool{}}
}

func (r *fakeRouter) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != "ndmc" || len(args) != 2 || args[0] != "-c" {
		return nil, fmt.Errorf("unexpected exec: %s %v", name, args)
	}
	cmd := args[1]
	if r.ctxAware && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.calls = append(r.calls, cmd)
	if r.onCall != nil {
		r.onCall(cmd)
	}
	if r.failOn[cmd] {
		return []byte("ndmc: rejected"), errors.New("command rejected")
	}
	switch {
	case cmd == "show running-config":
		var b strings.Builder
		b.WriteString("system\n    hostname Keenetic\n!\ndns-proxy\n    rebind-protect auto\n")
		for _, l := range r.lines {
			b.WriteString("    " + l + "\n")
		}
		b.WriteString("!\ninterface Wireguard0\n    description vpn\n!\n")
		return []byte(b.String()), nil
	case strings.HasPrefix(cmd, "dns-proxy no "):
		if r.stuck[cmd] {
			return nil, nil
		}
		var kept []string
		removed := 0
		one := r.onePerCall && strings.HasPrefix(cmd, "dns-proxy no https ")
		for _, l := range r.lines {
			rc, _ := actions.DNSProxyRemovalCommand(l)
			if rc == cmd && !(one && removed > 0) {
				removed++
				continue
			}
			kept = append(kept, l)
		}
		if removed == 0 {
			return []byte("ndmc: no such upstream"), errors.New("not found")
		}
		r.lines = kept
		return nil, nil
	case strings.HasPrefix(cmd, "dns-proxy "):
		r.lines = append(r.lines, echoForm(strings.TrimPrefix(cmd, "dns-proxy ")))
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected ndmc command %q", cmd)
}

// echoForm is how the router shows a tls upstream with a bare IP: with :853.
func echoForm(line string) string {
	f := strings.Fields(line)
	if len(f) >= 3 && f[0] == "tls" && net.ParseIP(f[2]) != nil {
		f[2] += ":853"
	}
	return strings.Join(f, " ")
}

func (r *fakeRouter) snapshotLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

func (r *fakeRouter) setLines(lines ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = lines
}

func (r *fakeRouter) takeCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.calls
	r.calls = nil
	return c
}

func mutating(calls []string) []string {
	var out []string
	for _, c := range calls {
		if c != "show running-config" {
			out = append(out, c)
		}
	}
	return out
}

func sameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got %q\nwant %q", what, g, w)
	}
}

type fakeProbes struct {
	mu          sync.Mutex
	ownErr      error
	dead        map[string]bool
	probed      []string // "line|domain"
	onCandidate func()   // runs at the start of every candidate probe
}

func (p *fakeProbes) own(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ownErr
}

func (p *fakeProbes) setOwn(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ownErr = err
}

func (p *fakeProbes) candidate(_ context.Context, line, domain string) error {
	if p.onCandidate != nil {
		p.onCandidate()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probed = append(p.probed, line+"|"+domain)
	if p.dead[line] {
		return errors.New("remote error: tls: handshake failure")
	}
	return nil
}

func (p *fakeProbes) probeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.probed)
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type harness struct {
	t         *testing.T
	r         *fakeRouter
	p         *fakeProbes
	clk       *fakeClock
	statePath string
	logs      *bytes.Buffer
	w         *Watcher
}

func newHarness(t *testing.T, r *fakeRouter, dead ...string) *harness {
	t.Helper()
	h := &harness{
		t:         t,
		r:         r,
		p:         &fakeProbes{dead: map[string]bool{}},
		clk:       &fakeClock{now: t0},
		statePath: filepath.Join(t.TempDir(), "dns-watchdog-state.json"),
		logs:      &bytes.Buffer{},
	}
	for _, d := range dead {
		h.p.dead[d] = true
	}
	h.w = h.newWatcher()
	return h
}

// newWatcher builds a watcher over the harness' router, probes, clock and
// state file — a second call models an agent restart.
func (h *harness) newWatcher() *Watcher {
	cfg := Config{
		Endpoint:          ownEndpoint,
		CanaryDomain:      "example.com",
		RUCanary:          "ya.ru",
		Interval:          time.Minute,
		FailThreshold:     2,
		OKThreshold:       2,
		Cooldown:          5 * time.Minute,
		MaxForeign:        3,
		RUZones:           DefaultRUZones,
		RUCandidates:      DefaultRUCandidates,
		ForeignCandidates: DefaultForeignCandidates,
		PinnedZones:       DefaultPinnedZones,
		PinnedCandidate:   DefaultPinnedCandidate,
		StatePath:         h.statePath,
	}
	return New(cfg, Deps{
		Exec:           h.r.exec,
		ProbeOwn:       h.p.own,
		ProbeCandidate: h.p.candidate,
		Now:            h.clk.Now,
		Logger:         slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
}

func (h *harness) tick(after time.Duration) Snapshot {
	return h.tickCtx(context.Background(), after)
}

func (h *harness) tickCtx(ctx context.Context, after time.Duration) Snapshot {
	h.clk.now = h.clk.now.Add(after)
	h.w.Tick(ctx)
	return h.w.Snapshot()
}

// writeState puts a state file in place, e.g. the one an agent killed in the
// middle of a switch left behind.
func (h *harness) writeState(p persisted) {
	h.t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(h.statePath, body, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// goFallback: the own resolver fails two probes a minute apart (t0, t0+1m).
func (h *harness) goFallback() Snapshot {
	h.t.Helper()
	h.p.setOwn(errDown)
	h.tick(0)
	s := h.tick(time.Minute)
	if s.Mode != ModeFallback {
		h.t.Fatalf("expected fallback after two failed probes, got %+v\nlogs:\n%s", s, h.logs)
	}
	return s
}

// goPrimary: the own resolver answers two probes once the cooldown is over.
func (h *harness) goPrimary() Snapshot {
	h.t.Helper()
	h.p.setOwn(nil)
	h.tick(5 * time.Minute)
	return h.tick(time.Minute)
}

func (h *harness) state() persisted {
	h.t.Helper()
	body, err := os.ReadFile(h.statePath)
	if err != nil {
		h.t.Fatalf("read state: %v", err)
	}
	var p persisted
	if err := json.Unmarshal(body, &p); err != nil {
		h.t.Fatalf("state json: %v\n%s", err, body)
	}
	return p
}

func fullFallbackSet() []string {
	lines, _, _ := BuildFallbackSet(Config{
		MaxForeign: 3, RUZones: DefaultRUZones, PinnedZones: DefaultPinnedZones, PinnedCandidate: DefaultPinnedCandidate,
	}, DefaultRUCandidates, DefaultForeignCandidates)
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, echoForm(l))
	}
	return out
}

// TestWatch_NoLiveFallbackLeavesConfigUntouched: own resolver down, no foreign
// candidate answers → the router's config is not touched at all (breaking a
// slow-but-working resolver for a dead fallback is worse than doing nothing),
// the decision is no_live_fallback, logged as loudly as a switch, and the
// candidates are probed again on the next tick.
func TestWatch_NoLiveFallbackLeavesConfigUntouched(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated), DefaultForeignCandidates...)
	h.p.setOwn(errDown)
	h.tick(0)
	s := h.tick(time.Minute)

	if m := mutating(h.r.takeCalls()); len(m) != 0 {
		t.Fatalf("config must stay untouched, got ndmc %q", m)
	}
	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine, unrelated})
	if s.Mode != ModePrimary || !s.NoLiveFallback {
		t.Fatalf("snapshot = %+v, want primary + NoLiveFallback", s)
	}
	if h.p.probeCount() == 0 {
		t.Fatal("candidates must be probed before deciding")
	}
	if _, err := os.Stat(h.statePath); err == nil {
		if p := h.state(); p.Mode == ModeFallback || len(p.RemovedPrimaryLines) > 0 {
			t.Fatalf("no switch happened, state file must not claim one: %+v", p)
		}
	}
	if !strings.Contains(h.logs.String(), "level=ERROR") || !strings.Contains(h.logs.String(), KindNoLiveFallback) {
		t.Fatalf("no_live_fallback must be logged loudly, logs:\n%s", h.logs)
	}

	// Next tick: still down, candidates probed again, still untouched.
	before := h.p.probeCount()
	h.tick(time.Minute)
	if h.p.probeCount() <= before {
		t.Fatal("candidates must be re-probed while the own resolver stays down")
	}
	if m := mutating(h.r.takeCalls()); len(m) != 0 {
		t.Fatalf("config must stay untouched, got ndmc %q", m)
	}

	// The own resolver recovers → the alarm clears.
	h.p.setOwn(nil)
	if s := h.tick(time.Minute); s.NoLiveFallback || s.Mode != ModePrimary {
		t.Fatalf("after recovery snapshot = %+v", s)
	}
}

// TestWatch_SwitchesToSplitSetOfLiveCandidates: the fallback set is built
// from the candidates that answered — the first LIVE ones, not the first
// listed — each probed with its own canary.
func TestWatch_SwitchesToSplitSetOfLiveCandidates(t *testing.T) {
	yandexDoT, yandexDoH := DefaultRUCandidates[0], DefaultRUCandidates[1]
	quad9DoH, controlD, cfDoH, cfDoT, quad9DoT := DefaultForeignCandidates[0], DefaultForeignCandidates[1],
		DefaultForeignCandidates[2], DefaultForeignCandidates[3], DefaultForeignCandidates[4]
	h := newHarness(t, newRouter(ownLine, unrelated), yandexDoT, quad9DoH, cfDoH)
	s := h.goFallback()

	want := []string{unrelated, controlD, echoForm(cfDoT), echoForm(quad9DoT)}
	for _, z := range DefaultRUZones {
		want = append(want, yandexDoH+" domain "+z)
	}
	sameSet(t, "router lines in fallback", h.r.snapshotLines(), want)

	if !s.Since.Equal(t0.Add(time.Minute)) {
		t.Errorf("Since = %v, want the switch time", s.Since)
	}
	if !reflect.DeepEqual(s.Foreign, []string{controlD, cfDoT, quad9DoT}) || s.RU != yandexDoH || s.RUDegraded || s.NoLiveFallback {
		t.Errorf("snapshot = %+v", s)
	}
	for _, pr := range h.p.probed {
		line, domain, _ := strings.Cut(pr, "|")
		wantDomain := "example.com"
		if line == yandexDoT || line == yandexDoH {
			wantDomain = "ya.ru"
		}
		if domain != wantDomain {
			t.Errorf("candidate %q probed with %q, want %q", line, domain, wantDomain)
		}
	}
}

// TestWatch_ReturnRestoresSavedLines: going back removes every fallback line
// and puts the saved own-resolver line back — and adds it BEFORE removing the
// working fallback, so the router is never left without upstreams.
func TestWatch_ReturnRestoresSavedLines(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	sameSet(t, "router lines in fallback", h.r.snapshotLines(), append([]string{unrelated}, fullFallbackSet()...))
	h.r.takeCalls()

	s := h.goPrimary()
	if s.Mode != ModePrimary || len(s.Leftover) != 0 || len(s.Missing) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
	sameSet(t, "router lines after return", h.r.snapshotLines(), []string{ownLine, unrelated})

	calls := mutating(h.r.takeCalls())
	addOwn, firstRemoval := -1, -1
	for i, c := range calls {
		if c == "dns-proxy "+ownLine && addOwn < 0 {
			addOwn = i
		}
		if strings.HasPrefix(c, "dns-proxy no ") && firstRemoval < 0 {
			firstRemoval = i
		}
	}
	if addOwn < 0 || firstRemoval < 0 || addOwn > firstRemoval {
		t.Fatalf("own line must be re-added before the fallback is removed, calls: %q", calls)
	}
}

// TestWatch_ReturnAbortsWhenOwnLineCannotBeAdded: if the own line does not go
// back, the working fallback stays — the return is retried next tick.
func TestWatch_ReturnAbortsWhenOwnLineCannotBeAdded(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	fallback := h.r.snapshotLines()
	h.r.failOn["dns-proxy "+ownLine] = true
	h.r.takeCalls()

	s := h.goPrimary()
	if s.Mode != ModeFallback {
		t.Fatalf("return must be aborted, snapshot = %+v", s)
	}
	for _, c := range h.r.takeCalls() {
		if strings.HasPrefix(c, "dns-proxy no ") {
			t.Fatalf("fallback must not be removed when the own line did not go back, got %q", c)
		}
	}
	sameSet(t, "router lines", h.r.snapshotLines(), fallback)
	if !strings.Contains(h.logs.String(), "level=ERROR") {
		t.Fatalf("aborted return must be logged loudly, logs:\n%s", h.logs)
	}

	delete(h.r.failOn, "dns-proxy "+ownLine)
	if s := h.tick(time.Minute); s.Mode != ModePrimary {
		t.Fatalf("return must be retried on the next tick, snapshot = %+v", s)
	}
	sameSet(t, "router lines after retry", h.r.snapshotLines(), []string{ownLine, unrelated})
}

// TestWatch_RestartInFallbackDerivesModeFromRunningConfigAndState: an agent
// restart while on the fallback reads the mode from the router (own line
// absent) plus the state file (removed lines), keeps the switch time, and can
// still put the own line back.
func TestWatch_RestartInFallbackDerivesModeFromRunningConfigAndState(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	h.r.takeCalls()

	h.w = h.newWatcher() // agent restart
	s := h.tick(time.Minute)
	if s.Mode != ModeFallback || !s.Since.Equal(t0.Add(time.Minute)) {
		t.Fatalf("restarted snapshot = %+v, want fallback since the original switch", s)
	}
	if m := mutating(h.r.takeCalls()); len(m) != 0 {
		t.Fatalf("restart must not touch the config, got %q", m)
	}

	h.p.setOwn(nil)
	h.tick(4 * time.Minute)
	if s := h.tick(time.Minute); s.Mode != ModePrimary {
		t.Fatalf("restarted watcher must return, snapshot = %+v\nlogs:\n%s", s, h.logs)
	}
	sameSet(t, "router lines after return", h.r.snapshotLines(), []string{ownLine, unrelated})
}

// TestWatch_RebootRestoredSavedConfigIsPrimary: after a router reboot the
// saved config (own line) is back and the runtime fallback is gone — the
// state file's "fallback" must not override what the router shows.
func TestWatch_RebootRestoredSavedConfigIsPrimary(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	h.r.setLines(ownLine, unrelated) // reboot: saved config back
	h.r.takeCalls()

	h.w = h.newWatcher()
	s := h.tick(time.Minute)
	if s.Mode != ModePrimary {
		t.Fatalf("snapshot = %+v, want primary", s)
	}
	if m := mutating(h.r.takeCalls()); len(m) != 0 {
		t.Fatalf("no ndmc mutation expected, got %q", m)
	}
	if p := h.state(); p.Mode != ModePrimary || len(p.RemovedPrimaryLines) != 0 {
		t.Fatalf("state file must follow the router, got %+v", p)
	}
}

// TestWatch_IdleWhenOwnLineAbsent: a router whose config has no own-resolver
// line has nothing to guard — the watchdog never adds a fallback there, and
// starts guarding once the line appears.
func TestWatch_IdleWhenOwnLineAbsent(t *testing.T) {
	h := newHarness(t, newRouter(unrelated))
	h.p.setOwn(errDown)
	var s Snapshot
	for i := 0; i < 3; i++ {
		s = h.tick(time.Minute)
	}
	if m := mutating(h.r.takeCalls()); len(m) != 0 {
		t.Fatalf("idle watchdog must not touch the config, got %q", m)
	}
	if !s.Idle || s.Mode != ModePrimary {
		t.Fatalf("snapshot = %+v, want idle primary", s)
	}

	h.r.setLines(ownLine, unrelated)
	h.tick(time.Minute)
	if s := h.tick(time.Minute); s.Idle || s.Mode != ModeFallback {
		t.Fatalf("once the own line appears the watchdog guards it, snapshot = %+v", s)
	}
}

// TestWatch_UnreadableRunningConfigDoesNothing: without a readable config the
// mode can't be known — no probe decides anything until it can be read.
func TestWatch_UnreadableRunningConfigDoesNothing(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.r.failOn["show running-config"] = true
	h.p.setOwn(errDown)
	h.tick(0)
	s := h.tick(time.Minute)
	if s.Ready || s.Mode == ModeFallback {
		t.Fatalf("snapshot = %+v, want not ready", s)
	}
	if m := mutating(h.r.takeCalls()); len(m) != 0 {
		t.Fatalf("no mutation expected, got %q", m)
	}
	delete(h.r.failOn, "show running-config")
	h.tick(time.Minute)
	if s := h.tick(time.Minute); !s.Ready || s.Mode != ModeFallback {
		t.Fatalf("once readable the watchdog works, snapshot = %+v", s)
	}
}

// TestWatch_NeverSavesConfig: runtime only — a saved switch would outlive a
// night-time outage and strand the router on the fallback after a reboot. No
// path of the watchdog (switch, no-live, aborted return, retry, return) may
// run `system configuration save`.
func TestWatch_NeverSavesConfig(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated), DefaultForeignCandidates...)
	h.r.onePerCall = true
	var all []string
	h.p.setOwn(errDown)
	h.tick(0)
	h.tick(time.Minute) // no_live_fallback
	all = append(all, h.r.takeCalls()...)

	h.p.dead = map[string]bool{}
	h.tick(time.Minute) // switch
	h.r.failOn["dns-proxy "+ownLine] = true
	h.p.setOwn(nil)
	h.tick(5 * time.Minute)
	h.tick(time.Minute) // aborted return
	delete(h.r.failOn, "dns-proxy "+ownLine)
	s := h.tick(time.Minute) // return with one-line-per-call retries
	all = append(all, h.r.takeCalls()...)

	if s.Mode != ModePrimary {
		t.Fatalf("cycle must end in primary, snapshot = %+v\nlogs:\n%s", s, h.logs)
	}
	for _, c := range all {
		if strings.Contains(c, "configuration save") {
			t.Fatalf("the watchdog must never save the config, got ndmc %q", c)
		}
	}
}

// TestWatch_PreexistingLineSharingIdentifierSurvivesReturn: a line that was on
// the router before the switch and shares an identifier with a fallback line
// (`no tls upstream <host>` drops every line of that host) is not added twice
// and is back after the return.
func TestWatch_PreexistingLineSharingIdentifierSurvivesReturn(t *testing.T) {
	pre := "tls upstream common.dot.dns.yandex.net domain ru"
	h := newHarness(t, newRouter(ownLine, pre))
	h.goFallback()
	n := 0
	for _, l := range h.r.snapshotLines() {
		if l == pre {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("pre-existing line must stay exactly once in fallback, got %d in %q", n, h.r.snapshotLines())
	}
	if s := h.goPrimary(); s.Mode != ModePrimary || len(s.Leftover) != 0 || len(s.Missing) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
	sameSet(t, "router lines after return", h.r.snapshotLines(), []string{ownLine, pre})
}

// TestWatch_RetryClearsLeftoverWhenHTTPSRemovalTakesOneLinePerCall: nobody has
// verified that `no https upstream <URL>` drops every domain line of that URL.
// After each switch the watchdog re-reads the config and retries what should
// be gone — here each retry takes one more line until none is left.
func TestWatch_RetryClearsLeftoverWhenHTTPSRemovalTakesOneLinePerCall(t *testing.T) {
	t.Run("return", func(t *testing.T) {
		h := newHarness(t, newRouter(ownLine, unrelated))
		h.r.onePerCall = true
		h.goFallback()
		h.r.takeCalls()
		s := h.goPrimary()
		if s.Mode != ModePrimary || len(s.Leftover) != 0 {
			t.Fatalf("snapshot = %+v\nlogs:\n%s", s, h.logs)
		}
		sameSet(t, "router lines after return", h.r.snapshotLines(), []string{ownLine, unrelated})
		if p := h.state(); len(p.Leftover) != 0 {
			t.Fatalf("state leftover = %q", p.Leftover)
		}
		cf := 0
		for _, c := range h.r.takeCalls() {
			if c == "dns-proxy no https upstream https://cloudflare-dns.com/dns-query" {
				cf++
			}
		}
		if cf != 1+len(DefaultPinnedZones) {
			t.Errorf("cloudflare removals = %d, want 1 + one retry per leftover line (%d)", cf, 1+len(DefaultPinnedZones))
		}
	})
	t.Run("to fallback", func(t *testing.T) {
		ownDomain := ownLine + " domain example.org"
		h := newHarness(t, newRouter(ownLine, ownDomain, unrelated))
		h.r.onePerCall = true
		s := h.goFallback()
		if len(s.Leftover) != 0 {
			t.Fatalf("snapshot = %+v", s)
		}
		sameSet(t, "router lines in fallback", h.r.snapshotLines(), append([]string{unrelated}, fullFallbackSet()...))
		// Both own lines are saved in full and both come back.
		h.goPrimary()
		sameSet(t, "router lines after return", h.r.snapshotLines(), []string{ownLine, ownDomain, unrelated})
	})
}

// TestWatch_PersistentLeftoverIsPartial: a line that is still there after the
// retry makes the switch partial — logged loudly with the lines, kept in the
// state file and exposed in the snapshot.
func TestWatch_PersistentLeftoverIsPartial(t *testing.T) {
	stuck := "dns-proxy no https upstream https://cloudflare-dns.com/dns-query"
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.r.stuck[stuck] = true
	h.goFallback()
	s := h.goPrimary()

	var wantLeft []string
	for _, l := range fullFallbackSet() {
		if strings.HasPrefix(l, DefaultPinnedCandidate) {
			wantLeft = append(wantLeft, l)
		}
	}
	if s.Mode != ModePrimary {
		t.Fatalf("own line is back, mode must be primary, snapshot = %+v", s)
	}
	sameSet(t, "snapshot leftover", s.Leftover, wantLeft)
	sameSet(t, "state leftover", h.state().Leftover, wantLeft)
	logs := h.logs.String()
	if !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "leftover") || !strings.Contains(logs, "cloudflare-dns.com/dns-query domain tmdb.org") {
		t.Fatalf("partial switch must be logged loudly with the leftover lines, logs:\n%s", logs)
	}
	lines := h.r.snapshotLines()
	if !contains(lines, ownLine) {
		t.Fatalf("own line must be back, got %q", lines)
	}
}

// TestWatch_StateFileAfterSwitch: the state file (tmp + rename, owner-only —
// it holds the endpoint's secret path) carries what is needed to go back.
func TestWatch_StateFileAfterSwitch(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	p := h.state()
	if p.Mode != ModeFallback || !p.LastSwitch.Equal(t0.Add(time.Minute)) {
		t.Fatalf("state = %+v", p)
	}
	if !reflect.DeepEqual(p.RemovedPrimaryLines, []string{ownLine}) {
		t.Errorf("removed_primary_lines = %q", p.RemovedPrimaryLines)
	}
	want, _, _ := BuildFallbackSet(h.w.cfg, DefaultRUCandidates, DefaultForeignCandidates)
	if !reflect.DeepEqual(p.AppliedLines, want) {
		t.Errorf("applied_lines:\n got %q\nwant %q", p.AppliedLines, want)
	}
	if !reflect.DeepEqual(p.Foreign, DefaultForeignCandidates[:3]) || p.RU != DefaultRUCandidates[0] {
		t.Errorf("candidate info = foreign %q ru %q", p.Foreign, p.RU)
	}
	fi, err := os.Stat(h.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, want 0600", fi.Mode().Perm())
	}
	if _, err := os.Stat(h.statePath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file must be renamed away, stat err = %v", err)
	}

	h.goPrimary()
	p = h.state()
	if p.Mode != ModePrimary || len(p.RemovedPrimaryLines) != 0 || len(p.AppliedLines) != 0 || !p.LastSwitch.Equal(t0.Add(7*time.Minute)) {
		t.Fatalf("state after return = %+v", p)
	}
}

// TestWatch_SecretPathNeverInLogsOrSnapshot: the endpoint path is a
// credential. It is kept in the state file (needed to restore the line) but
// never logged and never put in the snapshot the backend receives.
func TestWatch_SecretPathNeverInLogsOrSnapshot(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.r.stuck["dns-proxy no https upstream "+ownEndpoint] = true
	h.p.setOwn(fmt.Errorf("doh: Get %q: context deadline exceeded", ownEndpoint+"?dns=AAAB"))
	h.tick(0)
	s := h.tick(time.Minute)
	if s.Mode != ModeFallback {
		t.Fatalf("snapshot = %+v", s)
	}
	if !reflect.DeepEqual(s.Leftover, []string{maskedOwn}) {
		t.Fatalf("leftover = %q, want the masked own line", s.Leftover)
	}
	if strings.Contains(h.logs.String(), "secret-path") {
		t.Fatalf("logs leak the endpoint path:\n%s", h.logs)
	}
	if strings.Contains(fmt.Sprintf("%+v", s), "secret-path") {
		t.Fatalf("snapshot leaks the endpoint path: %+v", s)
	}
}
