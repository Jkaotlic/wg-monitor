package dnswatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
)

// rawFallbackSet is the full fallback set (every candidate live) in add form.
func rawFallbackSet() []string {
	lines, _, _ := BuildFallbackSet(Config{
		MaxForeign: 3, RUZones: DefaultRUZones, PinnedZones: DefaultPinnedZones, PinnedCandidate: DefaultPinnedCandidate,
	}, DefaultRUCandidates, DefaultForeignCandidates)
	return lines
}

// foreignOf keeps the lines without a domain qualifier: the foreign pool.
func foreignOf(set []string) []string {
	var out []string
	for _, l := range set {
		if !strings.Contains(l, " domain ") {
			out = append(out, l)
		}
	}
	return out
}

func isAdd(cmd string) bool {
	return strings.HasPrefix(cmd, "dns-proxy ") && !strings.HasPrefix(cmd, "dns-proxy no ")
}

const ownRemoval = "dns-proxy no https upstream " + ownEndpoint

// TestWatch_ForwardAddsRejectedKeepsOwnLine: if the router rejects every
// fallback line, the own line must stay — removing it first would leave the
// router with no upstream at all while the check claimed "on fallback, sites
// work". Nothing took hold → rolled back, primary, reported no_live_fallback.
func TestWatch_ForwardAddsRejectedKeepsOwnLine(t *testing.T) {
	h := newHarness(t, newRouter(ownLine))
	for _, l := range rawFallbackSet() {
		h.r.failOn["dns-proxy "+l] = true
	}
	h.p.setOwn(errDown)
	h.tick(0)
	s := h.tick(time.Minute)

	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine})
	for _, c := range h.r.takeCalls() {
		if c == ownRemoval {
			t.Fatalf("own line removed although no fallback line took hold")
		}
	}
	if s.Mode != ModePrimary || !s.NoLiveFallback {
		t.Fatalf("snapshot = %+v, want primary + NoLiveFallback", s)
	}
	c := Check{Source: h.w}.Run(context.Background(), checks.Deps{})
	if c.Status != "fail" || c.Details["reason"] != DetailReasonNoLiveFallback {
		t.Fatalf("check = %s %#v, want fail no_live_fallback", c.Status, c.Details)
	}
	if p := h.state(); p.Mode == ModeFallback || len(p.RemovedPrimaryLines) != 0 {
		t.Fatalf("state file must not claim a switch: %+v", p)
	}
	if !strings.Contains(h.logs.String(), "level=ERROR") {
		t.Fatalf("rollback must be logged loudly, logs:\n%s", h.logs)
	}

	// A restart with the adds accepted again finds a primary router, own line in place.
	h.r.failOn = map[string]bool{}
	h.w = h.newWatcher()
	h.p.setOwn(nil)
	if s := h.tick(time.Minute); s.Mode != ModePrimary {
		t.Fatalf("after restart snapshot = %+v", s)
	}
	sameSet(t, "router lines after restart", h.r.snapshotLines(), []string{ownLine})
}

// TestWatch_ForwardForeignRejectedRollsBackTheRest: the RU and pinned lines
// went in but no foreign line did — nothing carries the bulk of the traffic,
// so what was added comes out again and the own line stays.
func TestWatch_ForwardForeignRejectedRollsBackTheRest(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	for _, l := range foreignOf(rawFallbackSet()) {
		h.r.failOn["dns-proxy "+l] = true
	}
	h.p.setOwn(errDown)
	h.tick(0)
	s := h.tick(time.Minute)

	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine, unrelated})
	for _, c := range h.r.takeCalls() {
		if c == ownRemoval {
			t.Fatalf("own line removed although no foreign line took hold")
		}
	}
	if s.Mode != ModePrimary || !s.NoLiveFallback || len(s.Leftover) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
}

// TestWatch_ForwardAddsFallbackBeforeRemovingOwn: mirror of the return — the
// fallback set goes in first, the own line comes out only afterwards.
func TestWatch_ForwardAddsFallbackBeforeRemovingOwn(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.p.setOwn(errDown)
	h.tick(0)
	h.r.takeCalls()
	if s := h.tick(time.Minute); s.Mode != ModeFallback {
		t.Fatalf("snapshot = %+v", s)
	}
	calls := mutating(h.r.takeCalls())
	removal := -1
	for i, c := range calls {
		if c == ownRemoval {
			removal = i
		}
	}
	if removal < 0 {
		t.Fatalf("own line never removed, calls %q", calls)
	}
	adds := 0
	for i, c := range calls {
		if isAdd(c) {
			adds++
			if i > removal {
				t.Fatalf("add %q issued after the own line was removed, calls %q", c, calls)
			}
		}
	}
	if adds != len(rawFallbackSet()) {
		t.Fatalf("adds = %d, want %d", adds, len(rawFallbackSet()))
	}
}

