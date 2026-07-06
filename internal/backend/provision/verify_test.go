package provision

import (
	"context"
	"strings"
	"testing"
	"time"
)

// virtualClock is a fake now()/sleep() pair for deterministic, instant
// tests: sleep never actually blocks, it just advances the same clock that
// now() reads, so a loop that would take up to two minutes in production
// (120s budget / 3s poll, see verify.go) runs in microseconds here.
type virtualClock struct {
	t      time.Time
	sleeps []time.Duration
}

func (c *virtualClock) now() time.Time { return c.t }

func (c *virtualClock) sleep(d time.Duration) {
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
}

// --- (a) a report newer than `since` arrives on the 2nd poll -> ok ----------

func TestVerifyOnline_ReturnsOKWhenReportArrivesAfterSince(t *testing.T) {
	since := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// The engine only calls VerifyOnline once the relay run (which took some
	// real time) has already reported rc==0, so the clock at entry is
	// naturally a bit ahead of `since`.
	clock := &virtualClock{t: since.Add(2 * time.Second)}

	calls := 0
	lastSeen := func(nick string) (time.Time, bool) {
		calls++
		if nick != "router1" {
			t.Fatalf("lastSeen called with nick = %q, want %q", nick, "router1")
		}
		if calls == 1 {
			return since.Add(-10 * time.Second), true // stale: before `since`
		}
		return since.Add(1 * time.Second), true // fresh: after `since`
	}

	detail, ok := VerifyOnline(context.Background(), "router1", since, 120*time.Second, 3*time.Second,
		lastSeen, clock.now, clock.sleep)

	if !ok {
		t.Fatal("ok = false, want true once lastSeen reports a time after `since`")
	}
	if !strings.Contains(detail, "report") {
		t.Errorf("detail = %q, want it to contain %q", detail, "report")
	}
	// Exact value pinned to the spec's own example ("first report 4s ago"):
	// clock is at since+5s (start=since+2s, one 3s poll) when the fresh
	// report at since+1s is observed, so now-seen == 4s.
	if want := "first report 4s ago"; detail != want {
		t.Errorf("detail = %q, want %q", detail, want)
	}
	if calls != 2 {
		t.Errorf("lastSeen called %d times, want 2 (one stale poll, then the fresh one)", calls)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 3*time.Second {
		t.Errorf("sleeps = %v, want exactly one 3s sleep between the stale and fresh polls", clock.sleeps)
	}
}

// --- (b) lastSeen never reports anything after `since` -> budget expiry ----

func TestVerifyOnline_ReturnsNotOKAfterBudgetExpires(t *testing.T) {
	since := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := &virtualClock{t: since}

	calls := 0
	lastSeen := func(string) (time.Time, bool) {
		calls++
		return since.Add(-1 * time.Second), true // always stale, never after `since`
	}

	detail, ok := VerifyOnline(context.Background(), "router1", since, 10*time.Second, 3*time.Second,
		lastSeen, clock.now, clock.sleep)

	if ok {
		t.Fatal("ok = true, want false: lastSeen never reported a time after `since`")
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty on a failed verify", detail)
	}
	if calls == 0 {
		t.Error("lastSeen was never called")
	}
	if elapsed := clock.t.Sub(since); elapsed < 10*time.Second {
		t.Errorf("clock only advanced %s, want >= the 10s budget", elapsed)
	}
}

// A nick that has never reported at all (found==false) must not be treated
// as fresh even if the zero/garbage time value it comes with would
// otherwise look like it's after `since` — the bool is the actual "has this
// nick ever reported" signal, `seen` is meaningless when it's false.
func TestVerifyOnline_IgnoresUnfoundReportRegardlessOfTimeValue(t *testing.T) {
	since := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := &virtualClock{t: since}

	lastSeen := func(string) (time.Time, bool) {
		return since.Add(1 * time.Hour), false // looks fresh, but found is false
	}

	detail, ok := VerifyOnline(context.Background(), "router1", since, 6*time.Second, 3*time.Second,
		lastSeen, clock.now, clock.sleep)

	if ok {
		t.Fatal("ok = true, want false: found was false, so seen must be ignored")
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty on a failed verify", detail)
	}
}

// --- (c) ctx already canceled -> returns false promptly, no sleeping -------

func TestVerifyOnline_HonorsAlreadyCanceledContext(t *testing.T) {
	since := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := &virtualClock{t: since}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	lastSeen := func(string) (time.Time, bool) {
		calls++
		return since.Add(-1 * time.Second), true // stale, so the ctx check is what ends the loop
	}

	detail, ok := VerifyOnline(ctx, "router1", since, 120*time.Second, 3*time.Second,
		lastSeen, clock.now, clock.sleep)

	if ok {
		t.Fatal("ok = true, want false for an already-canceled context")
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty", detail)
	}
	if len(clock.sleeps) != 0 {
		t.Errorf("sleep called %d times, want 0 — an already-canceled ctx must return before ever sleeping", len(clock.sleeps))
	}
	if calls != 1 {
		t.Errorf("lastSeen called %d times, want exactly 1 — the canceled ctx must short-circuit the loop right after the first poll", calls)
	}
}
