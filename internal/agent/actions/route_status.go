package actions

import (
	"context"
	"encoding/json"

	"golang.org/x/sync/errgroup"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// RouteStatus fetches the routing snapshot and returns it as a JSON-encoded
// string suitable for wire.CommandResult.Output.
func RouteStatus(ctx context.Context, c *awgmgr.Client) (string, error) {
	var (
		hr      *awgmgr.HydraRouteStatus
		tunnels *awgmgr.TunnelsAll
		dns     []awgmgr.DNSRoute
		statics []awgmgr.StaticRoute
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { hr, err = c.HydraRouteStatus(gctx); return })
	g.Go(func() (err error) { tunnels, err = c.TunnelsAll(gctx); return })
	g.Go(func() (err error) { dns, err = c.ListDNSRoutes(gctx); return })
	g.Go(func() (err error) { statics, err = c.ListStaticRoutes(gctx); return })
	if err := g.Wait(); err != nil {
		return "", err
	}
	snap := buildRouteSnapshot(hr, tunnels, dns, statics)
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildRouteSnapshot is the pure aggregation function — easy to test.
func buildRouteSnapshot(hr *awgmgr.HydraRouteStatus, tunnels *awgmgr.TunnelsAll, dns []awgmgr.DNSRoute, statics []awgmgr.StaticRoute) wire.RouteSnapshot {
	snap := wire.RouteSnapshot{Counts: make(map[string]wire.TunnelCounts)}
	if hr != nil {
		snap.HRNeo = wire.HRStatus{Installed: hr.Installed, Running: hr.Running}
	}
	byIface := make(map[string]string) // iface → tunnel id
	defaultIface := ""
	if tunnels != nil {
		for _, t := range tunnels.Tunnels {
			snap.Tunnels = append(snap.Tunnels, wire.TunnelMeta{
				ID: t.ID, Name: t.Name, Iface: t.InterfaceName,
				Enabled: t.Enabled, DefaultRoute: t.DefaultRoute,
			})
			if t.InterfaceName != "" {
				byIface[t.InterfaceName] = t.ID
			}
			if t.DefaultRoute && defaultIface == "" {
				defaultIface = t.InterfaceName
			}
		}
	}
	creditDNS := func(tid string, isHRNeo bool) {
		c := snap.Counts[tid]
		c.DNS++
		if isHRNeo {
			c.HRNeo++
		}
		snap.Counts[tid] = c
	}
	creditOther := func(isHRNeo bool, isStatic bool) {
		if isStatic {
			snap.Other.Static++
		} else {
			snap.Other.DNS++
			if isHRNeo {
				snap.Other.HRNeo++
			}
		}
	}
	for _, r := range dns {
		isHR := r.Backend == "hydraroute"
		ruleBind := ""
		if len(r.Routes) > 0 {
			iface := r.Routes[0].Interface
			ruleBind = iface
			if id, ok := byIface[iface]; ok {
				creditDNS(id, isHR)
			} else {
				creditOther(isHR, false)
			}
		} else {
			// Fall-through rule.
			if isHR && r.HRPolicyName != "" && defaultIface != "" {
				ruleBind = defaultIface
				if id, ok := byIface[defaultIface]; ok {
					creditDNS(id, true)
				} else {
					creditOther(isHR, false)
				}
			} else {
				creditOther(isHR, false)
			}
		}
		targets := append([]string{}, r.Domains...)
		targets = append(targets, r.ManualDomains...)
		snap.Rules = append(snap.Rules, normalizeRouteRuleSummary(wire.RouteRuleSummary{
			ID: r.ID, Name: r.Name, Kind: "dns", Backend: r.Backend, Enabled: r.Enabled, Bind: ruleBind, Targets: rawTargets(targets),
		}))
	}
	for _, r := range statics {
		if id, ok := byIface[r.TunnelID]; ok {
			c := snap.Counts[id]
			c.Static++
			snap.Counts[id] = c
		} else {
			snap.Other.Static++
		}
		snap.Rules = append(snap.Rules, normalizeRouteRuleSummary(wire.RouteRuleSummary{
			ID: r.ID, Name: r.Name, Kind: "static", Enabled: r.Enabled, Bind: r.TunnelID, Targets: rawTargets(r.Subnets),
		}))
	}
	return snap
}
