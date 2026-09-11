package dnswatch

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Round 4 of the 4b review: the fold loses shared lines, own + foreign after
// a stuck return is an unsafe state that must be visible (and classified the
// same in-process and after a restart), and a primary that lost its own line
// by hand must not be taken for a fallback.

const cloudflareForeign = "https upstream https://cloudflare-dns.com/dns-query"

// stuckCloudflareReturn: a forward, then a return whose Cloudflare removal is
// stuck — the own line is back, and the foreign Cloudflare line plus its six
// pinned `domain` lines sit next to it.
func stuckCloudflareReturn(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.r.stuck[stuckCloudflareRemoval] = true
	h.goFallback()
	s := h.goPrimary()
	lines := h.r.snapshotLines()
	if s.Mode != ModePrimary || !hasOwn(lines) || !contains(lines, cloudflareForeign) {
		t.Fatalf("precondition: own line back, Cloudflare still there; snapshot %+v lines %q", s, lines)
	}
	return h
}

// TestWatch_SharedLineSurvivesFoldedForward: a pre-existing line shares the
// RU identifier. Return 1 leaves the RU lines (stuck removal); forward 2
// folds them in as its own. The pre-existing line must stay in the record as
// shared — return 2's removal by identifier takes it too, and only the record
// puts it back.
func TestWatch_SharedLineSurvivesFoldedForward(t *testing.T) {
	pre := "tls upstream common.dot.dns.yandex.net domain ru"
	h := newHarness(t, newRouter(ownLine, unrelated, pre))
	h.goFallback()
	if !contains(h.state().SharedLines, pre) {
		t.Fatalf("precondition: record 1 keeps the shared line, got %+v", h.state())
	}
	h.r.stuck[stuckRURemoval()] = true
	if s := h.goPrimary(); len(s.Leftover) == 0 || !contains(h.r.snapshotLines(), pre) {
		t.Fatalf("precondition: RU leftover after return 1, pre-existing line kept; snapshot %+v", s)
	}

	h.p.setOwn(errDown)
	for m := 1; m <= 10 && h.w.Snapshot().Mode != ModeFallback; m++ {
		h.tick(time.Minute)
	}
	if s := h.w.Snapshot(); s.Mode != ModeFallback {
		t.Fatalf("forward 2 never happened: %+v", s)
	}
	p := h.state()
	if !contains(p.SharedLines, pre) {
		t.Fatalf("record 2 lost the shared line: shared %q", p.SharedLines)
	}
	if contains(p.AppliedLines, pre) {
		t.Fatalf("the pre-existing line is not the watchdog's: applied %q", p.AppliedLines)
	}

	h.r.stuck = map[string]bool{}
	h.goPrimary()
	h.tick(time.Minute)
	sameSet(t, "router lines after return 2", h.r.snapshotLines(), []string{ownLine, unrelated, pre})
}

// TestWatch_LineSharingFoldedIdentifierSurvivesReturn: a line sharing the RU
// identifier appears while the cleanup is held (the own resolver is failing,
// nothing is re-read) — it is in neither the leftover nor the old record. No
// line forward 2 ADDS has that identifier; only the folded leftovers do. It
// must be recorded as shared all the same, or return 2 wipes it.
func TestWatch_LineSharingFoldedIdentifierSurvivesReturn(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()
	h.r.stuck[stuckRURemoval()] = true
	h.goPrimary()
	h.p.setOwn(errDown)
	h.tick(time.Minute) // cleanup held from here on
	operator := "tls upstream common.dot.dns.yandex.net domain example.org"
	h.r.setLines(append(h.r.snapshotLines(), operator)...)
	for m := 1; m <= 10 && h.w.Snapshot().Mode != ModeFallback; m++ {
		h.tick(time.Minute)
	}
	if s := h.w.Snapshot(); s.Mode != ModeFallback {
		t.Fatalf("forward 2 never happened: %+v", s)
	}
	p := h.state()
	if !contains(p.SharedLines, operator) || contains(p.AppliedLines, operator) {
		t.Fatalf("record 2: shared %q applied %q — the operator's line is shared, not the watchdog's", p.SharedLines, p.AppliedLines)
	}
	h.r.stuck = map[string]bool{}
	h.goPrimary()
	h.tick(time.Minute)
	sameSet(t, "router lines after return 2", h.r.snapshotLines(), []string{ownLine, unrelated, operator})
}

