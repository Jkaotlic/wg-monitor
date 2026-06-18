package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

const defaultHydraRoutePolicyName = "HydraRoute"

func hrNeoPolicyRoute(name string, targets []string, iface string) awgmgr.DNSRoute {
	return awgmgr.DNSRoute{
		Name:               name,
		Domains:            targets,
		ManualDomains:      targets,
		Enabled:            true,
		Backend:            "hydraroute",
		HRPolicyName:       defaultHydraRoutePolicyName,
		HRRouteMode:        "policy",
		HRPolicyInterfaces: []string{iface},
	}
}

func addIfaceToHydraRoutePolicies(ctx context.Context, c *awgmgr.Client, iface string) (int, error) {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return 0, nil
	}
	all, err := c.ListDNSRoutes(ctx)
	if err != nil {
		return 0, fmt.Errorf("dns/list: %w", err)
	}
	changed := 0
	for _, rule := range all {
		if !isHydraRoutePolicyRule(rule) || routePolicyHasInterface(rule, iface) {
			continue
		}
		existing := cleanRoutePolicyInterfaces(rule.HRPolicyInterfaces)
		// Leave global-default policies (empty interface chain) untouched: they
		// follow the live default route, and appending into an empty list would
		// make the new tunnel their sole/active interface — hijacking all their
		// traffic onto a freshly imported tunnel. Only policies that already pin
		// an explicit chain get the new tunnel, where it lands as a fallback.
		if len(existing) == 0 {
			continue
		}
		updated := rule
		updated.HRPolicyInterfaces = append(existing, iface)
		if updated.HRPolicyName == "" {
			updated.HRPolicyName = defaultHydraRoutePolicyName
		}
		if updated.HRRouteMode == "" {
			updated.HRRouteMode = "policy"
		}
		if err := c.UpdateDNSRoute(ctx, updated); err != nil {
			return changed, fmt.Errorf("dns/update id=%s: %w", rule.ID, err)
		}
		changed++
	}
	return changed, nil
}

func isHydraRoutePolicyRule(rule awgmgr.DNSRoute) bool {
	if !strings.EqualFold(strings.TrimSpace(rule.Backend), "hydraroute") {
		return false
	}
	if len(rule.Routes) > 0 {
		return false
	}
	if isDirectProviderHRNeoPolicy(rule) {
		return false
	}
	policy := strings.TrimSpace(rule.HRPolicyName)
	return policy == "" || strings.EqualFold(policy, defaultHydraRoutePolicyName)
}

func routePolicyHasInterface(rule awgmgr.DNSRoute, iface string) bool {
	for _, existing := range rule.HRPolicyInterfaces {
		if strings.EqualFold(strings.TrimSpace(existing), iface) {
			return true
		}
	}
	return false
}

func cleanRoutePolicyInterfaces(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}
