//go:build !windows && !linux && !darwin

package main

import "fmt"

// addHostRouteThroughInterface is a no-op stub for OSes we don't have a
// route-add command line for (BSDs other than darwin, plan9, etc).
// Operator gets a clear error pointing at manual remediation. Cross-build
// catches anyone who tries to ship a wizard there before we plug it in.
func addHostRouteThroughInterface(targetIP string, ifIndex int) (func(), error) {
	return nil, fmt.Errorf("auto route fix not implemented for this OS; add /32 host route via your SSTP/VPN iface manually for %s", targetIP)
}
