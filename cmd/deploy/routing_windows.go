//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// addHostRouteThroughInterface installs a /32 host route for targetIP
// pinned to the given Windows interface index. Uses the legacy `route ADD`
// CLI (works without ICACLS-tier admin rights on most Windows 11 installs
// when the operator is in the Network Configuration Operators group; falls
// back with a clear error otherwise — wizard surfaces and asks the
// operator to relaunch elevated). Metric 1 = highest priority, beats the
// /24 entries from both LAN and SSTP.
//
// Returns a rollback function that deletes the same route; the caller defers
// it so the routing table is left clean whether deploy succeeded or not.
// Best-effort rollback: prints a warning + manual command if delete fails,
// rather than returning an error (we don't want to mask the deploy result).
func addHostRouteThroughInterface(targetIP string, ifIndex int) (func(), error) {
	addCmd := exec.Command("route", "ADD", targetIP, "MASK", "255.255.255.255", "0.0.0.0", "IF", fmt.Sprint(ifIndex), "METRIC", "1")
	out, err := addCmd.CombinedOutput()
	if err != nil {
		// Windows `route ADD` prints "The requested operation requires
		// elevation." on a non-admin shell. Pass it through so the
		// operator sees the actual reason.
		return nil, fmt.Errorf("route ADD %s/32 IF %d: %v: %s",
			targetIP, ifIndex, err, strings.TrimSpace(string(out)))
	}
	rollback := func() {
		delCmd := exec.Command("route", "DELETE", targetIP)
		if dout, derr := delCmd.CombinedOutput(); derr != nil {
			PrintWarn(fmt.Sprintf("не смог снять /32 маршрут на %s: %v: %s — удали руками: route DELETE %s",
				targetIP, derr, strings.TrimSpace(string(dout)), targetIP))
			return
		}
		PrintInfo(fmt.Sprintf("временный /32-маршрут к %s снят", targetIP))
	}
	return rollback, nil
}
