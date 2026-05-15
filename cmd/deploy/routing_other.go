//go:build !windows

package main

import "fmt"

// addHostRouteThroughInterface on non-Windows is a no-op stub that returns
// an error pointing the operator at the manual `ip route add ... metric 1`
// command. Detection still runs and prints a warning — the auto-fix is
// Windows-only for now because that's where the wizard ships and where
// operators actually hit the 192.168.0.x SSTP-vs-LAN collision in practice.
func addHostRouteThroughInterface(targetIP string, ifIndex int) (func(), error) {
	return nil, fmt.Errorf("auto route fix only supported on Windows; run manually: ip route add %s/32 dev <sstp-iface> metric 1", targetIP)
}
