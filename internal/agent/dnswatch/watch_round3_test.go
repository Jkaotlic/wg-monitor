package dnswatch

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/actions"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
)

// ruDomainLine is one Yandex `domain` line of the full set (add form).
func ruDomainLine() string {
	for _, l := range rawFallbackSet() {
		if strings.Contains(l, " domain ") && !strings.Contains(l, "cloudflare") {
			return l
		}
	}
	return ""
}

func stuckRURemoval() string {
	cmd, _ := actions.DNSProxyRemovalCommand(ruDomainLine())
	return cmd
}

const stuckCloudflareRemoval = "dns-proxy no https upstream https://cloudflare-dns.com/dns-query"

// TestWatch_StuckReturnCleanupDoesNotStarveForward: a return whose RU
// removal is stuck leaves leftover lines. That is cleanup, not an unproven
// mode: once the own resolver dies, the normal machine still switches to the
// fallback (hysteresis + cooldown), with fresh candidate probes.
func TestWatch_StuckReturnCleanupDoesNotStarveForward(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	h.r.stuck[stuckRURemoval()] = true
	s := h.goPrimary()
	if s.Mode != ModePrimary || len(s.Leftover) != len(DefaultRUZones) {
		t.Fatalf("after return: %+v", s)
	}
	if p := h.state(); p.Pending != "" || len(p.Leftover) != len(DefaultRUZones) {
		t.Fatalf("own line is proven on the router — a leftover is cleanup, not pending: %+v", p)
	}

	h.p.setOwn(errDown)
	before := h.p.probeCount()
	reached := -1
	for m := 1; m <= 15; m++ {
		s = h.tick(time.Minute)
		lines := h.r.snapshotLines()
		if reached < 0 && s.Mode == ModeFallback && !hasOwn(lines) && hasForeignLine(lines) {
			reached = m
		}
	}
	if reached < 0 {
		t.Fatalf("own resolver dead for 15 min, the router never went to the fallback; snapshot %+v lines %q", s, h.r.snapshotLines())
	}
	if h.p.probeCount() == before {
		t.Fatal("the forward must probe candidates afresh")
	}
	if c := (Check{Source: h.w}).Run(context.Background(), checks.Deps{}); c.Status != "fail" || c.Details["reason"] != DetailReasonFallback {
		t.Fatalf("check = %s %#v", c.Status, c.Details)
	}
	t.Logf("fallback reached at t+%dm", reached)
}

// TestWatch_PartialRollbackCleanupDoesNotStarveRetry: a rollback leaves RU
// lines behind (stuck removal). The own line is proven, so primary is proven:
// the retry of the forward runs once the cooldown allows and the router
// accepts adds again — the leftover lines join the new switch as its own.
func TestWatch_PartialRollbackCleanupDoesNotStarveRetry(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	for _, l := range foreignOf(rawFallbackSet()) {
		h.r.failOn["dns-proxy "+l] = true
	}
	h.r.stuck[stuckRURemoval()] = true
	h.p.setOwn(errDown)
	h.tick(0)
	s := h.tick(time.Minute)
	if s.Mode != ModePrimary || !s.NoLiveFallback || len(s.Leftover) == 0 {
		t.Fatalf("after rollback: %+v", s)
	}
	if p := h.state(); p.Pending != "" {
		t.Fatalf("own line never left the router — primary is proven, got pending %q", p.Pending)
	}
	h.r.mu.Lock()
	h.r.failOn = map[string]bool{}
	h.r.mu.Unlock()

	reached := -1
	for m := 1; m <= 15; m++ {
		s = h.tick(time.Minute)
		lines := h.r.snapshotLines()
		if reached < 0 && s.Mode == ModeFallback && !hasOwn(lines) && hasForeignLine(lines) {
			reached = m
		}
	}
	if reached < 0 {
		t.Fatalf("forward never retried in 15 min; snapshot %+v lines %q", s, h.r.snapshotLines())
	}
	// The RU lines left by the rollback belong to the fallback set now: the
	// return takes them out.
	h.r.stuck = map[string]bool{}
	if s := h.goPrimary(); s.Mode != ModePrimary {
		t.Fatalf("return: %+v", s)
	}
	h.tick(time.Minute)
	sameSet(t, "router lines after return", h.r.snapshotLines(), []string{ownLine, unrelated})
}