// TestWatch_ShutdownDoesNotStartASwitch: once the agent is stopping, no switch
// begins — neither when the tick starts late nor when the stop arrives while
// the candidates are being probed.
func TestWatch_ShutdownDoesNotStartASwitch(t *testing.T) {
	t.Run("cancelled before the tick", func(t *testing.T) {
		h := newHarness(t, newRouter(ownLine, unrelated))
		h.p.setOwn(errDown)
		h.tick(0)
		h.r.takeCalls()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := h.tickCtx(ctx, time.Minute)
		if m := mutating(h.r.takeCalls()); len(m) != 0 {
			t.Fatalf("no switch may start after shutdown, got %q", m)
		}
		if s.Mode != ModePrimary {
			t.Fatalf("snapshot = %+v", s)
		}
	})
	t.Run("cancelled while probing candidates", func(t *testing.T) {
		h := newHarness(t, newRouter(ownLine, unrelated))
		h.p.setOwn(errDown)
		h.tick(0)
		h.r.takeCalls()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h.p.onCandidate = cancel
		s := h.tickCtx(ctx, time.Minute)
		if m := mutating(h.r.takeCalls()); len(m) != 0 {
			t.Fatalf("no switch may start after shutdown, got %q", m)
		}
		if s.Mode != ModePrimary || s.NoLiveFallback {
			t.Fatalf("shutdown is not no_live_fallback, snapshot = %+v", s)
		}
		// Not shutting down after all: the next tick switches.
		h.p.onCandidate = nil
		if s := h.tick(time.Minute); s.Mode != ModeFallback {
			t.Fatalf("next tick must switch, snapshot = %+v", s)
		}
	})
}

// TestWatch_ShutdownMidSwitchCompletesTheSwitch: a stop arriving between
// adding the fallback and removing the own line (SIGTERM → context
// cancelled, every later ndmc killed) must not cut the switch in half.
func TestWatch_ShutdownMidSwitchCompletesTheSwitch(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.r.ctxAware = true
	h.p.setOwn(errDown)
	h.tick(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.r.onCall = func(cmd string) {
		if isAdd(cmd) {
			cancel()
		}
	}
	s := h.tickCtx(ctx, time.Minute)
	sameSet(t, "router lines", h.r.snapshotLines(), append([]string{unrelated}, fullFallbackSet()...))
	if s.Mode != ModeFallback || len(s.Leftover) != 0 || len(s.Missing) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
}

// TestWatch_RestartMidReturnFinishesTheReturn: the agent was killed after the
// return put the own line back but before the fallback lines came out. The
// restart must not take "own line present" for a reboot and leave foreign
// resolvers racing the own one (bypassing its filtering): it finishes the
// return, restoring a pre-existing line that shared an identifier.
func TestWatch_RestartMidReturnFinishesTheReturn(t *testing.T) {
	pre := "tls upstream common.dot.dns.yandex.net domain ru"
	h := newHarness(t, newRouter(ownLine, unrelated, pre))
	h.goFallback()
	h.r.setLines(append(h.r.snapshotLines(), ownLine)...) // own re-added, then killed
	h.w = h.newWatcher()
	h.p.setOwn(nil)
	s := h.tick(time.Minute)

	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine, unrelated, pre})
	if s.Mode != ModePrimary || len(s.Leftover) != 0 || len(s.Missing) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
	if p := h.state(); p.Mode != ModePrimary || len(p.AppliedLines) != 0 || len(p.RemovedPrimaryLines) != 0 {
		t.Fatalf("state = %+v", p)
	}
}

// TestWatch_RestartMidForwardUndoesTheHalfSwitch: killed after the fallback
// set went in but before the own line came out — own line and fallback lines
// side by side. The restart takes the half switch back out; the watchdog then
// decides afresh from its probes.
func TestWatch_RestartMidForwardUndoesTheHalfSwitch(t *testing.T) {
	set := rawFallbackSet()
	h := newHarness(t, newRouter(append([]string{ownLine, unrelated}, fullFallbackSet()...)...))
	h.writeState(persisted{Mode: ModeFallback, LastSwitch: t0, RemovedPrimaryLines: []string{ownLine},
		AppliedLines: set, Foreign: foreignOf(set), RU: DefaultRUCandidates[0]})
	h.p.setOwn(nil)
	s := h.tick(time.Minute)

	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine, unrelated})
	if s.Mode != ModePrimary || len(s.Leftover) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
	if p := h.state(); p.Mode != ModePrimary || len(p.AppliedLines) != 0 {
		t.Fatalf("state = %+v", p)
	}
}

// TestWatch_RestartInFallbackReaddsMissingFallbackLines: restarted in
// fallback (own line absent, state file holds it) with part of the fallback
// set missing — the set is completed, not trusted.
func TestWatch_RestartInFallbackReaddsMissingFallbackLines(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	var kept []string
	for _, l := range h.r.snapshotLines() {
		if !strings.Contains(l, "yandex") {
			kept = append(kept, l)
		}
	}
	h.r.setLines(kept...)
	h.w = h.newWatcher()
	s := h.tick(time.Minute)

	sameSet(t, "router lines", h.r.snapshotLines(), append([]string{unrelated}, fullFallbackSet()...))
	if s.Mode != ModeFallback || len(s.Missing) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
}

// TestWatch_NoLiveClearsWhenWatchdogGoesIdle: no_live_fallback, then the
// operator removes the own line by hand — the watchdog has nothing left to
// guard and must stop reporting the outage.
func TestWatch_NoLiveClearsWhenWatchdogGoesIdle(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated), DefaultForeignCandidates...)
	h.p.setOwn(errDown)
	h.tick(0)
	if s := h.tick(time.Minute); !s.NoLiveFallback {
		t.Fatalf("precondition: no_live_fallback, got %+v", s)
	}
	h.r.setLines(unrelated) // operator fixed DNS by hand
	var s Snapshot
	for i := 0; i < 3; i++ {
		s = h.tick(time.Minute)
	}
	if !s.Idle || s.NoLiveFallback {
		t.Fatalf("snapshot = %+v, want idle without no_live_fallback", s)
	}
	if c := (Check{Source: h.w}).Run(context.Background(), checks.Deps{}); c.Status != "ok" {
		t.Fatalf("check = %s %#v, want ok", c.Status, c.Details)
	}
}
