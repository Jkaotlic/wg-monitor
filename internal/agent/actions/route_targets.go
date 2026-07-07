package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

type routeEndpoint struct {
	ID           string
	Name         string
	Iface        string
	BindID       string
	Aliases      []string
	Type         string
	Enabled      bool
	Available    bool
	DefaultRoute bool
}

func managedRouteEndpoint(t awgmgr.Tunnel) routeEndpoint {
	return routeEndpoint{
		ID:           t.ID,
		Name:         firstNonEmptyRoute(t.Name, t.InterfaceName, t.ID),
		Iface:        t.InterfaceName,
		BindID:       firstNonEmptyRoute(t.NDMSName, t.ID, t.InterfaceName),
		Aliases:      routeAliases(t.InterfaceName, t.NDMSName, t.ID),
		Type:         "managed",
		Enabled:      t.Enabled,
		Available:    t.Enabled,
		DefaultRoute: t.DefaultRoute,
	}
}

func ndmsRouteEndpoint(t awgmgr.RoutingTunnel) routeEndpoint {
	iface := firstNonEmptyRoute(t.Iface, t.ID)
	typ := firstNonEmptyRoute(t.Type, "ndms")
	bindID := iface
	if strings.EqualFold(typ, "managed") {
		bindID = firstNonEmptyRoute(t.ID, iface)
	}
	return routeEndpoint{
		ID:        iface,
		Name:      firstNonEmptyRoute(t.Name, iface, t.ID),
		Iface:     iface,
		BindID:    bindID,
		Aliases:   routeAliases(iface, t.ID),
		Type:      typ,
		Enabled:   t.Available || routingStatusEnabled(t.Status),
		Available: t.Available,
	}
}

func routingStatusEnabled(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "up", "started", "active":
		return true
	default:
		return false
	}
}

func resolveRouteEndpoint(ctx context.Context, c *awgmgr.Client, id string) (routeEndpoint, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return routeEndpoint{}, fmt.Errorf("empty route target id")
	}
	routing, err := c.RoutingTunnels(ctx)
	if err == nil {
		for _, t := range routing {
			ep := ndmsRouteEndpoint(t)
			if ep.Iface == "" {
				continue
			}
			if id == ep.ID || id == ep.Iface || id == t.ID {
				if mt, getErr := getTunnel(ctx, c, t.ID); getErr == nil {
					managed := managedRouteEndpoint(*mt)
					ep = mergeRouteEndpoints(ep, managed)
				}
				return ep, nil
			}
		}
	}
	if t, getErr := getTunnel(ctx, c, id); getErr == nil {
		ep := managedRouteEndpoint(*t)
		if ep.Iface == "" {
			return routeEndpoint{}, fmt.Errorf("managed tunnel %s missing interfaceName", id)
		}
		if err == nil {
			if routingEP, ok := findRoutingEndpointForManaged(routing, ep); ok {
				return mergeRouteEndpoints(routingEP, ep), nil
			}
		}
		return ep, nil
	}
	if err != nil {
		return routeEndpoint{}, err
	}
	return routeEndpoint{}, fmt.Errorf("route target %q not found", id)
}

func findRoutingEndpointForManaged(routing []awgmgr.RoutingTunnel, managed routeEndpoint) (routeEndpoint, bool) {
	managedAliases := routeAliasSet(managed.Aliases)
	for _, t := range routing {
		ep := ndmsRouteEndpoint(t)
		if ep.Iface == "" {
			continue
		}
		for _, alias := range ep.Aliases {
			if routeBindMatches(alias, managedAliases) {
				return ep, true
			}
		}
	}
	return routeEndpoint{}, false
}

func mergeRouteEndpoints(routing, managed routeEndpoint) routeEndpoint {
	out := routing
	out.ID = managed.ID
	out.Name = firstNonEmptyRoute(managed.Name, routing.Name)
	out.BindID = firstNonEmptyRoute(routing.BindID, managed.BindID, routing.Iface, managed.Iface)
	out.Aliases = routeAliases(append(routing.Aliases, managed.Aliases...)...)
	out.Type = firstNonEmptyRoute(managed.Type, routing.Type)
	out.Enabled = routing.Enabled || managed.Enabled
	out.Available = routing.Available || managed.Available
	out.DefaultRoute = managed.DefaultRoute
	return out
}

func routeDNSBindID(ep routeEndpoint) string {
	if strings.EqualFold(ep.Type, "managed") {
		return firstNonEmptyRoute(ep.BindID, ep.Iface)
	}
	return ep.Iface
}

func routeAliases(values ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func routeAliasSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			out[v] = true
		}
	}
	return out
}

func routeBindMatches(bind string, aliases map[string]bool) bool {
	bind = strings.TrimSpace(bind)
	return bind != "" && aliases[bind]
}

func routeMappedID(ep routeEndpoint, known map[string]string) (string, bool) {
	for _, alias := range ep.Aliases {
		if id, ok := known[alias]; ok {
			return id, true
		}
	}
	return "", false
}