// TestWatch_SharedLinesCarriedAcrossFold: record 1 owes the router a
// pre-existing Cloudflare `domain` line. Cloudflare is dead at forward 2, so
// no line of the new switch shares its identifier — the new record still
// carries it over as shared (the router is owed it until a clean primary).
func TestWatch_SharedLinesCarriedAcrossFold(t *testing.T) {
	pre := DefaultPinnedCandidate + " domain example.org"
	h := newHarness(t, newRouter(ownLine, unrelated, pre))
	h.goFallback()
	if !contains(h.state().SharedLines, pre) {
		t.Fatalf("precondition: record 1 shares the Cloudflare line, got %+v", h.state())
	}
	h.r.stuck[stuckRURemoval()] = true
	h.goPrimary()
	h.p.dead[DefaultPinnedCandidate] = true
	h.p.setOwn(errDown)
	for m := 1; m <= 10 && h.w.Snapshot().Mode != ModeFallback; m++ {
		h.tick(time.Minute)
	}
	if s := h.w.Snapshot(); s.Mode != ModeFallback {
		t.Fatalf("forward 2 never happened: %+v", s)
	}
	if p := h.state(); !contains(p.SharedLines, pre) {
		t.Fatalf("record 2 dropped a line the router is owed: shared %q", p.SharedLines)
	}
	h.r.stuck = map[string]bool{}
	h.goPrimary()
	h.tick(time.Minute)
	sameSet(t, "router lines after return 2", h.r.snapshotLines(), []string{ownLine, unrelated, pre})
}

// TestWatch_ForeignLeftoverAfterReturnIsUnsafe: own line back, the foreign
// Cloudflare line still next to it — it races the own resolver and bypasses
// its filtering. The check says so (fail, reason foreign_leftover, masked
// lines, since), the log says so at ERROR every tick, the removal is retried
// every tick, and once it goes through the check is plain ok again.
func TestWatch_ForeignLeftoverAfterReturnIsUnsafe(t *testing.T) {
	h := stuckCloudflareReturn(t)
	returnedAt := t0.Add(7 * time.Minute).UTC().Format(time.RFC3339)
	for m := 0; m < 3; m++ {
		if m > 0 {
			h.logs.Reset()
			h.r.takeCalls()
			h.tick(time.Minute)
			logs := h.logs.String()
			if !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, DetailReasonForeignLeftover) {
				t.Fatalf("t+%dm: the unsafe state must be logged at ERROR every tick, logs:\n%s", m, logs)
			}
			retried := false
			for _, c := range h.r.takeCalls() {
				retried = retried || c == stuckCloudflareRemoval
			}
			if !retried {
				t.Fatalf("t+%dm: the foreign leftover removal must be retried every tick", m)
			}
		}
		c := runCheck(t, h.w)
		d := c.Details
		if c.Status != "fail" || d["reason"] != DetailReasonForeignLeftover || d["mode"] != "primary" {
			t.Fatalf("t+%dm: check = %s %#v, want fail foreign_leftover in primary", m, c.Status, d)
		}
		if d["since"] != returnedAt {
			t.Fatalf("t+%dm: since = %v, want the moment it was first seen %s", m, d["since"], returnedAt)
		}
		left, _ := d["leftover"].([]string)
		if !contains(left, cloudflareForeign) {
			t.Fatalf("t+%dm: leftover must name the foreign line, got %q", m, left)
		}
		if strings.Contains(fmt.Sprint(d), "secret-path") {
			t.Fatalf("details leak the endpoint path: %#v", d)
		}
	}
	if p := h.state(); p.LeftoverSince.IsZero() {
		t.Fatalf("the moment is kept in the state file (restarts must not reset it), got %+v", p)
	}

	h.r.stuck = map[string]bool{}
	h.tick(time.Minute)
	c := runCheck(t, h.w)
	if c.Status != "ok" || !reflect.DeepEqual(c.Details, map[string]any{"mode": "primary"}) {
		t.Fatalf("cleaned up: check = %s %#v, want ok {mode: primary}", c.Status, c.Details)
	}
	sameSet(t, "router lines", h.r.snapshotLines(), []string{ownLine, unrelated})
}

