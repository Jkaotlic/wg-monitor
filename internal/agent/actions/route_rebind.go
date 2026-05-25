package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// RouteRebind moves all DNS / Static rules whose target is src's iface to
// dst's iface. WAN/system rules are preserved during normal tunnel-to-tunnel
// moves, and are moved only when srcID is wire.RouteOtherID.
//
// Fall-through DNS rules (routes==null) with backend="hydraroute" are
// converted to explicit routes pointing at dst when src is the default-route
// managed tunnel. This matches the awg-manager UI's own behaviour.
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
	srcIface, dstIface := src.Iface, dst.Iface
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
	res.DNS, res.HRNeo, hrTouched = rebindDNS(ctx, c, srcIface, dstIface, srcIsDefaultRoute, srcIsOther, defaultIface, knownIfaces)
	res.Static = rebindStatic(ctx, c, srcIface, dstIface, srcIsOther, knownIfaces)

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
		known[ep.Iface] = true
		if ep.DefaultRoute && defaultIface == "" {
			defaultIface = ep.Iface
		}
	}
	if routing, err := c.RoutingTunnels(ctx); err == nil {
		for _, t := range routing {
			ep := ndmsRouteEndpoint(t)
			if ep.Iface != "" {
				known[ep.Iface] = true
			}
		}
	}
	return known, defaultIface, nil
}

// rebindDNS walks all DNS rules, swaps explicit routes from src to dst, and
// (when src is the default-route managed tunnel) converts hydraroute
// fall-through rules to explicit routes pointing at dst.
//
// Returns: total category result, the HRNeo sub-count (subset of total),
// and whether any hydraroute rule was actually written.
func rebindDNS(ctx context.Context, c *awgmgr.Client, srcIface, dstIface string, srcIsDefaultRoute bool, srcIsOther bool, defaultIface string, knownIfaces map[string]bool) (total wire.CategoryResult, hrNeo wire.CategoryResult, hrTouched bool) {
	all, err := c.ListDNSRoutes(ctx)
	if err != nil {
		total.Failed = 1
		total.Errors = []string{"dns/list: " + err.Error()}
		return
	}
	for _, r := range all {
		isHR := r.Backend == "hydraroute"
		newRoutes, didChange := rewriteRoutes(r.Routes, srcIface, dstIface)
		if !didChange && srcIsOther {
			newRoutes, didChange = rewriteOtherRoutes(r.Routes, dstIface, knownIfaces)
		}
		if !didChange && srcIsOther && len(r.Routes) == 0 && !(isHR && r.HRPolicyName != "" && defaultIface != "") {
			newRoutes = []awgmgr.DNSRouteEntry{{Interface: dstIface, TunnelID: dstIface, Fallback: "auto"}}
			didChange = true
		}
		if !didChange {
			if r.Routes == nil && isHR && r.HRPolicyName != "" && srcIsDefaultRoute {
				newRoutes = []awgmgr.DNSRouteEntry{{Interface: dstIface, TunnelID: dstIface, Fallback: "auto"}}
				didChange = true
			}
		}
		if !didChange {
			continue
		}
		updated := r
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

// rewriteRoutes returns a copy of routes with every entry whose interface ==
// srcIface remapped to dstIface (both interface and tunnelId). The boolean
// indicates whether any entry was rewritten.
func rewriteRoutes(routes []awgmgr.DNSRouteEntry, srcIface, dstIface string) ([]awgmgr.DNSRouteEntry, bool) {
	if len(routes) == 0 {
		return routes, false
	}
	out := make([]awgmgr.DNSRouteEntry, len(routes))
	changed := false
	for i, e := range routes {
		out[i] = e
		if e.Interface == srcIface || e.TunnelID == srcIface {
			out[i].Interface = dstIface
			out[i].TunnelID = dstIface
			changed = true
		}
	}
	return out, changed
}

func rewriteOtherRoutes(routes []awgmgr.DNSRouteEntry, dstIface string, knownIfaces map[string]bool) ([]awgmgr.DNSRouteEntry, bool) {
	if len(routes) == 0 {
		return routes, false
	}
	out := make([]awgmgr.DNSRouteEntry, len(routes))
	changed := false
	for i, e := range routes {
		out[i] = e
		if routeBindIsOther(firstNonEmptyRoute(e.Interface, e.TunnelID), knownIfaces) {
			out[i].Interface = dstIface
			out[i].TunnelID = dstIface
			changed = true
		}
	}
	return out, changed
}

func routeBindIsOther(bind string, knownIfaces map[string]bool) bool {
	bind = strings.TrimSpace(bind)
	return bind == "" || !knownIfaces[bind]
}

func rebindStatic(ctx context.Context, c *awgmgr.Client, srcIface, dstIface string, srcIsOther bool, knownIfaces map[string]bool) wire.CategoryResult {
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
		} else if r.TunnelID != srcIface {
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
