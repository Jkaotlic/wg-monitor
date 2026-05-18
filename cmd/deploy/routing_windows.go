//go:build windows

package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// addTempHostRoute installs a /32 host route for targetIP pinned to the
// Windows interface at ifIndex. Uses the legacy `route ADD` CLI (works
// without ICACLS-tier admin rights on most Windows 11 installs when the
// operator is in Network Configuration Operators group; otherwise falls
// back with an access-denied error — wizard surfaces and continues with
// default-route probe). Metric 1 beats /24 entries from both LAN and SSTP.
//
// Returns RouteToken which the caller passes to delTempHostRoute to undo.
// We return the token rather than a closure so a single defer can iterate
// over multiple tokens collected during multi-iface probing.
func addTempHostRoute(targetIP string, ifIndex int) (RouteToken, error) {
	addCmd := exec.Command("route", "ADD", targetIP, "MASK", "255.255.255.255", "0.0.0.0", "IF", fmt.Sprint(ifIndex), "METRIC", "1")
	out, err := addCmd.CombinedOutput()
	if err != nil {
		return RouteToken{}, fmt.Errorf("route ADD %s/32 IF %d: %v: %s",
			targetIP, ifIndex, err, strings.TrimSpace(string(out)))
	}
	return RouteToken{TargetIP: targetIP, IfIndex: ifIndex}, nil
}

// delTempHostRoute removes the route that addTempHostRoute installed.
// `route DELETE` doesn't take an IF param — IP alone identifies the route
// added at METRIC 1 above. Errors print a warning + manual hint and
// return nil — we don't want a cleanup failure to mask the deploy result.
func delTempHostRoute(t RouteToken) error {
	delCmd := exec.Command("route", "DELETE", t.TargetIP)
	if out, err := delCmd.CombinedOutput(); err != nil {
		PrintWarn(fmt.Sprintf("не смог снять /32 маршрут на %s: %v: %s — удали руками: route DELETE %s",
			t.TargetIP, err, strings.TrimSpace(string(out)), t.TargetIP))
		return nil
	}
	PrintInfo(fmt.Sprintf("временный /32-маршрут к %s снят", t.TargetIP))
	return nil
}

func manualRouteCommands(targetIP string, iface net.Interface) (add, del string) {
	return fmt.Sprintf("route ADD %s MASK 255.255.255.255 0.0.0.0 IF %d METRIC 1", targetIP, iface.Index),
		fmt.Sprintf("route DELETE %s", targetIP)
}

func manualRouteHint(targetIP string, c PathCandidate) string {
	if c.Index == 0 {
		return fmt.Sprintf(
			"  • Windows видит VPN-интерфейс %s, но не знает его IF index. Запусти netfix --agent <nick>, чтобы wizard перечислил доступные интерфейсы.\n",
			c.Iface,
		)
	}
	add := fmt.Sprintf("route ADD %s MASK 255.255.255.255 0.0.0.0 IF %d METRIC 1", targetIP, c.Index)
	del := fmt.Sprintf("route DELETE %s", targetIP)
	return fmt.Sprintf(
		"  • Windows видит VPN-интерфейс %s, но не дала wizard'у поставить временный /32 маршрут без админских прав.\n"+
			"    Быстрый фикс: открой PowerShell от имени администратора и выполни:\n"+
			"      %s\n"+
			"    Потом повтори wizard-команду. После работы можно снять маршрут:\n"+
			"      %s\n"+
			"    Долгий фикс: добавь в WireGuard AllowedIPs точный адрес %s/32.\n",
		c.Iface, add, del, targetIP,
	)
}