// TestWatch_ForeignLeftoverSameVerdictAfterRestart: the same state — own
// line plus foreign leftovers — is classified the same in-process and after
// an agent restart: primary adopted, the leftovers are cleanup, and the
// machine (hysteresis + cooldown) rules. ONE failed probe flips nothing and
// removes nothing: the foreign lines keep serving while the own resolver
// fails. The second failed probe switches.
func TestWatch_ForeignLeftoverSameVerdictAfterRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			h := stuckCloudflareReturn(t)
			h.clk.now = h.clk.now.Add(10 * time.Minute) // cooldown long over
			if restart {
				h.w = h.newWatcher()
			}
			h.p.setOwn(errDown)
			s := h.tick(time.Minute)
			lines := h.r.snapshotLines()
			if s.Mode != ModePrimary || !hasOwn(lines) {
				t.Fatalf("one failed probe flipped the router: %+v lines %q", s, lines)
			}
			if !hasForeignLine(lines) {
				t.Fatalf("own resolver failing — the foreign lines that answer must not be removed: %q", lines)
			}
			if c := runCheck(t, h.w); c.Status != "fail" || c.Details["reason"] != DetailReasonForeignLeftover {
				t.Fatalf("check = %s %#v, want fail foreign_leftover", c.Status, c.Details)
			}
			if p := h.state(); p.Pending != "" || p.Mode != ModePrimary {
				t.Fatalf("own line proven — primary adopted, nothing pending: %+v", p)
			}

			s = h.tick(time.Minute)
			lines = h.r.snapshotLines()
			if s.Mode != ModeFallback || hasOwn(lines) || !hasForeignLine(lines) {
				t.Fatalf("the second failed probe switches: %+v lines %q", s, lines)
			}
			if p := h.state(); p.Pending != "" {
				t.Fatalf("fallback proven, got pending %q", p.Pending)
			}

			h.r.stuck = map[string]bool{}
			h.goPrimary()
			h.tick(time.Minute)
			sameSet(t, "router lines after the return", h.r.snapshotLines(), []string{ownLine, unrelated})
			if c := runCheck(t, h.w); c.Status != "ok" {
				t.Fatalf("check = %s %#v", c.Status, c.Details)
			}
		})
	}
}

// TestWatch_ForeignLeftoverKeepsServingWhileOwnFails: the stuck removal
// clears just as the own resolver dies, inside the return's cooldown. Removing
// the foreign lines now would leave the router on a dead resolver alone until
// the cooldown ends. They stay until the fallback takes over.
func TestWatch_ForeignLeftoverKeepsServingWhileOwnFails(t *testing.T) {
	h := stuckCloudflareReturn(t)
	h.r.stuck = map[string]bool{}
	h.p.setOwn(errDown)
	reached := -1
	for m := 1; m <= 10; m++ {
		s := h.tick(time.Minute)
		lines := h.r.snapshotLines()
		if !hasForeignLine(lines) {
			t.Fatalf("t+%dm: own resolver dead and the foreign lines are gone: %+v lines %q", m, s, lines)
		}
		if reached < 0 && s.Mode == ModeFallback && !hasOwn(lines) {
			reached = m
		}
	}
	if reached < 0 {
		t.Fatalf("never reached the fallback: %+v", h.w.Snapshot())
	}
	sameSet(t, "router lines in fallback", h.r.snapshotLines(), append([]string{unrelated}, fullFallbackSet()...))
}

