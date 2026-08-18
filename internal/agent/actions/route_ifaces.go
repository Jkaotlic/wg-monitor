package actions

import (
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// policyIfaceResolver answers the two questions the routing snapshot cannot
// answer without it.
//
// First: an access policy names its interfaces with NDMS/RCI names
// ("OpkgTun11", "Wireguard0", "GigabitEthernet1") while our tunnels are known
// by id ("awg11"), kernel iface ("opkgtun11") and — for nativewg tunnels only —
// ndmsName ("Wireguard0"). Plain string equality matches almost nothing, so
// every policy→tunnel lookup has to go through here.
//
// Second: a policy interface that matches no tunnel of ours is NOT an error.
// WAN, bridges and guest networks are legitimate policy targets, and traffic
// leaving through them is unprotected. That fact must reach the screen instead
// of being swallowed as "unknown".
type policyIfaceResolver struct {
	tunnelByKey map[string]string // lowercased alias → tunnel id
	upByName    map[string]bool   // lowercased policy interface name → up
	nameByKey   map[string]string // lowercased alias → exact policy interface name
}

func newPolicyIfaceResolver(tunnels []wire.TunnelMeta, ifaces []awgmgr.PolicyInterface) *policyIfaceResolver {
	r := &policyIfaceResolver{
		tunnelByKey: map[string]string{},
		upByName:    map[string]bool{},
		nameByKey:   map[string]string{},
	}
	// Build tunnelByKey in global tiers of alias strength: a stronger alias always
	// wins over a weaker one regardless of tunnel slice order. Within each tier,
	// first occurrence wins. Tier order: NDMSName (literal match) > Iface
	// (case-insensitive) > ID > Name (label lookup). This ensures consistent
	// resolution regardless of tunnel list order.
	// Tier 1: NDMSName (global literal match in policy)
	for _, t := range tunnels {
		if key := policyIfaceKey(t.NDMSName); key != "" {
			if _, taken := r.tunnelByKey[key]; !taken {
				r.tunnelByKey[key] = t.ID
			}
		}
	}
	// Tier 2: Iface (case-insensitive for opkgtun-style, unrelated for nativewg)
	for _, t := range tunnels {
		if key := policyIfaceKey(t.Iface); key != "" {
			if _, taken := r.tunnelByKey[key]; !taken {
				r.tunnelByKey[key] = t.ID
			}
		}
	}
	// Tier 3: ID
	for _, t := range tunnels {
		if key := policyIfaceKey(t.ID); key != "" {
			if _, taken := r.tunnelByKey[key]; !taken {
				r.tunnelByKey[key] = t.ID
			}
		}
	}
	// Tier 4: Name (weakest, matched only through policy label)
	for _, t := range tunnels {
		if key := policyIfaceKey(t.Name); key != "" {
			if _, taken := r.tunnelByKey[key]; !taken {
				r.tunnelByKey[key] = t.ID
			}
		}
	}
	for _, pi := range ifaces {
		if key := policyIfaceKey(pi.Name); key != "" {
			r.upByName[key] = pi.Up
			r.nameByKey[key] = pi.Name
		}
		if key := policyIfaceKey(pi.Label); key != "" {
			if _, taken := r.nameByKey[key]; !taken {
				r.nameByKey[key] = pi.Name
			}
		}
	}
	return r
}

func policyIfaceKey(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// tunnelForNames maps a policy chain entry (its name, then its label) to one of
// our tunnels. ok=false means the interface is not a tunnel of ours — WAN,
// bridge, guest network — and traffic on it leaves the router unprotected.
func (r *policyIfaceResolver) tunnelForNames(names ...string) (string, bool) {
	for _, name := range names {
		if id, ok := r.tunnelByKey[policyIfaceKey(name)]; ok {
			return id, true
		}
	}
	return "", false
}

// up reports the live state of a policy chain entry. An interface the router
// does not list is reported down: it cannot be carrying traffic, and guessing
// "up" would promote it to the active link of the chain.
func (r *policyIfaceResolver) up(name string) bool {
	state, ok := r.upByName[policyIfaceKey(name)]
	return ok && state
}

// knownInterface reports whether the router lists this name among the
// interfaces policies may use — true even when it is not a tunnel of ours
// (WAN, bridge, guest network). Callers use it to tell "an interface we cannot
// map to a tunnel" from "a name that means nothing here".
func (r *policyIfaceResolver) knownInterface(name string) bool {
	_, ok := r.nameByKey[policyIfaceKey(name)]
	return ok
}

// permitName returns the exact interface name /api/access-policies/permit will
// accept for a tunnel known by the given aliases. ok=false means the router
// does not offer this interface to policies at all — the caller must fail
// loudly, never report success.
func (r *policyIfaceResolver) permitName(aliases ...string) (string, bool) {
	for _, alias := range aliases {
		if name, ok := r.nameByKey[policyIfaceKey(alias)]; ok {
			return name, true
		}
	}
	return "", false
}