// TestWatch_SingleBlipDuringCleanupDoesNotFlip: after a return whose
// Cloudflare removal is stuck (foreign leftover), ONE failed probe of a live
// own resolver must not remove the own line — hysteresis still applies.
func TestWatch_SingleBlipDuringCleanupDoesNotFlip(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.r.stuck[stuckCloudflareRemoval] = true
	h.goFallback()
	s := h.goPrimary()
	if s.Mode != ModePrimary || len(s.Leftover) == 0 {
		t.Fatalf("after return: %+v", s)
	}
	h.tick(time.Minute)
	h.p.setOwn(errDown)
	s = h.tick(time.Minute)
	if s.Mode != ModePrimary || !hasOwn(h.r.snapshotLines()) {
		t.Fatalf("one failed probe flipped the router: %+v", s)
	}
	h.p.setOwn(nil)
	for m := 1; m <= 8; m++ {
		s = h.tick(time.Minute)
		if s.Mode != ModePrimary || !hasOwn(h.r.snapshotLines()) {
			t.Fatalf("t+%dm: own alive, mode must stay primary: %+v", m, s)
		}
	}
	// Round 4 ruling: a foreign leftover next to the own line is an unsafe
	// state — the check fails with foreign_leftover while the mode stays primary.
	c := Check{Source: h.w}.Run(context.Background(), checks.Deps{})
	if c.Status != "fail" || c.Details["reason"] != DetailReasonForeignLeftover || c.Details["mode"] != "primary" || c.Details["leftover"] == nil {
		t.Fatalf("the stuck foreign leftover is reported as foreign_leftover in primary: %s %#v", c.Status, c.Details)
	}
}

// TestWatch_FlapPatternSameForPartialAndFullSet: probes dead,dead,ok for an
// hour. With the full set the router switches once and stays (never two OKs
// in a row). A partial set (two foreign adds rejected) must behave the same —
// a missing line is cleanup, it must not bypass hysteresis and cooldown.
func TestWatch_FlapPatternSameForPartialAndFullSet(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%v", partial), func(t *testing.T) {
			h := newHarness(t, newRouter(ownLine, unrelated))
			if partial {
				for _, l := range foreignOf(rawFallbackSet())[1:] {
					h.r.failOn["dns-proxy "+l] = true
				}
			}
			pattern := []bool{false, false, true}
			prev, changes := ModePrimary, 0
			var at []string
			for m := 0; m < 60; m++ {
				if pattern[m%3] {
					h.p.setOwn(nil)
				} else {
					h.p.setOwn(errDown)
				}
				d := time.Minute
				if m == 0 {
					d = 0
				}
				if s := h.tick(d); s.Mode != prev {
					changes++
					at = append(at, fmt.Sprintf("%dm->%s", m, s.Mode))
					prev = s.Mode
				}
			}
			if changes != 1 {
				t.Fatalf("mode changes in 60 min = %d %v, want 1", changes, at)
			}
		})
	}
}

// TestWatch_CleanupRetriesEachTickUntilClean: a stuck removal is retried in
// the normal path; once it goes through, the record is dropped — the mode
// never changed.
func TestWatch_CleanupRetriesEachTickUntilClean(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	h.r.stuck[stuckRURemoval()] = true
	if s := h.goPrimary(); len(s.Leftover) == 0 {
		t.Fatalf("precondition: leftover, got %+v", s)
	}
	h.r.takeCalls()
	h.tick(time.Minute)
	retried := 0
	for _, c := range h.r.takeCalls() {
		if c == stuckRURemoval() {
			retried++
		}
	}
	if retried == 0 {
		t.Fatal("the leftover removal must be retried in the next tick")
	}
	h.r.stuck = map[string]bool{}
	s := h.tick(time.Minute)
	if s.Mode != ModePrimary || len(s.Leftover) != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine, unrelated})
	if p := h.state(); len(p.AppliedLines) != 0 || len(p.Leftover) != 0 {
		t.Fatalf("record must be dropped once clean, got %+v", p)
	}
}

// TestWatch_GoneByHandIdleSaysWhy: the idle after dropping a stale record says
// so in the check details.
func TestWatch_GoneByHandIdleSaysWhy(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	h.r.setLines(unrelated)
	h.w = h.newWatcher()
	h.tick(time.Minute)
	c := Check{Source: h.w}.Run(context.Background(), checks.Deps{})
	if c.Status != "ok" || c.Details["idle"] != true || c.Details["idle_reason"] != "record_dropped_manual_cleanup" {
		t.Fatalf("check = %s %#v", c.Status, c.Details)
	}
}

// TestWatch_RestartInProvenModeKeepsHysteresis: when the router proves a mode
// by itself at start-up, the watchdog adopts it without acting and the normal
// machine — thresholds and cooldown — decides. A single probe must not flip it.
func TestWatch_RestartInProvenModeKeepsHysteresis(t *testing.T) {
	t.Run("proven fallback, own resolver back", func(t *testing.T) {
		h := newHarness(t, newRouter(ownLine, unrelated))
		h.goFallback()
		h.clk.now = h.clk.now.Add(10 * time.Minute) // cooldown long over
		h.w = h.newWatcher()
		h.p.setOwn(nil)
		if s := h.tick(time.Minute); s.Mode != ModeFallback || hasOwn(h.r.snapshotLines()) {
			t.Fatalf("one good probe at start must not return: %+v", s)
		}
		if s := h.tick(time.Minute); s.Mode != ModePrimary {
			t.Fatalf("the second good probe returns: %+v", s)
		}
	})
	t.Run("proven primary with watchdog leftovers, own resolver dead", func(t *testing.T) {
		h := newHarness(t, newRouter(ownLine, unrelated))
		h.goFallback()
		h.r.stuck[stuckRURemoval()] = true
		if s := h.goPrimary(); len(s.Leftover) == 0 {
			t.Fatalf("precondition: RU leftover, got %+v", s)
		}
		h.clk.now = h.clk.now.Add(10 * time.Minute)
		h.w = h.newWatcher()
		h.p.setOwn(errDown)
		if s := h.tick(time.Minute); s.Mode != ModePrimary || !hasOwn(h.r.snapshotLines()) {
			t.Fatalf("one failed probe at start must not switch: %+v", s)
		}
		if s := h.tick(time.Minute); s.Mode != ModeFallback {
			t.Fatalf("the second failed probe switches: %+v", s)
		}
	})
}

