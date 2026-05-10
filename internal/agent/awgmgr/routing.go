package awgmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// ListDNSRoutes returns /api/dns-routes/list .data.
func (c *Client) ListDNSRoutes(ctx context.Context) ([]DNSRoute, error) {
	var env Envelope[[]DNSRoute]
	if err := c.get(ctx, "/api/dns-routes/list", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr dns-routes/list: success=false")
	}
	return env.Data, nil
}

// UpdateDNSRoute calls POST /api/dns-routes/update?id=<id> with the full
// rule object as the body. awg-manager treats the call as full-replace —
// the rule must be sent verbatim with only the desired fields modified.
func (c *Client) UpdateDNSRoute(ctx context.Context, rule DNSRoute) error {
	body, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/dns-routes/update?id="+rule.ID, body, nil)
}

// ListStaticRoutes returns /api/static-routes/list .data.
func (c *Client) ListStaticRoutes(ctx context.Context) ([]StaticRoute, error) {
	var env Envelope[[]StaticRoute]
	if err := c.get(ctx, "/api/static-routes/list", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr static-routes/list: success=false")
	}
	return env.Data, nil
}

// UpdateStaticRoute calls POST /api/static-routes/update — the id is in the
// body, NOT in the URL (different from DNS update). awg-manager full-replaces.
func (c *Client) UpdateStaticRoute(ctx context.Context, rule StaticRoute) error {
	body, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/static-routes/update", body, nil)
}

// RoutingTunnels returns /api/routing/tunnels .data — the catalogue of all
// routable interfaces (managed/system/wan). Used by the rebind action to
// resolve the iface value used in route bindings.
func (c *Client) RoutingTunnels(ctx context.Context) ([]RoutingTunnel, error) {
	var env Envelope[[]RoutingTunnel]
	if err := c.get(ctx, "/api/routing/tunnels", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr routing/tunnels: success=false")
	}
	return env.Data, nil
}

// RoutingRefresh forces NDMS cache reset.
func (c *Client) RoutingRefresh(ctx context.Context) error {
	return c.post(ctx, "/api/routing/refresh", nil, nil)
}

// HydraRouteControl posts {"action":"<action>"} to /api/system/hydraroute-control.
// action ∈ {"start","stop","restart"}. Called after rebinding any rule with
// backend=="hydraroute" so the daemon reloads.
func (c *Client) HydraRouteControl(ctx context.Context, action string) error {
	body, err := json.Marshal(map[string]string{"action": action})
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/system/hydraroute-control", body, nil)
}

// postJSON is a helper that POSTs JSON with the right headers. The existing
// (lowercase) post helper accepts a body io.Reader but doesn't set
// Content-Type; awg-manager's update endpoints require it. Inline here to
// avoid disturbing the existing helper.
func (c *Client) postJSON(ctx context.Context, path string, body []byte, out any) error {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "POST", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return fmt.Errorf("awgmgr POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "POST", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(rb))
	}
	if out != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, out); err != nil {
			return fmt.Errorf("awgmgr %s: decode: %w", path, err)
		}
	}
	return nil
}
