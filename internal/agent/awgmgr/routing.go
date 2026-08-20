package awgmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
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
//
// HR-Neo rule IDs routinely contain spaces and colons (e.g. "hr:CIDR: iplist:
// Telegram.org") — must be percent-encoded in the query, otherwise the HTTP
// parser rejects the request with HTTP 400 before awg-manager sees it.
func (c *Client) UpdateDNSRoute(ctx context.Context, rule DNSRoute) error {
	body, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/dns-routes/update?id="+url.QueryEscape(rule.ID), body, nil)
}

// CreateDNSRoute calls POST /api/dns-routes/create with the rule object as
// the body.
func (c *Client) CreateDNSRoute(ctx context.Context, rule DNSRoute) error {
	body, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/dns-routes/create", body, nil)
}

// DeleteDNSRoute calls POST /api/dns-routes/delete?id=<id>. DNS route IDs can
// contain spaces and colons, so the query value must be percent-encoded.
func (c *Client) DeleteDNSRoute(ctx context.Context, id string) error {
	return c.postJSON(ctx, "/api/dns-routes/delete?id="+url.QueryEscape(id), nil, nil)
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

// CreateStaticRoute calls POST /api/static-routes/create with the rule object
// as the body.
func (c *Client) CreateStaticRoute(ctx context.Context, rule StaticRoute) error {
	body, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/static-routes/create", body, nil)
}

// DeleteStaticRoute calls POST /api/static-routes/delete?id=<id>. awg-manager
// v2.10.8 documents the id in query; older builds accepted it in the body.
func (c *Client) DeleteStaticRoute(ctx context.Context, id string) error {
	queryErr := c.postJSON(ctx, "/api/static-routes/delete?id="+url.QueryEscape(id), nil, nil)
	if queryErr == nil {
		return nil
	}
	if !isLegacyStaticDeleteFallbackError(queryErr) {
		return queryErr
	}
	body, err := json.Marshal(StaticRoute{ID: id})
	if err != nil {
		return err
	}
	bodyErr := c.postJSON(ctx, "/api/static-routes/delete", body, nil)
	if bodyErr == nil {
		return nil
	}
	return fmt.Errorf("awgmgr static-routes/delete failed with query id: %v; legacy body id: %w", queryErr, bodyErr)
}

func isLegacyStaticDeleteFallbackError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 400") || strings.Contains(msg, "HTTP 404")
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

// Presets returns the unified AWG Manager presets catalogue from /api/presets.
func (c *Client) Presets(ctx context.Context) ([]Preset, error) {
	var raw json.RawMessage
	if err := c.get(ctx, "/api/presets", &raw); err != nil {
		return nil, err
	}
	var marker struct {
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal(raw, &marker); err == nil && marker.Success != nil {
		if !*marker.Success {
			return nil, fmt.Errorf("awgmgr presets: success=false")
		}
		var env Envelope[PresetsListResponse]
		if err := json.Unmarshal(raw, &env); err == nil && len(env.Data.Presets) > 0 {
			return env.Data.Presets, nil
		}
		// Флот разноверсионный, и конверт между сборками менялся: у одних
		// data -- объект с presets, у других сразу массив. Понимать надо обе
		// формы: непонятая означает пустой каталог на экране.
		var arr Envelope[[]Preset]
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("awgmgr presets: decode envelope: %w", err)
		}
		return arr.Data, nil
	}
	var direct PresetsListResponse
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, fmt.Errorf("awgmgr presets: decode: %w", err)
	}
	return direct.Presets, nil
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
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(body), "application/json")
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "POST", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return fmt.Errorf("awgmgr POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "POST", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("awgmgr read %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(rb))
	}
	if err := rejectFailureEnvelope(path, rb); err != nil {
		return err
	}
	if out != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, out); err != nil {
			return fmt.Errorf("awgmgr %s: decode: %w", path, err)
		}
	}
	return nil
}