// crashPoint is the router and the state file as they were when the agent
// was killed before ndmc call number k.
type crashPoint struct {
	lines []string
	state []byte
	hit   bool
	cmd   string
}

func armCrash(h *harness, k int) *crashPoint {
	cp := &crashPoint{}
	n := 0
	h.r.onCall = func(cmd string) {
		n++
		if n == k && !cp.hit {
			cp.hit, cp.cmd = true, cmd
			cp.lines = append([]string(nil), h.r.lines...)
			cp.state, _ = os.ReadFile(h.statePath)
		}
	}
	return cp
}

func sameSetBool(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	m := map[string]int{}
	for _, g := range got {
		m[g]++
	}
	for _, w := range want {
		m[w]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

// restartAndCheck restarts the agent on a crash point and runs it for ten
// minutes: the router must never be left with neither the own line nor a
// foreign line, must end in the right mode with nothing pending and no
// cleanup, and must then stay still.
func restartAndCheck(t *testing.T, label string, cp *crashPoint, at time.Time, ownAlive bool) {
	t.Helper()
	h := newHarness(t, newRouter(cp.lines...))
	if cp.state != nil {
		if err := os.WriteFile(h.statePath, cp.state, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.clk.now = at
	if ownAlive {
		h.p.setOwn(nil)
	} else {
		h.p.setOwn(errDown)
	}
	var s Snapshot
	for i := 0; i < 10; i++ {
		s = h.tick(time.Minute)
		if lines := h.r.snapshotLines(); !hasOwn(lines) && !hasForeignLine(lines) {
			t.Errorf("%s alive=%v tick %d: router has neither own nor foreign: %q", label, ownAlive, i, lines)
		}
	}
	lines := h.r.snapshotLines()
	p, _ := h.w.load()
	if ownAlive {
		if !sameSetBool(lines, []string{ownLine, unrelated}) || s.Mode != ModePrimary || p.Pending != "" || len(s.Leftover)+len(s.Missing) != 0 {
			t.Errorf("%s alive: final mode=%s pending=%q lines=%d %q", label, s.Mode, p.Pending, len(lines), lines)
		}
	} else {
		if s.Mode != ModeFallback || p.Pending != "" || !sameSetBool(lines, append([]string{unrelated}, fullFallbackSet()...)) {
			t.Errorf("%s dead: final mode=%s pending=%q lines=%d own=%v", label, s.Mode, p.Pending, len(lines), hasOwn(lines))
		}
	}
	h.r.takeCalls()
	for i := 0; i < 3; i++ {
		h.tick(time.Minute)
	}
	if m := mutating(h.r.takeCalls()); len(m) != 0 {
		t.Errorf("%s alive=%v: still mutating after convergence: %q", label, ownAlive, m)
	}
}

// TestWatch_CrashAtEveryStep: kill the agent before every ndmc call of a
// forward and of a return, restart with the own resolver alive and dead —
// every case must converge to the right, stable state.
func TestWatch_CrashAtEveryStep(t *testing.T) {
	forward := 0
	for k := 1; ; k++ {
		h := newHarness(t, newRouter(ownLine, unrelated))
		h.p.setOwn(errDown)
		h.tick(0)
		cp := armCrash(h, k)
		h.tick(time.Minute)
		if !cp.hit {
			break
		}
		forward++
		for _, alive := range []bool{true, false} {
			restartAndCheck(t, fmt.Sprintf("forward k=%d before %q", k, cp.cmd), cp, h.clk.now, alive)
		}
	}
	ret := 0
	for k := 1; ; k++ {
		h := newHarness(t, newRouter(ownLine, unrelated))
		h.goFallback()
		h.p.setOwn(nil)
		h.tick(5 * time.Minute)
		cp := armCrash(h, k)
		h.tick(time.Minute)
		if !cp.hit {
			break
		}
		ret++
		for _, alive := range []bool{true, false} {
			restartAndCheck(t, fmt.Sprintf("return k=%d before %q", k, cp.cmd), cp, h.clk.now, alive)
		}
	}
	if forward < 10 || ret < 5 {
		t.Fatalf("too few crash points exercised: forward %d, return %d", forward, ret)
	}
	t.Logf("crash points: forward %d, return %d", forward, ret)
}
