package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// RouteStatus fetches the routing snapshot and returns it as a JSON-encoded
// string suitable for wire.CommandResult.Output.
func RouteStatus(ctx context.Context, c *awgmgr.Client) (string, error) {
	var (
		hr         *awgmgr.HydraRouteStatus
		tunnels    *awgmgr.TunnelsAll
		routing    []awgmgr.RoutingTunnel
		routingErr error
		dns        []awgmgr.DNSRoute
		statics    []awgmgr.StaticRoute
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { hr, err = c.HydraRouteStatus(gctx); return })
	g.Go(func() (err error) { tunnels, err = c.TunnelsAll(gctx); return })
	g.Go(func() error {
		routing, routingErr = c.RoutingTunnels(gctx)
		return nil
	})
	g.Go(func() (err error) { dns, err = c.ListDNSRoutes(gctx); return })
	g.Go(func() (err error) { statics, err = c.ListStaticRoutes(gctx); return })
	if err := g.Wait(); err != nil {
		return "", err
	}
	snap := buildRouteSnapshot(hr, tunnels, routing, dns, statics)
	if routingErr != nil {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("/api/routing/tunnels failed: %v", routingErr))
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildRouteSnapshot is the pure aggregation function: easy to test.
func buildRouteSnapshot(hr *awgmgr.HydraRouteStatus, tunnels *awgmgr.TunnelsAll, routing []awgmgr.RoutingTunnel, dns []awgmgr.DNSRoute, statics []awgmgr.StaticRoute) wire.RouteSnapshot {
	snap := wire.RouteSnapshot{Counts: make(map[string]wire.TunnelCounts)}
	if hr != nil {
		snap.HRNeo = wire.HRStatus{Installed: hr.Installed, Running: hr.Running}
	}
	byIface := make(map[string]string)
	defaultIface := ""
	if tunnels != nil {
		for _, t := range tunnels.Tunnels {
			ep := managedRouteEndpoint(t)
			if ep.Iface == "" {
				continue
			}
			snap.Tunnels = append(snap.Tunnels, wire.TunnelMeta{
				ID: ep.ID, Name: ep.Name, Iface: ep.Iface, Type: ep.Type,
				NDMSName: t.NDMSName, Enabled: ep.Enabled, Available: ep.Available,
				Status: t.Status, DefaultRoute: ep.DefaultRoute,
				HasHandshake: t.LastHandshake.Time() != nil,
				HandshakeAge: handshakeAgeSeconds(t.LastHandshake.Time()),
				PingStatus:   t.PingCheck.Status, PingFails: t.PingCheck.FailCount, PingFailMax: t.PingCheck.FailThreshold,
			})
			for _, alias := range ep.Aliases {
				byIface[alias] = ep.ID
			}
			if ep.DefaultRoute && ep.Enabled && defaultIface == "" {
				defaultIface = ep.Iface
			}
		}
	}
	for _, t := range routing {
		ep := ndmsRouteEndpoint(t)
		if ep.Iface == "" {
			continue
		}
		if routeAnyAliasMapped(ep, byIface) {
			continue
		}
		snap.Tunnels = append(snap.Tunnels, wire.TunnelMeta{
			ID: ep.ID, Name: ep.Name, Iface: ep.Iface, Type: ep.Type,
			Enabled: ep.Enabled, Available: ep.Available,
		})
		for _, alias := range ep.Aliases {
			byIface[alias] = ep.ID
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
		if r.Enabled {
			if len(r.Routes) > 0 {
				iface := firstNonEmptyRoute(r.Routes[0].Interface, r.Routes[0].TunnelID)
				ruleBind = iface
				if id, ok := byIface[iface]; ok {
					creditDNS(id, isHR)
				} else {
					creditOther(isHR, false)
				}
			} else {
				if isMovableHRNeoFallthrough(r) && (defaultIface != "" || len(r.HRPolicyInterfaces) > 0) {
					ruleBind = routePolicyBind(r, defaultIface, byIface)
					if id, ok := byIface[ruleBind]; ok {
						creditDNS(id, true)
					} else {
						creditOther(isHR, false)
					}
				} else {
					creditOther(isHR, false)
				}
			}
		} else if len(r.Routes) > 0 {
			ruleBind = firstNonEmptyRoute(r.Routes[0].Interface, r.Routes[0].TunnelID)
		}
		targets := append([]string{}, r.Domains...)
		targets = append(targets, r.ManualDomains...)
		snap.Rules = append(snap.Rules, normalizeRouteRuleSummary(wire.RouteRuleSummary{
			ID: r.ID, Name: r.Name, Kind: "dns", Backend: r.Backend, Enabled: r.Enabled, Bind: ruleBind, Targets: rawTargets(targets),
		}))
	}
	for _, r := range statics {
		if r.Enabled {
			if id, ok := byIface[r.TunnelID]; ok {
				c := snap.Counts[id]
				c.Static++
				snap.Counts[id] = c
			} else {
				snap.Other.Static++
			}
		}
		snap.Rules = append(snap.Rules, normalizeRouteRuleSummary(wire.RouteRuleSummary{
			ID: r.ID, Name: r.Name, Kind: "static", Enabled: r.Enabled, Bind: r.TunnelID, Targets: rawTargets(r.Subnets),
		}))
	}
	return snap
}

func routePolicyBind(r awgmgr.DNSRoute, fallbackIface string, byIface map[string]string) string {
	for _, bind := range r.HRPolicyInterfaces {
		bind = firstNonEmptyRoute(bind)
		if bind == "" {
			continue
		}
		if _, ok := byIface[bind]; ok {
			return bind
		}
	}
	return fallbackIface
}

func handshakeAgeSeconds(t *time.Time) int {
	if t == nil {
		return 0
	}
	return int(time.Since(*t).Seconds())
}
