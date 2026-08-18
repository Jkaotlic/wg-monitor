package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

const defaultHydraRoutePolicyName = "HydraRoute"

// hrNeoPolicyRoute builds an HR-Neo rule pinned to one interface.
//
// hrRouteMode is an enum: "policy" takes the binding from the named policy and
// ignores the rule's own interface fields, "interface" pins the rule itself.
// Setting mode="policy" AND a non-empty interface list — which is what this
// function used to do — is self-contradictory: the rule silently followed the
// policy instead of the requested tunnel.
//
// The pin goes into Routes, not into HRPolicyInterfaces: that is where
// awg-manager reads an interface-target rule from. The two fields of the entry
// are NOT the same namespace — `interface` is the kernel iface ("nwg1",
// "opkgtun11") while `tunnelId` is the stable bind id, exactly as the NDMS
// branch of route_add already sends it. HRPolicyName stays empty so nothing
// re-reads this rule as policy-bound.
func hrNeoPolicyRoute(name string, targets []string, iface, bindID string) awgmgr.DNSRoute {
	return awgmgr.DNSRoute{
		Name:          name,
		Domains:       targets,
		ManualDomains: targets,
		Enabled:       true,
		Backend:       "hydraroute",
		HRRouteMode:   "interface",
		Routes:        []awgmgr.DNSRouteEntry{{Interface: iface, TunnelID: bindID, Fallback: "auto"}},
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
