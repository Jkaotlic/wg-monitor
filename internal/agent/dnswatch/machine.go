// Package dnswatch is the agent-side DNS watchdog. When the router's own DoH
// resolver stops answering, it switches the router's dns-proxy — in the LIVE
// config only, never saved — to a split fallback set built from candidates
// that pass a probe (BuildFallbackSet), and switches back when the own
// resolver recovers. A reboot therefore always returns the router to its
// saved, intended DNS setup.
//
// machine.go is the pure state machine (Decide); watch.go runs the loop,
// probes, applies and persists; check.go reports the watchdog's snapshot as
// the `resolver_guard` check.
package dnswatch

import "time"

// Mode is which resolver set the router's dns-proxy runs.
type Mode string

const (
	ModePrimary  Mode = "primary"  // the own resolver
	ModeFallback Mode = "fallback" // the split fallback set
)

// Decision kinds. Decide yields none / to_fallback / to_primary; the watch
// turns a to_fallback it cannot carry out into no_live_fallback.
const (
	KindNone           = "none"
	KindToFallback     = "to_fallback"
	KindToPrimary      = "to_primary"
	KindNoLiveFallback = "no_live_fallback"
)

// Decision reasons.
const (
	ReasonSteady         = "steady"          // the probe agrees with the current mode
	ReasonBelowThreshold = "below_threshold" // streak shorter than the threshold
	ReasonCooldown       = "cooldown"        // threshold reached, the last switch is too recent
	ReasonOwnDown        = "own_resolver_down"
	ReasonOwnBack        = "own_resolver_back"
)

// State is the machine's memory: the mode, the current streak of failed and
// successful probes (each resets the other), and when the mode last changed.
type State struct {
	Mode       Mode
	Fails, OKs int
	LastSwitch time.Time
}

// Decision is what Decide concluded for one probe.
type Decision struct {
	Kind   string
	Reason string
}

// Decide folds one own-resolver probe result into st and says whether to
// switch.
//
//   - primary + FailThreshold failures in a row + cooldown over → to_fallback;
//   - fallback + OKThreshold successes in a row + cooldown over → to_primary;
//   - a probe that agrees with the current mode is none: re-entering the
//     active mode never does anything (idempotence);
//   - a switch resets both counters and stamps LastSwitch = now.
//
// Time only gates the cooldown; a gap between probes (missed ticks, a clock
// jump) does not switch by itself. A LastSwitch in the future (the clock went
// back, e.g. a router booting before NTP) counts as cooldown over: an
// untrustworthy stamp must not lock the watchdog out.
func Decide(st State, probeOK bool, now time.Time, cfg Config) (State, Decision) {
	if st.Mode != ModeFallback {
		st.Mode = ModePrimary
	}
	st = observe(st, probeOK)
	if (st.Mode == ModePrimary) == probeOK {
		return st, Decision{Kind: KindNone, Reason: ReasonSteady}
	}

	threshold, streak := cfg.FailThreshold, st.Fails
	target, kind, reason := ModeFallback, KindToFallback, ReasonOwnDown
	if st.Mode == ModeFallback {
		threshold, streak = cfg.OKThreshold, st.OKs
		target, kind, reason = ModePrimary, KindToPrimary, ReasonOwnBack
	}
	if threshold < 1 {
		threshold = 1
	}
	if streak < threshold {
		return st, Decision{Kind: KindNone, Reason: ReasonBelowThreshold}
	}
	if !cooldownOver(st.LastSwitch, now, cfg.Cooldown) {
		return st, Decision{Kind: KindNone, Reason: ReasonCooldown}
	}
	return State{Mode: target, LastSwitch: now}, Decision{Kind: kind, Reason: reason}
}

// observe counts one probe result into the streak counters.
func observe(st State, probeOK bool) State {
	if probeOK {
		st.OKs++
		st.Fails = 0
	} else {
		st.Fails++
		st.OKs = 0
	}
	return st
}

func cooldownOver(lastSwitch, now time.Time, cooldown time.Duration) bool {
	if lastSwitch.IsZero() {
		return true
	}
	elapsed := now.Sub(lastSwitch)
	if elapsed < 0 {
		return true
	}
	return elapsed >= cooldown
}
