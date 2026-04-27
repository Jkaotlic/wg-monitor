// Package checks defines the per-tick health checks the agent runs.
// Each Check is pure with respect to its Deps — that lets us mock subprocess
// and HTTP calls in tests instead of shelling out.
package checks

import (
	"context"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks/wgreader"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type Check interface {
	Name() string
	Run(ctx context.Context, d Deps) wire.Check
}

// Deps is the set of injectable side effects every check may use.
// Concrete checks pick what they need; tests pass mocks.
type Deps struct {
	Runner     Runner          // subprocess executor (used by HTTP-bound stuff & legacy paths)
	HTTPClient *http.Client    // pre-configured with iface-bound dialer
	WGReader   wgreader.Reader // WG peer-state reader (used by AwgHandshake)
}

func OK(name string, start time.Time, details map[string]any) wire.Check {
	out := make(map[string]any, len(details))
	for k, v := range details {
		out[k] = v
	}
	return wire.Check{
		Name:       name,
		Status:     "ok",
		DurationMs: time.Since(start).Milliseconds(),
		Details:    out,
	}
}

func Fail(name string, start time.Time, errMsg string, details map[string]any) wire.Check {
	out := make(map[string]any, len(details)+1)
	for k, v := range details {
		out[k] = v
	}
	out["error"] = errMsg
	return wire.Check{
		Name:       name,
		Status:     "fail",
		DurationMs: time.Since(start).Milliseconds(),
		Details:    out,
	}
}
