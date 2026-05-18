//go:build !windows && !linux && !darwin

package main

import (
	"fmt"
	"net"
)

// addTempHostRoute is a no-op stub for OSes we don't have a route-add
// command line for (BSDs other than darwin, plan9, etc). Returns a
// zero-value token and an error pointing at manual remediation.
func addTempHostRoute(targetIP string, ifIndex int) (RouteToken, error) {
	return RouteToken{}, fmt.Errorf("auto route fix not implemented for this OS; add /32 host route via your SSTP/VPN iface manually for %s", targetIP)
}

// delTempHostRoute is a no-op on unsupported OSes — addTempHostRoute
// never returns a valid token so this is unreachable in practice, but
// the function must exist for cross-build to succeed.
func delTempHostRoute(t RouteToken) error {
	return nil
}

func manualRouteCommands(targetIP string, iface net.Interface) (add, del string) {
	name := iface.Name
	if name == "" {
		name = fmt.Sprintf("ifIndex=%d", iface.Index)
	}
	return fmt.Sprintf("add a /32 host route for %s via %s using your OS routing tool", targetIP, name),
		fmt.Sprintf("remove the /32 host route for %s using your OS routing tool", targetIP)
}

func manualRouteHint(targetIP string, c PathCandidate) string {
	add, del := manualRouteCommands(targetIP, net.Interface{Name: c.Iface, Index: c.Index})
	return fmt.Sprintf(
		"  • Wizard видит VPN-интерфейс %s, но авто-маршрут для этой ОС не реализован.\n"+
			"    Быстрый фикс: %s\n"+
			"    После работы: %s\n"+
			"    Долгий фикс: добавь в WireGuard AllowedIPs точный адрес %s/32.\n",
		c.Iface, add, del, targetIP,
	)
}
