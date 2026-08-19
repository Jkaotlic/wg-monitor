package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// RoutePolicyPromoteJSON makes tunnelID the first link of a policy's chain --
// the "сделать главным" step.
//
// The mechanism is not obvious and was established against a live router
// (2.17.2+r15, docs/operations/2026-08-19-permit-via-router-ui-session.md):
// awg-manager has NO reorder endpoint. Promotion is `permit` with order 0 for
// an interface ALREADY in the chain; the router shifts the rest down itself.
// The router's own web UI does exactly this — its "Вверх" button posts the
// same request.
//
// Reordering only. A tunnel that is not already in the chain is refused rather
// than added: adding a new interface to a policy has a different blast radius
// (it changes what the policy can fall back to, not merely the order) and
// belongs to the config-replacement wizard.
//
// The postcondition is verified by re-reading policies PAST the NDMS cache.
// This API answers success:true without doing anything — observed on a bridge
// interface, both from our client and from the router's own UI — so trusting
// the response would report a no-op as a completed promotion.
func RoutePolicyPromoteJSON(ctx context.Context, c *awgmgr.Client, req wire.RoutePolicyPromoteRequest) (string, error) {
	if c == nil {
		return "", fmt.Errorf("route_policy_promote: awg-manager client is required")
	}
	policyName := strings.TrimSpace(req.PolicyName)
	tunnelID := strings.TrimSpace(req.TunnelID)
	if policyName == "" || tunnelID == "" {
		return "", fmt.Errorf("route_policy_promote: policy_name and tunnel_id are required")
	}

	resolver, err := promoteResolver(ctx, c)
	if err != nil {
		return "", err
	}
	policies, err := c.AccessPolicies(ctx)
	if err != nil {
		return "", fmt.Errorf("route_policy_promote: read policies: %w", err)
	}
	policy, ok := findAccessPolicy(policies, policyName)
	if !ok {
		return "", fmt.Errorf("route_policy_promote: policy %q not found", policyName)
	}

	iface := ""
	for _, link := range buildPolicySummary(policy, resolver).Interfaces {
		if link.TunnelID == tunnelID {
			iface = link.Bind
			break
		}
	}
	if iface == "" {
		return "", fmt.Errorf("route_policy_promote: tunnel %q is not in policy %q", tunnelID, policyName)
	}

	if err := c.PermitPolicyInterface(ctx, policy.Name, iface, 0); err != nil {
		return "", fmt.Errorf("route_policy_promote: permit %q: %w", iface, err)
	}

	fresh, err := c.AccessPoliciesFresh(ctx)
	if err != nil {
		return "", fmt.Errorf("route_policy_promote: verify: %w", err)
	}
	after, ok := findAccessPolicy(fresh, policyName)
	if !ok || len(after.Interfaces) == 0 || !strings.EqualFold(strings.TrimSpace(after.Interfaces[0].Name), iface) {
		return "", fmt.Errorf("route_policy_promote: %q did not become the first link of %q", iface, policyName)
	}

	b, err := json.Marshal(buildPolicySummary(after, resolver))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// promoteResolver builds the same interface→tunnel resolver the snapshot uses,
// from the same two sources, so "which tunnel is this chain link" has one
// answer in the whole agent rather than two that can drift.
func promoteResolver(ctx context.Context, c *awgmgr.Client) (*policyIfaceResolver, error) {
	tunnels, err := c.TunnelsAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("route_policy_promote: read tunnels: %w", err)
	}
	// Missing policy-interfaces is not fatal: older builds lack the endpoint,
	// and the resolver still matches by tunnel alias without it.
	polIfaces, _ := c.PolicyInterfaces(ctx)

	var managed []wire.TunnelMeta
	if tunnels != nil {
		for _, t := range tunnels.Tunnels {
			ep := managedRouteEndpoint(t)
			if ep.Iface == "" {
				continue
			}
			managed = append(managed, wire.TunnelMeta{
				ID: ep.ID, Name: ep.Name, Iface: ep.Iface, Type: "managed",
				NDMSName: t.NDMSName, Enabled: ep.Enabled,
			})
		}
	}
	return newPolicyIfaceResolver(managed, polIfaces), nil
}

func findAccessPolicy(policies []awgmgr.AccessPolicy, name string) (awgmgr.AccessPolicy, bool) {
	for _, p := range policies {
		if strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(name)) {
			return p, true
		}
	}
	return awgmgr.AccessPolicy{}, false
}
