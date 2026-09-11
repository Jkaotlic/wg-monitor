package dnswatch

import (
	"context"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// CheckName is the watchdog's check in the agent report. Not `dns_*`: the
// backend treats everything `dns_`-prefixed as noisy (raised thresholds, sleep
// warm-up), while the watchdog has already filtered flapping itself.
const CheckName = "resolver_guard"

// Reason values in the check's details — the contract with the backend's
// owner-facing texts (part 4c). Do not change them.
const (
	DetailReasonFallback       = "fallback"
	DetailReasonNoLiveFallback = "no_live_fallback"
	// DetailReasonForeignLeftover: the router is back on the own resolver, but
	// foreign lines of the fallback set are still next to it — they race the
	// own resolver and bypass its filtering (v0.30 ruling, part 4b round 4).
	DetailReasonForeignLeftover = "foreign_leftover"
)

// SnapshotSource is what the check reads — the running Watcher.
type SnapshotSource interface {
	Snapshot() Snapshot
}

// Check is the thin resolver_guard check: it only reads the watchdog's last
// snapshot. The watchdog runs in its own goroutine — its probes and ndmc calls
// don't fit the reporter's per-check timeout.
//
//	ok:   {mode:"primary"}
//	fail: {mode:"fallback", since (RFC3339 UTC), reason:"fallback", foreign:[...], ru:"<line or empty>", ru_degraded}
//	fail: {mode:"primary", reason:"no_live_fallback"}
//
// A partial switch adds leftover:[...] / missing:[...] in any mode; an idle
// watchdog (no own-resolver line on the router) adds idle:true, one that has
// not read the router yet adds ready:false — both ok, no reason.
type Check struct {
	Source SnapshotSource
}

func (Check) Name() string { return CheckName }

func (c Check) Run(_ context.Context, _ checks.Deps) wire.Check {
	start := time.Now()
	s := c.Source.Snapshot()
	d := map[string]any{"mode": string(ModePrimary)}
	if len(s.Leftover) > 0 {
		d["leftover"] = s.Leftover
	}
	if len(s.Missing) > 0 {
		d["missing"] = s.Missing
	}
	switch {
	case s.Mode == ModeFallback:
		foreign := s.Foreign
		if foreign == nil {
			foreign = []string{}
		}
		d["mode"] = string(ModeFallback)
		d["since"] = s.Since.UTC().Format(time.RFC3339)
		d["reason"] = DetailReasonFallback
		d["foreign"] = foreign
		d["ru"] = s.RU
		d["ru_degraded"] = s.RUDegraded
		return checks.Fail(CheckName, start, "own DNS resolver is down — the router runs on fallback DNS resolvers", d)
	case s.NoLiveFallback:
		d["reason"] = DetailReasonNoLiveFallback
		return checks.Fail(CheckName, start, "own DNS resolver is down and no fallback DNS resolver answers — nothing was switched", d)
	case !s.Idle && s.Mode == ModePrimary && len(s.ForeignLeftover) > 0:
		d["reason"] = DetailReasonForeignLeftover
		if !s.LeftoverSince.IsZero() {
			d["since"] = s.LeftoverSince.UTC().Format(time.RFC3339)
		}
		return checks.Fail(CheckName, start, "fallback DNS resolvers are still on the router next to the own one — part of the queries bypass it", d)
	}
	if !s.Ready {
		d["ready"] = false
	}
	if s.Idle {
		d["idle"] = true
		if s.IdleReason != "" {
			d["idle_reason"] = s.IdleReason
		}
	}
	return checks.OK(CheckName, start, d)
}
