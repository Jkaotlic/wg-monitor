// Package-internal helpers for the pingcheck_status / pingcheck_toggle
// agent actions. Status is a JSON passthrough so the backend owns the
// rendering shape; toggle is two-tiered (awg-mgr POST primary, ndmc CLI
// fallback) — see Section 3 of the design spec.
package actions

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

// PingCheckStatusJSON returns the awg-mgr /api/pingcheck/status body
// re-serialised as a JSON envelope. We re-marshal so the backend
// always sees a stable shape (envelope shape is owned by awg-mgr; we
// just pass it through).
func PingCheckStatusJSON(ctx context.Context, c *awgmgr.Client) (string, error) {
	st, err := c.PingCheckStatus(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return "", fmt.Errorf("encode pingcheck status: %w", err)
	}
	return string(b), nil
}
