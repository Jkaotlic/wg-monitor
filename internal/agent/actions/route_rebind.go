package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// RouteRebind moves all DNS / Static rules whose target is src's iface to
// dst's iface. WAN/system rules are preserved during normal tunnel-to-tunnel
// moves, and are moved only when srcID is wire.RouteOtherID.
//
// Fall-through HR-Neo DNS policy rules (routes==null, backend="hydraroute")
// stay policy-backed and are rebound by hrPolicyInterfaces. Legacy rules
// without hrPolicyInterfaces are attached to dst when src is the default-route
// managed tunnel.
func RouteRebind(ctx context.Context, c *awgmgr.Client, srcID, dstID string) (string, error) {
	res := wire.RouteRebindResult{SrcTunnelID: srcID, DstTunnelID: dstID}
	if srcID == dstID {
		b, _ := json.Marshal(res)
		return string(b), nil
	}
	dst, err := resolveRouteEndpoint(ctx, c, dstID)
	if err != nil {
		return "", fmt.Errorf("resolve dst: %w", err)
	}
	srcIsOther := srcID == wire.RouteOtherID
	src := routeEndpoint{}
	if !srcIsOther {
		src, err = resolveRouteEndpoint(ctx, c, srcID)
		if err != nil {
			return "", fmt.Errorf("resolve src: %w", err)
		}
	}
	if (!srcIsOther && src.Iface == "") || dst.Iface == "" {
		return "", fmt.Errorf("src/dst missing iface: src=%+v dst=%+v", src, dst)
	}
	dstIface := dst.Iface
	dstDNSBindID := routeDNSBindID(dst)
	srcAliases := routeAliasSet(src.Aliases)
	srcIsDefaultRoute := src.DefaultRoute
	defaultIface := ""
	knownIfaces := map[string]bool{}
	if srcIsOther {
		knownIfaces, defaultIface, err = routeKnownIfaces(ctx, c)
		if err != nil {
			return "", fmt.Errorf("resolve WAN/system sources: %w", err)
		}
	}

	hrTouched := false
	res.DNS, res.HRNeo, hrTouched = rebindDNS(ctx, c, srcAliases, dstIface, dstDNSBindID, srcIsDefaultRoute, srcIsOther, defaultIface, knownIfaces)
	res.Static = rebindStatic(ctx, c, srcAliases, dstIface, srcIsOther, knownIfaces)

	if err := c.RoutingRefresh(ctx); err != nil {
		appendErr(&res.Static, "routing/refresh: "+err.Error())
	}
	if hrTouched {
		hr, err := c.HydraRouteStatus(ctx)
		if err == nil && hr.Installed {
			if err := c.HydraRouteControl(ctx, "restart"); err != nil {
				appendErr(&res.HRNeo, "hr/control restart: "+err.Error())
			}
		}
	}

	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func getTunnel(ctx context.Context, c *awgmgr.Client, id string) (*awgmgr.Tunnel, error) {
	var env awgmgr.Envelope[awgmgr.Tunnel]
	if err := c.GetEnv(ctx, "/api/tunnels/get?id="+url.QueryEscape(id), &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("tunnels/get id=%s: success=false", id)
	}
	return &env.Data, nil
}

func routeKnownIfaces(ctx context.Context, c *awgmgr.Client) (map[string]bool, string, error) {
	known := map[string]bool{}
	defaultIface := ""
	tunnels, err := c.TunnelsAll(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, t := range tunnels.Tunnels {
		ep := managedRouteEndpoint(t)
		if ep.Iface == "" {
			continue
		}
		for _, alias := range ep.Aliases {
			known[alias] = true
		}
		if ep.DefaultRoute && defaultIface == "" {
			defaultIface = ep.Iface
		}
	}
	if routing, err := c.RoutingTunnels(ctx); err == nil {
		for _, t := range routing {
			ep := ndmsRouteEndpoint(t)
			if ep.Iface != "" {
				for _, alias := range ep.Aliases {
					known[alias] = true
				}
			}
		}
	}
	return known, defaultIface, nil
}

// rebindDNS walks all DNS rules, swaps explicit routes from src to dst, and
// (when src is the default-route managed tunnel) attaches HR-Neo fall-through
// policy rules to dst via hrPolicyInterfaces.
//
// Returns: total category result, the HRNeo sub-count (subset of total),
// and whether any hydraroute rule was actually written.
func rebindDNS(ctx context.Context, c *awgmgr.Client, srcAliases map[string]bool, dstIface, dstDNSBindID string, srcIsDefaultRoute bool, srcIsOther bool, defaultIface string, knownIfaces map[string]bool) (total wire.CategoryResult, hrNeo wire.CategoryResult, hrTouched bool) {
	all, err := c.ListDNSRoutes(ctx)
	if err != nil {
		total.Failed = 1
		total.Errors = []string{"dns/list: " + err.Error()}
		return
	}
	for _, r := range all {
		isHR := isHydraRouteBackend(r)
		updated := r
		newRoutes, didChange := rewriteRoutes(r.Routes, srcAliases, dstIface, dstDNSBindID)
		if !didChange && srcIsOther {
			newRoutes, didChange = rewriteOtherRoutes(r.Routes, dstIface, dstDNSBindID, knownIfaces)
		}
		if !didChange && srcIsOther && len(r.Routes) == 0 && !(isMovableHRNeoFallthrough(r) && defaultIface != "") {
			newRoutes = []awgmgr.DNSRouteEntry{{Interface: dstIface, TunnelID: dstDNSBindID, Fallback: "auto"}}
			didChange = true
		}
		if !didChange && !srcIsOther {
			if newPolicyIfaces, changed := rebindHRNeoPolicyInterfaces(r, srcAliases, dstIface, srcIsDefaultRoute); changed {
				updated.HRPolicyInterfaces = newPolicyIfaces
				ensureHRNeoPolicyDefaults(&updated)
				didChange = true
			}
		}
		if !didChange {
			continue
		}
		updated.Routes = newRoutes
		if err := c.UpdateDNSRoute(ctx, updated); err != nil {
			total.Failed++
			total.Errors = append(total.Errors, fmt.Sprintf("dns/update id=%s: %v", r.ID, err))
			if isHR {
				hrNeo.Failed++
			}
			continue
		}
		total.OK++
		if isHR {
			hrNeo.OK++
			hrTouched = true
		}
	}
	return
}

func isMovableHRNeoFallthrough(r awgmgr.DNSRoute) bool {
	if r.Routes != nil || !isHydraRouteBackend(r) || strings.TrimSpace(r.HRPolicyName) == "" {
		return false
	}
	return !isDirectProviderHRNeoPolicy(r)
}

func isHydraRouteBackend(r awgmgr.DNSRoute) bool {
	return strings.EqualFold(strings.TrimSpace(r.Backend), "hydraroute")
}

func rebindHRNeoPolicyInterfaces(r awgmgr.DNSRoute, srcAliases map[string]bool, dstIface string, srcIsDefaultRoute bool) ([]string, bool) {
	if !isMovableHRNeoFallthrough(r) {
		return nil, false
	}
	if len(r.HRPolicyInterfaces) == 0 {
		if !srcIsDefaultRoute {
			return nil, false
		}
		return []string{dstIface}, true
	}
	out := make([]string, 0, len(r.HRPolicyInterfaces))
	seen := map[string]bool{}
	changed := false
	for _, bind := range r.HRPolicyInterfaces {
		bind = strings.TrimSpace(bind)
		if bind == "" {
			continue
		}
		if routeBindMatches(bind, srcAliases) {
			bind = dstIface
			changed = true
		}
		key := strings.ToLower(bind)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, bind)
	}
	if !changed {
		return nil, false
	}
	return out, true
}

func ensureHRNeoPolicyDefaults(r *awgmgr.DNSRoute) {
	if strings.TrimSpace(r.HRPolicyName) == "" {
		r.HRPolicyName = defaultHydraRoutePolicyName
	}
	if strings.TrimSpace(r.HRRouteMode) == "" {
		r.HRRouteMode = "policy"
	}
}

func isDirectProviderHRNeoPolicy(r awgmgr.DNSRoute) bool {
	mode := strings.ToLower(strings.TrimSpace(r.HRRouteMode))
	switch mode {
	case "direct", "provider", "wan", "isp":
		return true
	}
	policy := strings.ToLower(strings.TrimSpace(r.HRPolicyName))
	for _, marker := range []string{"direct", "provider", "wan", "isp", "провайдер", "напрям"} {
		if strings.Contains(policy, marker) {
			return true
		}
	}
	return false
}

// rewriteRoutes returns a copy of routes with every entry whose interface or
// tunnelId matches any source alias remapped to dstIface. The boolean indicates
// whether any entry was rewritten.
func rewriteRoutes(routes []awgmgr.DNSRouteEntry, srcAliases map[string]bool, dstIface, dstDNSBindID string) ([]awgmgr.DNSRouteEntry, bool) {
	if len(routes) == 0 {
		return routes, false
	}
	out := make([]awgmgr.DNSRouteEntry, len(routes))
	changed := false
	for i, e := range routes {
		out[i] = e
		if routeBindMatches(e.Interface, srcAliases) || routeBindMatches(e.TunnelID, srcAliases) {
			out[i].Interface = dstIface
			out[i].TunnelID = dstDNSBindID
			changed = true
		}
	}
	return out, changed
}

func rewriteOtherRoutes(routes []awgmgr.DNSRouteEntry, dstIface, dstDNSBindID string, knownIfaces map[string]bool) ([]awgmgr.DNSRouteEntry, bool) {
	if len(routes) == 0 {
		return routes, false
	}
	out := make([]awgmgr.DNSRouteEntry, len(routes))
	changed := false
	for i, e := range routes {
		out[i] = e
		if routeBindIsOther(firstNonEmptyRoute(e.Interface, e.TunnelID), knownIfaces) {
			out[i].Interface = dstIface
			out[i].TunnelID = dstDNSBindID
			changed = true
		}
	}
	return out, changed
}

func routeBindIsOther(bind string, knownIfaces map[string]bool) bool {
	bind = strings.TrimSpace(bind)
	return bind == "" || !knownIfaces[bind]
}

func rebindStatic(ctx context.Context, c *awgmgr.Client, srcAliases map[string]bool, dstIface string, srcIsOther bool, knownIfaces map[string]bool) wire.CategoryResult {
	var res wire.CategoryResult
	all, err := c.ListStaticRoutes(ctx)
	if err != nil {
		res.Failed = 1
		res.Errors = []string{"static/list: " + err.Error()}
		return res
	}
	for _, r := range all {
		if srcIsOther {
			if !routeBindIsOther(r.TunnelID, knownIfaces) {
				continue
			}
		} else if !routeBindMatches(r.TunnelID, srcAliases) {
			continue
		}
		updated := r
		updated.TunnelID = dstIface
		if err := c.UpdateStaticRoute(ctx, updated); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("static/update id=%s: %v", r.ID, err))
			continue
		}
		res.OK++
	}
	return res
}

func appendErr(c *wire.CategoryResult, s string) {
	c.Errors = append(c.Errors, s)
}