// TestWatch_OwnLineRemovedByHandDuringCleanupGoesIdle: primary with a stuck
// foreign leftover; the operator removes the own line by hand while the own
// resolver answers. That is not a fallback: no fail reason=fallback, the own
// line is not put back, nothing is touched — idle, saying why. Same after a
// restart. When the operator puts the own line back, guarding and the cleanup
// resume.
func TestWatch_OwnLineRemovedByHandDuringCleanupGoesIdle(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			h := stuckCloudflareReturn(t)
			var keep []string
			for _, l := range h.r.snapshotLines() {
				if l != ownLine {
					keep = append(keep, l)
				}
			}
			h.r.setLines(keep...)
			if restart {
				h.w = h.newWatcher()
			}
			h.r.takeCalls()
			var s Snapshot
			for m := 1; m <= 10; m++ {
				s = h.tick(time.Minute)
				c := runCheck(t, h.w)
				if s.Mode == ModeFallback || c.Status != "ok" {
					t.Fatalf("t+%dm: hand-removed own line taken for an outage: %+v check %s %#v", m, s, c.Status, c.Details)
				}
				if hasOwn(h.r.snapshotLines()) {
					t.Fatalf("t+%dm: the own line the operator removed was put back", m)
				}
			}
			if m := mutating(h.r.takeCalls()); len(m) != 0 {
				t.Fatalf("nothing may be touched, got %q", m)
			}
			c := runCheck(t, h.w)
			if !s.Idle || c.Details["idle"] != true || c.Details["idle_reason"] != idleOwnLineRemoved {
				t.Fatalf("snapshot %+v check %#v, want idle with reason %q", s, c.Details, idleOwnLineRemoved)
			}
			if p := h.state(); p.Pending != "" {
				t.Fatalf("nothing pending while idle, got %+v", p)
			}

			h.r.stuck = map[string]bool{}
			h.r.setLines(append(h.r.snapshotLines(), ownLine)...)
			h.tick(time.Minute)
			h.tick(time.Minute)
			sameSet(t, "router lines once the own line is back", h.r.snapshotLines(), []string{unrelated, ownLine})
			if c := runCheck(t, h.w); c.Status != "ok" || c.Details["idle"] != nil {
				t.Fatalf("guarding again: %s %#v", c.Status, c.Details)
			}
		})
	}
}

// TestWatch_CrashDuringFoldForward: kill the agent before every ndmc call
// after a return that left lines behind (RU: cleanup only; Cloudflare:
// foreign_leftover) — two cleanup ticks retrying the stuck removal while the
// own resolver answers, then it dies: the held cleanup and the forward that
// folds the leftovers in. Every case converges.
func TestWatch_CrashDuringFoldForward(t *testing.T) {
	total := 0
	for _, stuck := range []string{stuckRURemoval(), stuckCloudflareRemoval} {
		points := 0
		for k := 1; ; k++ {
			h := newHarness(t, newRouter(ownLine, unrelated))
			h.goFallback()
			h.r.stuck[stuck] = true
			h.goPrimary()
			cp := armCrash(h, k)
			for i := 0; i < 2 && !cp.hit; i++ {
				h.tick(time.Minute) // cleanup retries, own resolver alive
			}
			h.p.setOwn(errDown)
			for i := 0; i < 10 && !cp.hit; i++ {
				h.tick(time.Minute)
			}
			if !cp.hit {
				break
			}
			points++
			for _, alive := range []bool{true, false} {
				restartAndCheck(t, fmt.Sprintf("fold (%s) k=%d before %q", stuck, k, cp.cmd), cp, h.clk.now, alive)
			}
		}
		if points < 10 {
			t.Fatalf("too few crash points for %q: %d", stuck, points)
		}
		total += points
	}
	t.Logf("fold crash points: %d", total)
}
