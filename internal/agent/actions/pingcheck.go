// Package-internal helpers for the pingcheck_status / pingcheck_toggle
// agent actions. Status is a JSON passthrough so the backend owns the
// rendering shape; toggle is two-tiered (awg-mgr POST primary, ndmc CLI
// fallback) — see Section 3 of the design spec.
package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

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

// PingCheckToggle enables or disables the per-tunnel watchdog. Tries
// awg-mgr POST first (no body required — query params); on any error
// falls back to ndmc CLI. Returns aggregated err if both paths fail.
//
// tunnelID is the awg-mgr id ("awg10"); ndmsName is the Keenetic
// interface name ("Wireguard0"). Both are needed because the two
// paths address the tunnel differently.
//
// Trust boundary: ndmsName is interpolated into the ndmc command string
// unsanitised. The backend's callbacks/parse.go enforces the regex
// ^[A-Za-z0-9_-]{1,32}$ on this value before it reaches the wire (SEC-02);
// agent-level callers must preserve that validation. Same pattern as
// runner.go::tunnel_enable.
func PingCheckToggle(ctx context.Context, c *awgmgr.Client, exec ExecFunc, tunnelID, ndmsName string, enable bool) error {
	primaryErr := primaryPingCheckToggle(ctx, c, tunnelID, enable)
	if primaryErr == nil {
		return nil
	}
	cmd := "interface " + ndmsName + " ping-check"
	if !enable {
		cmd = "no " + cmd
	}
	out, ndmcErr := exec(ctx, "ndmc", "-c", cmd)
	if ndmcErr == nil {
		return nil
	}
	return fmt.Errorf("pingcheck_toggle: POST=%v; ndmc=%v (%s)", primaryErr, ndmcErr, string(out))
}

func primaryPingCheckToggle(ctx context.Context, c *awgmgr.Client, tunnelID string, enable bool) error {
	flag := "0"
	if enable {
		flag = "1"
	}
	q := url.Values{"id": {tunnelID}, "enable": {flag}}
	// Reuse the client's HTTP transport but bypass GetEnv (we don't decode
	// the body — only care about status code).
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/pingcheck/toggle?"+q.Encode(), bytes.NewReader(nil))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP_REFUSED: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP_%d: %s", resp.StatusCode, string(body))
	}
	return nil
}
