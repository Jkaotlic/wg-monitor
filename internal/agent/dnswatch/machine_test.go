package dnswatch

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

func machineConfig() Config {
	return Config{FailThreshold: 2, OKThreshold: 2, Cooldown: 300 * time.Second}
}

type step struct {
	probeOK  bool
	at       time.Duration // offset from t0
	wantKind string
	wantMode Mode
}

func runSteps(t *testing.T, st State, steps []step) State {
	t.Helper()
	cfg := machineConfig()
	for i, s := range steps {
		var d Decision
		st, d = Decide(st, s.probeOK, t0.Add(s.at), cfg)
		if d.Kind != s.wantKind || st.Mode != s.wantMode {
			t.Fatalf("step %d (ok=%v at +%v): kind=%q mode=%q reason=%q, want kind=%q mode=%q",
				i, s.probeOK, s.at, d.Kind, st.Mode, d.Reason, s.wantKind, s.wantMode)
		}
	}
	return st
}

func TestDecide(t *testing.T) {
	past := t0.Add(-time.Hour) // cooldown long over
	cases := []struct {
		name  string
		start State
		steps []step
	}{
		{"two fails in a row → to_fallback", State{Mode: ModePrimary, LastSwitch: past}, []step{
			{false, 0, KindNone, ModePrimary},
			{false, time.Minute, KindToFallback, ModeFallback},
		}},
		{"one fail then ok resets the streak", State{Mode: ModePrimary, LastSwitch: past}, []step{
			{false, 0, KindNone, ModePrimary},
			{true, time.Minute, KindNone, ModePrimary},
			{false, 2 * time.Minute, KindNone, ModePrimary},
		}},
		{"cooldown not over in primary → none", State{Mode: ModePrimary, LastSwitch: t0}, []step{
			{false, time.Minute, KindNone, ModePrimary},
			{false, 2 * time.Minute, KindNone, ModePrimary},
			{false, 4 * time.Minute, KindNone, ModePrimary},
			{false, 5 * time.Minute, KindToFallback, ModeFallback},
		}},
		{"fails in fallback re-enter nothing", State{Mode: ModeFallback, LastSwitch: past}, []step{
			{false, 0, KindNone, ModeFallback},
			{false, time.Minute, KindNone, ModeFallback},
			{false, 2 * time.Minute, KindNone, ModeFallback},
		}},
		{"oks in primary re-enter nothing", State{Mode: ModePrimary, LastSwitch: past}, []step{
			{true, 0, KindNone, ModePrimary},
			{true, time.Minute, KindNone, ModePrimary},
			{true, 2 * time.Minute, KindNone, ModePrimary},
		}},
		{"two oks in fallback after cooldown → to_primary", State{Mode: ModeFallback, LastSwitch: past}, []step{
			{true, 0, KindNone, ModeFallback},
			{true, time.Minute, KindToPrimary, ModePrimary},
		}},
		{"one ok then fail in fallback resets the streak", State{Mode: ModeFallback, LastSwitch: past}, []step{
			{true, 0, KindNone, ModeFallback},
			{false, time.Minute, KindNone, ModeFallback},
			{true, 2 * time.Minute, KindNone, ModeFallback},
		}},
		{"missed ticks (clock jump) do not switch by themselves", State{Mode: ModePrimary, Fails: 1, LastSwitch: past}, []step{
			{true, 5 * time.Hour, KindNone, ModePrimary},
			{false, 10 * time.Hour, KindNone, ModePrimary},
		}},
		{"a single ok after a long gap does not return from fallback", State{Mode: ModeFallback, LastSwitch: past}, []step{
			{true, 7 * time.Hour, KindNone, ModeFallback},
		}},
		{"clock behind the last switch does not lock the watchdog", State{Mode: ModePrimary, LastSwitch: t0.Add(24 * time.Hour)}, []step{
			{false, 0, KindNone, ModePrimary},
			{false, time.Minute, KindToFallback, ModeFallback},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runSteps(t, tc.start, tc.steps) })
	}
}

// TestDecide_AntiFlapCooldown: the own resolver comes back right after the
// switch, then dies right after the return. Each switch starts a cooldown; no
// switch happens inside it, however many probes agree.
func TestDecide_AntiFlapCooldown(t *testing.T) {
	st := runSteps(t, State{Mode: ModePrimary}, []step{
		{false, 0, KindNone, ModePrimary},
		{false, time.Minute, KindToFallback, ModeFallback}, // switch at +1m
		{true, 2 * time.Minute, KindNone, ModeFallback},
		{true, 3 * time.Minute, KindNone, ModeFallback}, // 2 oks, but cooldown until +6m
		{true, 5 * time.Minute, KindNone, ModeFallback},
		{true, 6 * time.Minute, KindToPrimary, ModePrimary}, // back at +6m
		{false, 7 * time.Minute, KindNone, ModePrimary},
		{false, 8 * time.Minute, KindNone, ModePrimary}, // 2 fails, cooldown until +11m
		{false, 11 * time.Minute, KindToFallback, ModeFallback},
	})
	if !st.LastSwitch.Equal(t0.Add(11 * time.Minute)) {
		t.Errorf("LastSwitch = %v, want the last switch time", st.LastSwitch)
	}
}

// TestDecide_CooldownNamedAsReason: a switch held back by the cooldown says so
// — the watch logs every decision not to switch.
func TestDecide_CooldownNamedAsReason(t *testing.T) {
	cfg := machineConfig()
	st := State{Mode: ModePrimary, Fails: 1, LastSwitch: t0}
	_, d := Decide(st, false, t0.Add(time.Minute), cfg)
	if d.Kind != KindNone || d.Reason != ReasonCooldown {
		t.Fatalf("decision = %+v, want none/%s", d, ReasonCooldown)
	}
}

// TestDecide_SwitchResetsCounters: the streak that caused a switch does not
// count toward the next one.
func TestDecide_SwitchResetsCounters(t *testing.T) {
	st, d := Decide(State{Mode: ModePrimary, Fails: 1}, false, t0, machineConfig())
	if d.Kind != KindToFallback {
		t.Fatalf("kind = %q", d.Kind)
	}
	if st.Fails != 0 || st.OKs != 0 || !st.LastSwitch.Equal(t0) {
		t.Fatalf("state after switch = %+v", st)
	}
}
