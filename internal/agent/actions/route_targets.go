package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

type routeEndpoint struct {
	ID           string
	Name         string
	Iface        string
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
		Type:         "managed",
		Enabled:      t.Enabled,
		Available:    t.Enabled,
		DefaultRoute: t.DefaultRoute,
	}
}

func ndmsRouteEndpoint(t awgmgr.RoutingTunnel) routeEndpoint {
	iface := firstNonEmptyRoute(t.Iface, t.ID)
	return routeEndpoint{
		ID:        iface,
		Name:      firstNonEmptyRoute(t.Name, iface, t.ID),
		Iface:     iface,
		Type:      firstNonEmptyRoute(t.Type, "ndms"),
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
	if t, err := getTunnel(ctx, c, id); err == nil {
		ep := managedRouteEndpoint(*t)
		if ep.Iface == "" {
			return routeEndpoint{}, fmt.Errorf("managed tunnel %s missing interfaceName", id)
		}
		return ep, nil
	}
	routing, err := c.RoutingTunnels(ctx)
	if err != nil {
		return routeEndpoint{}, err
	}
	for _, t := range routing {
		ep := ndmsRouteEndpoint(t)
		if ep.Iface == "" {
			continue
		}
		if id == ep.ID || id == ep.Iface || id == t.ID {
			return ep, nil
		}
	}
	return routeEndpoint{}, fmt.Errorf("route target %q not found", id)
}