// AccessPolicies returns /api/routing/access-policies .data.
//
// Read and write live in DIFFERENT namespaces on awg-manager: the routing
// snapshot is served from /api/routing/access-policies (this is what the
// router's own UI reads), while mutations go to /api/access-policies/*
// (see PermitPolicyInterface). Asymmetric, but it is the router's contract —
// do not "fix" one path to match the other.
//
// One deliberate exception: AccessPoliciesFresh below READS from the mutation
// namespace, because ?refresh=true (the only way past the NDMS cache) exists
// only there. That is not the mistake this comment forbids — see its own
// doc for why it is needed and where it is allowed to be used.
func (c *Client) AccessPolicies(ctx context.Context) ([]AccessPolicy, error) {
	var env Envelope[[]AccessPolicy]
	if err := c.get(ctx, "/api/routing/access-policies", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr routing/access-policies: success=false")
	}
	return env.Data, nil
}

// AccessPoliciesFresh returns the same data as AccessPolicies with the NDMS
// cache bypassed (the router's own OpenAPI documents ?refresh=true on this
// endpoint).
//
// Note the path: this reads from /api/access-policies — the MUTATION
// namespace — and it is the one deliberate exception to the split AccessPolicies
// documents. ?refresh=true exists nowhere else, so a cache-bypassing read has
// no other address. The flip side is that a build serving the routing
// namespace need not serve this one; callers must treat IsEndpointMissing here
// as "no fresh read available", not as a failure (addTunnelToHydraRoutePolicies
// falls back to the cached list for its post-condition check).
//
// Use it ONLY where a stale answer would be misread as a fact. Verifying that
// a write took effect is that case: re-reading through the cache right after
// PermitPolicyInterface can still show the chain from before the write, and
// the caller would report a successful permit as a failure. Everything else —
// the routing snapshot above all — stays on AccessPolicies: forcing an NDMS
// cache miss on every poll is load on the router with nothing bought for it.
func (c *Client) AccessPoliciesFresh(ctx context.Context) ([]AccessPolicy, error) {
	var env Envelope[[]AccessPolicy]
	if err := c.get(ctx, "/api/access-policies?refresh=true", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr access-policies?refresh=true: success=false")
	}
	return env.Data, nil
}

// PolicyInterfaces returns /api/routing/policy-interfaces .data — the
// interfaces policies may reference, with their live up/down state.
func (c *Client) PolicyInterfaces(ctx context.Context) ([]PolicyInterface, error) {
	var env Envelope[[]PolicyInterface]
	if err := c.get(ctx, "/api/routing/policy-interfaces", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr routing/policy-interfaces: success=false")
	}
	return env.Data, nil
}

// PermitPolicyInterface adds iface to the policy's chain at the given order
// (lower = higher priority). POST /api/access-policies/permit.
//
// iface must be a name the router itself offers in /api/routing/policy-interfaces:
// kernel iface names ("opkgtun11") are not accepted where NDMS names
// ("OpkgTun11") are expected.
func (c *Client) PermitPolicyInterface(ctx context.Context, policy, iface string, order int) error {
	body, err := json.Marshal(map[string]any{"name": policy, "interface": iface, "order": order})
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/access-policies/permit", body, nil)
}

// DenyPolicyInterface removes iface from the policy's chain.
// DELETE /api/access-policies/permit?name=<policy>&interface=<iface>.
func (c *Client) DenyPolicyInterface(ctx context.Context, policy, iface string) error {
	return c.delete(ctx, "/api/access-policies/permit?name="+url.QueryEscape(policy)+"&interface="+url.QueryEscape(iface))
}

// delete issues a DELETE and applies the same envelope checks as postJSON.
// awg-manager uses DELETE only for policy interface removal.
func (c *Client) delete(ctx context.Context, path string) error {
	start := time.Now()
	resp, err := c.do(ctx, http.MethodDelete, path, nil, "")
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "DELETE", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return fmt.Errorf("awgmgr DELETE %s: %w", path, err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "DELETE", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("awgmgr read %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(rb))
	}
	return rejectFailureEnvelope(path, rb)
}
