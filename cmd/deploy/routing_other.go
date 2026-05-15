//go:build !windows && !linux && !darwin

package main

import "fmt"

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
