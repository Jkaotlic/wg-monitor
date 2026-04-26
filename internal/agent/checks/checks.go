// Package checks defines the per-tick health checks the agent runs.
// Each Check is pure with respect to its Deps — that lets us mock subprocess
// and HTTP calls in tests instead of shelling out.
package checks

import (
	"context"
	"net/http"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type Check interface {
	Name() string
	Run(ctx context.Context, d Deps) wire.Check
}

// Deps is the set of injectable side effects every check may use.
// Concrete checks pick what they need; tests pass mocks.
type Deps struct {
	Runner     Runner       // subprocess executor (wg, dig)
	HTTPClient *http.Client // pre-configured with iface-bound dialer
}

func OK(name string, start time.Time, details map[string]any) wire.Check {
	if details == nil {
		details = map[string]any{}
	}
	return wire.Check{
		Name:       name,
		Status:     "ok",
		DurationMs: time.Since(start).Milliseconds(),
		Details:    details,
	}
}

func Fail(name string, start time.Time, errMsg string, details map[string]any) wire.Check {
	if details == nil {
		details = map[string]any{}
	}
	details["error"] = errMsg
	return wire.Check{
		Name:       name,
		Status:     "fail",
		DurationMs: time.Since(start).Milliseconds(),
		Details:    details,
	}
}
