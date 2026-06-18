package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
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
		settings   *awgmgr.Settings
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
	g.Go(func() error {
		// Non-fatal: older awg-manager builds serve the SPA shell at this
		// endpoint. On any failure settings stays nil and buildRouteSnapshot
		// falls back to the first defaultRoute=true tunnel.
		settings, _ = c.Settings(gctx)
		return nil
	})
	if err := g.Wait(); err != nil {
		return "", err
	}
	snap := buildRouteSnapshot(hr, tunnels, routing, dns, statics, activeDefaultTunnelID(settings))
	if routingErr != nil {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("/api/routing/tunnels failed: %v", routingErr))
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// activeDefaultTunnelID extracts the authoritative active default-route tunnel
// id from awg-manager settings. download.routeTag has the form
// "<routeKind>-<tunnelID>" (e.g. "awg-awg12"); strip the kind prefix to recover
// the tunnel id used in /api/tunnels/all. Returns "" when settings are
// unavailable so callers keep the legacy first-defaultRoute heuristic.
func activeDefaultTunnelID(s *awgmgr.Settings) string {
	if s == nil {
		return ""
	}
	tag := strings.TrimSpace(s.Download.RouteTag)
	if tag == "" {
		return ""
	}
	if kind := strings.TrimSpace(s.Download.RouteKind); kind != "" {
		tag = strings.TrimPrefix(tag, kind+"-")
	}
	return strings.TrimSpace(tag)
}

// buildRouteSnapshot is the pure aggregation function: easy to test.
//
// activeDefaultID is the authoritative active default-route tunnel id (from
// awg-manager settings.download.routeTag, "" when unknown). Several tunnels can
// each report defaultRoute=true in /api/tunnels/all; HR-Neo policy rules with
// no explicit hrPolicyInterfaces fall through to the live default egress, so we
// must credit/label them against the tunnel awg-manager actually routes
// through — not just the first defaultRoute=true tunnel encountered.
func buildRouteSnapshot(hr *awgmgr.HydraRouteStatus, tunnels *awgmgr.TunnelsAll, routing []awgmgr.RoutingTunnel, dns []awgmgr.DNSRoute, statics []awgmgr.StaticRoute, activeDefaultID string) wire.RouteSnapshot {
	snap := wire.RouteSnapshot{Counts: make(map[string]wire.TunnelCounts)}
	if hr != nil {
		snap.HRNeo = wire.HRStatus{Installed: hr.Installed, Running: hr.Running}
	}
	byIface := make(map[string]string)
	defaultIface := ""
	authoritativeDefaultIface := ""
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
			if activeDefaultID != "" && ep.Enabled && ep.ID == activeDefaultID {
				authoritativeDefaultIface = ep.Iface
			}
		}
	}
	// awg-manager's recorded default-route tunnel wins over the first-listed
	// defaultRoute=true heuristic: when multiple tunnels carry defaultRoute=true
	// only one is the live egress, and routeTag names it.
	if authoritativeDefaultIface != "" {
		defaultIface = authoritativeDefaultIface
	}
	for _, t := range routing {
		ep := ndmsRouteEndpoint(t)
		if ep.Iface == "" {
			continue
		}
		if id, ok := routeMappedID(ep, byIface); ok {
			for _, alias := range ep.Aliases {
				byIface[alias] = id
			}
			for i := range snap.Tunnels {
				if snap.Tunnels[i].ID == id {
					snap.Tunnels[i].Iface = ep.Iface
					snap.Tunnels[i].Available = snap.Tunnels[i].Available || ep.Available
					snap.Tunnels[i].Enabled = snap.Tunnels[i].Enabled || ep.Enabled
					if snap.Tunnels[i].Status == "" {
						snap.Tunnels[i].Status = t.Status
					}
					break
				}
			}
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
	creditPolicy := func(r awgmgr.DNSRoute) {
		name := strings.TrimSpace(r.HRPolicyName)
		if name == "" {
			name = defaultHydraRoutePolicyName
		}
		ifaces := routePolicyInterfaces(r.HRPolicyInterfaces, byIface, snap.Tunnels)
		for i := range snap.Policies {
			if strings.EqualFold(snap.Policies[i].Name, name) {
				snap.Policies[i].DNS++
				snap.Policies[i].HRNeo++
				if len(ifaces) > len(snap.Policies[i].Interfaces) {
					snap.Policies[i].Interfaces = ifaces
				}
				return
			}
		}
		snap.Policies = append(snap.Policies, wire.RoutePolicySummary{
			Name:       name,
			Interfaces: ifaces,
			DNS:        1,
			HRNeo:      1,
		})
	}
	for _, r := range dns {
		isHR := isHydraRouteBackend(r)
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
				if isMovableHRNeoFallthrough(r) && len(r.HRPolicyInterfaces) > 0 {
					ruleBind = routePolicyLabel(r)
					creditPolicy(r)
				} else if isMovableHRNeoFallthrough(r) && defaultIface != "" {
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

func routePolicyLabel(r awgmgr.DNSRoute) string {
	name := strings.TrimSpace(r.HRPolicyName)
	if name == "" {
		name = defaultHydraRoutePolicyName
	}
	return "policy:" + name
}

func routePolicyInterfaces(binds []string, byIface map[string]string, tunnels []wire.TunnelMeta) []wire.RoutePolicyInterface {
	out := make([]wire.RoutePolicyInterface, 0, len(binds))
	seen := map[string]bool{}
	for _, bind := range binds {
		bind = strings.TrimSpace(bind)
		if bind == "" {
			continue
		}
		item := wire.RoutePolicyInterface{Bind: bind}
		if id, ok := byIface[bind]; ok {
			if t, ok := routeTunnelByID(tunnels, id); ok {
				item.Bind = t.Iface
				item.Name = t.Name
				item.Available = routeTunnelUsable(t)
			}
		}
		key := strings.ToLower(firstNonEmptyRoute(item.Name, item.Bind))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	activeAssigned := false
	for i := range out {
		if out[i].Available && !activeAssigned {
			out[i].Role = "active"
			activeAssigned = true
			continue
		}
		if out[i].Available {
			out[i].Role = "fallback"
		} else {
			out[i].Role = "unavailable"
		}
	}
	return out
}

func routeTunnelByID(tunnels []wire.TunnelMeta, id string) (wire.TunnelMeta, bool) {
	for _, t := range tunnels {
		if t.ID == id {
			return t, true
		}
	}
	return wire.TunnelMeta{}, false
}

func routeTunnelUsable(t wire.TunnelMeta) bool {
	if !t.Enabled {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(t.Status))
	return t.Available || status == "" || status == "running" || status == "up"
}

func handshakeAgeSeconds(t *time.Time) int {
	if t == nil {
		return 0
	}
	return int(time.Since(*t).Seconds())
}
