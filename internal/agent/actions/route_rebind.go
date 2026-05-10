package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// RouteRebind moves all DNS / Static rules whose target is src's iface to
// dst's iface. WAN, system, and other-managed-tunnel rules are preserved.
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
	src, err := getTunnel(ctx, c, srcID)
	if err != nil {
		return "", fmt.Errorf("resolve src: %w", err)
	}
	dst, err := getTunnel(ctx, c, dstID)
	if err != nil {
		return "", fmt.Errorf("resolve dst: %w", err)
	}
	if src.InterfaceName == "" || dst.InterfaceName == "" {
		return "", fmt.Errorf("src/dst missing interfaceName: src=%+v dst=%+v", src, dst)
	}
	srcIface, dstIface := src.InterfaceName, dst.InterfaceName
	srcIsDefaultRoute := src.DefaultRoute

	hrTouched := false
	res.DNS, res.HRNeo, hrTouched = rebindDNS(ctx, c, srcIface, dstIface, srcIsDefaultRoute)
	res.Static = rebindStatic(ctx, c, srcIface, dstIface)

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

// rebindDNS walks all DNS rules, swaps explicit routes from src to dst, and
// (when src is the default-route managed tunnel) converts hydraroute
// fall-through rules to explicit routes pointing at dst.
//
// Returns: total category result, the HRNeo sub-count (subset of total),
// and whether any hydraroute rule was actually written.
func rebindDNS(ctx context.Context, c *awgmgr.Client, srcIface, dstIface string, srcIsDefaultRoute bool) (total wire.CategoryResult, hrNeo wire.CategoryResult, hrTouched bool) {
	all, err := c.ListDNSRoutes(ctx)
	if err != nil {
		total.Failed = 1
		total.Errors = []string{"dns/list: " + err.Error()}
		return
	}
	for _, r := range all {
		isHR := r.Backend == "hydraroute"
		newRoutes, didChange := rewriteRoutes(r.Routes, srcIface, dstIface)
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

func rebindStatic(ctx context.Context, c *awgmgr.Client, srcIface, dstIface string) wire.CategoryResult {
	var res wire.CategoryResult
	all, err := c.ListStaticRoutes(ctx)
	if err != nil {
		res.Failed = 1
		res.Errors = []string{"static/list: " + err.Error()}
		return res
	}
	for _, r := range all {
		if r.TunnelID != srcIface {
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
