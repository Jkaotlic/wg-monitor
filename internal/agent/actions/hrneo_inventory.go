package actions

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// HRNeoInventoryJSON fetches live HR-Neo status and DNS route inventory, then
// returns the hydraroute-backed rule subset as JSON for CommandResult.Output.
func HRNeoInventoryJSON(ctx context.Context, c *awgmgr.Client) (string, error) {
	var (
		hr  *awgmgr.HydraRouteStatus
		dns []awgmgr.DNSRoute
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { hr, err = c.HydraRouteStatus(gctx); return })
	g.Go(func() (err error) { dns, err = c.ListDNSRoutes(gctx); return })
	if err := g.Wait(); err != nil {
		return "", err
	}
	inv := buildHRNeoInventory(hr, dns)
	b, err := json.Marshal(inv)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildHRNeoInventory(hr *awgmgr.HydraRouteStatus, dns []awgmgr.DNSRoute) wire.HRNeoInventory {
	var inv wire.HRNeoInventory
	if hr != nil {
		inv.Status = wire.HRStatus{Installed: hr.Installed, Running: hr.Running}
	}
	for _, route := range dns {
		if !isHydraRouteBackend(route) {
			continue
		}
		rule := wire.HRNeoRule{
			ID:         route.ID,
			Name:       route.Name,
			Enabled:    route.Enabled,
			Mode:       route.HRRouteMode,
			PolicyName: route.HRPolicyName,
		}
		if len(route.Routes) > 0 {
			rule.Bind = route.Routes[0].Interface
			if rule.Bind == "" {
				rule.Bind = route.Routes[0].TunnelID
			}
		} else if ifaces := cleanRoutePolicyInterfaces(route.HRPolicyInterfaces); len(ifaces) > 0 {
			rule.Bind = strings.Join(ifaces, ", ")
		}
		rule.Domains, rule.Routes = splitHRNeoTargets(route.Domains, rule.Routes)
		rule.ManualDomains, rule.Routes = splitHRNeoTargets(route.ManualDomains, rule.Routes)
		inv.Rules = append(inv.Rules, rule)
	}
	return inv
}

func splitHRNeoTargets(values []string, routes []string) ([]string, []string) {
	domains := make([]string, 0, len(values))
	for _, value := range values {
		if isIPRouteTarget(value) {
			routes = append(routes, value)
			continue
		}
		domains = append(domains, value)
	}
	return domains, routes
}

func isIPRouteTarget(value string) bool {
	if _, err := netip.ParsePrefix(value); err == nil {
		return true
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return true
	}
	return false
}
