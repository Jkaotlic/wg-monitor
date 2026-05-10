// Package checks defines the per-tick health checks the agent runs.
// Each Check is pure with respect to its Deps — that lets us mock subprocess
// and HTTP calls in tests instead of shelling out.
package checks

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type Check interface {
	Name() string
	Run(ctx context.Context, d Deps) wire.Check
}

// MultiCheck emits multiple wire.Check entries from a single Run call.
// Used for groups whose membership is dynamic (e.g. one Check per tunnel
// returned by awg-manager) — letting Reporter flatten them into the report.
type MultiCheck interface {
	Group() string
	Run(ctx context.Context, d Deps) []wire.Check
}

// Deps is the set of injectable side effects every check may use.
// Concrete checks pick what they need; tests pass mocks.
type Deps struct {
	Runner     Runner       // subprocess executor for shell-based checks (e.g. NDMC)
	HTTPClient *http.Client // pre-configured with iface-bound dialer (used by DoH probe)
}

func OK(name string, start time.Time, details map[string]any) wire.Check {
	out := make(map[string]any, len(details))
	for k, v := range details {
		out[k] = v
	}
	dur := time.Since(start)
	slog.Debug("check ok", "name", name, "duration_ms", dur.Milliseconds())
	return wire.Check{
		Name:       name,
		Status:     "ok",
		DurationMs: dur.Milliseconds(),
		Details:    out,
	}
}

func Fail(name string, start time.Time, errMsg string, details map[string]any) wire.Check {
	out := make(map[string]any, len(details)+1)
	for k, v := range details {
		out[k] = v
	}
	out["error"] = errMsg
	dur := time.Since(start)
	slog.Warn("check failed", "name", name, "err", errMsg, "duration_ms", dur.Milliseconds())
	return wire.Check{
		Name:       name,
		Status:     "fail",
		DurationMs: dur.Milliseconds(),
		Details:    out,
	}
}
