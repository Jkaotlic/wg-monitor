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

// addTunnelToHydraRoutePolicies makes a freshly imported tunnel reachable as a
// standby for the router's access policies.
//
// On the policy model (awg-manager 2.16+) the binding lives on the policy, so
// the change is a permit call per policy, not an edit of every rule. On older
// builds the legacy per-rule path below still applies.
//
// Two invariants carry over from the legacy implementation and must not be
// relaxed:
//
//   - a policy with an EMPTY chain is left alone. An empty chain means "follow
//     the global default"; appending into it would make the fresh tunnel that
//     policy's sole and therefore active interface, hijacking all of its
//     traffic the moment a config is imported.
//   - the new interface goes to the END of the chain (order = len(chain)), so
//     it is a fallback, never the active egress.
//
// And one invariant is new, because its absence WAS the defect: the chain is
// re-read after the write and the interface must be there. Reporting success
// without checking is what let this function silently do nothing for months.
// It takes the whole tunnel, not just its iface: the name /api/access-policies
// accepts is the NDMS one ("Wireguard0", "OpkgTun12"), and reaching it from a
// kernel iface ("nwg0") needs every alias the tunnel has.
func addTunnelToHydraRoutePolicies(ctx context.Context, c *awgmgr.Client, t awgmgr.Tunnel) (int, error) {
	iface := strings.TrimSpace(t.InterfaceName)
	if iface == "" {
		return 0, nil
	}
	policies, err := c.AccessPolicies(ctx)
	switch {
	case err != nil && awgmgr.IsEndpointMissing(err):
		// No such endpoint — a build old enough to keep the binding on the rule.
		return addIfaceToHydraRouteRules(ctx, c, iface)
	case err != nil:
		// The endpoint exists and the read failed. Falling back to the legacy
		// path here would edit rules whose binding no longer lives there and
		// report success for a tunnel that never entered routing.
		return 0, fmt.Errorf("routing/access-policies: %w", err)
	case len(policies) == 0:
		// Policy model present but nothing configured — nothing to permit into.
		return addIfaceToHydraRouteRules(ctx, c, iface)
	}
	polIfaces, err := c.PolicyInterfaces(ctx)
	if err != nil {
		return 0, fmt.Errorf("routing/policy-interfaces: %w", err)
	}
	resolver := newPolicyIfaceResolver(nil, polIfaces)
	permitName, ok := resolver.permitName(t.NDMSName, t.InterfaceName, t.Name, t.ID)
	if !ok {
		return 0, fmt.Errorf("access policies do not offer interface %q (the router lists %d policy interfaces)", iface, len(polIfaces))
	}
	changed := 0
	for _, p := range policies {
		if len(p.Interfaces) == 0 || policyChainHasInterface(p, permitName) {
			continue
		}
		if err := c.PermitPolicyInterface(ctx, p.Name, permitName, len(p.Interfaces)); err != nil {
			return changed, fmt.Errorf("access-policies/permit %s/%s: %w", p.Name, permitName, err)
		}
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	// Перечитывать ОБЯЗАТЕЛЬНО мимо кэша NDMS. Обычный список политик
	// отдаётся кэшированным, и сразу после permit кэш ещё показывает цепочку
	// до записи: успешная привязка прочиталась бы как "интерфейс не появился",
	// а импортированный туннель был бы объявлен немаршрутизируемым. Не менять
	// на AccessPolicies -- этот отказ виден только против живого роутера.
	//
	// Но и наоборот: свежее чтение живёт в неймспейсе /api/access-policies
	// (?refresh=true есть только там), а сюда мы дошли, успешно прочитав
	// /api/routing/access-policies. Сборка, обслуживающая routing-путь и не
	// обслуживающая простой, ответит 404 именно на проверке -- и сработавший
	// permit был бы объявлен провалом, то есть ровно тем ложным отказом,
	// против которого свежее чтение и заведено. На отсутствие эндпоинта
	// откатываемся на кэшированный список: проверка кэшем слабее свежей
	// (устаревший ответ может дать ложный "интерфейс не появился"), но это
	// всё же настоящая проверка, и она лучше, чем рапорт об ошибке на успехе.
	// Любой другой отказ свежего чтения остаётся ошибкой: 500 не означает
	// "эндпоинта нет", и проверять постусловие в этот момент нечем.
	after, err := c.AccessPoliciesFresh(ctx)
	if err != nil && awgmgr.IsEndpointMissing(err) {
		after, err = c.AccessPolicies(ctx)
	}
	if err != nil {
		return changed, fmt.Errorf("verify access policies after permit: %w", err)
	}
	for _, p := range after {
		if len(p.Interfaces) == 0 {
			continue
		}
		if !policyChainHasInterface(p, permitName) {
			return changed, fmt.Errorf("interface %q did not appear in policy %q after permit", permitName, p.Name)
		}
	}
	return changed, nil
}

func policyChainHasInterface(p awgmgr.AccessPolicy, iface string) bool {
	for _, existing := range p.Interfaces {
		if strings.EqualFold(strings.TrimSpace(existing.Name), iface) {
			return true
		}
	}
	return false
}

// addIfaceToHydraRouteRules is the legacy per-rule model: it edits every
// HR-Neo DNS rule directly. Runs when the router serves no access policies.
func addIfaceToHydraRouteRules(ctx context.Context, c *awgmgr.Client, iface string) (int, error) {
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
