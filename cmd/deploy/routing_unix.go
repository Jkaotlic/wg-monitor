//go:build linux || darwin

package main

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// sudoSession remembers whether we needed sudo for the FIRST route add in
// this wizard run. Subsequent del/add calls reuse the same decision so
// the operator doesn't get re-prompted (sudo cred cache handles it for ~5
// minutes by default).
var sudoSession = struct {
	mu    sync.Mutex
	known bool
	used  bool
}{}

// addTempHostRoute installs a /32 host route for targetIP via the
// interface at ifIndex. Linux uses iproute2, macOS uses BSD `route`. Tries
// direct exec first, falls back to sudo on permission denied. The sudo
// fallback prints a heads-up because the password prompt isn't visible
// in our wrapper.
func addTempHostRoute(targetIP string, ifIndex int) (RouteToken, error) {
	iface, err := net.InterfaceByIndex(ifIndex)
	if err != nil {
		return RouteToken{}, fmt.Errorf("resolve iface name for index %d: %w", ifIndex, err)
	}
	binary, addArgs, _, manualHint := unixRouteCmds(runtime.GOOS, targetIP, iface.Name)
	if binary == "" {
		return RouteToken{}, fmt.Errorf("unsupported OS %q for auto route fix; run manually: %s", runtime.GOOS, manualHint)
	}

	if out, err := exec.Command(binary, addArgs...).CombinedOutput(); err == nil {
		setSudoSessionUsed(false)
		return RouteToken{TargetIP: targetIP, IfIndex: ifIndex}, nil
	} else if !looksLikePermissionDenied(out, err) {
		return RouteToken{}, fmt.Errorf("%s %s: %v: %s", binary, strings.Join(addArgs, " "), err, strings.TrimSpace(string(out)))
	}

	if !sudoSessionUsed() {
		PrintInfo(fmt.Sprintf("повторяю через sudo (введи пароль) — нужен root для routing table: sudo %s %s",
			binary, strings.Join(addArgs, " ")))
	}
	sudoArgs := append([]string{binary}, addArgs...)
	cmd := exec.Command("sudo", sudoArgs...)
	cmd.Stdin = nil
	if out, err := cmd.CombinedOutput(); err != nil {
		return RouteToken{}, fmt.Errorf("sudo %s %s: %v: %s",
			binary, strings.Join(addArgs, " "), err, strings.TrimSpace(string(out)))
	}
	setSudoSessionUsed(true)
	return RouteToken{TargetIP: targetIP, IfIndex: ifIndex}, nil
}

// delTempHostRoute mirrors the add path's sudo decision. Best-effort:
// warns on failure but returns nil so the deploy result isn't masked.
func delTempHostRoute(t RouteToken) error {
	iface, err := net.InterfaceByIndex(t.IfIndex)
	if err != nil {
		PrintWarn(fmt.Sprintf("delTempHostRoute: cannot resolve iface idx %d: %v", t.IfIndex, err))
		return nil
	}
	binary, _, delArgs, manualHint := unixRouteCmds(runtime.GOOS, t.TargetIP, iface.Name)
	if binary == "" {
		PrintWarn("delTempHostRoute: " + manualHint)
		return nil
	}
	var cmd *exec.Cmd
	if sudoSessionUsed() {
		cmd = exec.Command("sudo", append([]string{binary}, delArgs...)...)
	} else {
		cmd = exec.Command(binary, delArgs...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		PrintWarn(fmt.Sprintf("не смог снять /32 маршрут к %s: %v: %s — удали руками: %s %s %s",
			t.TargetIP, err, strings.TrimSpace(string(out)),
			sudoPrefix(), binary, strings.Join(delArgs, " ")))
		return nil
	}
	PrintInfo(fmt.Sprintf("временный /32-маршрут к %s снят", t.TargetIP))
	return nil
}

func setSudoSessionUsed(used bool) {
	sudoSession.mu.Lock()
	defer sudoSession.mu.Unlock()
	sudoSession.known = true
	sudoSession.used = used
}

func sudoSessionUsed() bool {
	sudoSession.mu.Lock()
	defer sudoSession.mu.Unlock()
	return sudoSession.known && sudoSession.used
}

func sudoPrefix() string {
	if sudoSessionUsed() {
		return "sudo"
	}
	return ""
}

func unixRouteCmds(goos, targetIP, ifaceName string) (string, []string, []string, string) {
	switch goos {
	case "linux":
		return "ip",
			[]string{"route", "add", targetIP + "/32", "dev", ifaceName, "metric", "1"},
			[]string{"route", "del", targetIP + "/32"},
			fmt.Sprintf("sudo ip route add %s/32 dev %s metric 1", targetIP, ifaceName)
	case "darwin":
		return "route",
			[]string{"-n", "add", "-host", targetIP, "-interface", ifaceName},
			[]string{"-n", "delete", "-host", targetIP},
			fmt.Sprintf("sudo route -n add -host %s -interface %s", targetIP, ifaceName)
	}
	return "", nil, nil, ""
}

func looksLikePermissionDenied(out []byte, err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(string(out) + " " + err.Error())
	return strings.Contains(s, "permission denied") ||
		strings.Contains(s, "operation not permitted") ||
		strings.Contains(s, "must be root") ||
		strings.Contains(s, "you must be root") ||
		strings.Contains(s, "rtnetlink answers: operation not permitted")
}

func manualRouteCommands(targetIP string, iface net.Interface) (add, del string) {
	binary, addArgs, delArgs, manualHint := unixRouteCmds(runtime.GOOS, targetIP, iface.Name)
	if binary == "" {
		return manualHint, fmt.Sprintf("remove the host route for %s via your OS routing tool", targetIP)
	}
	return strings.TrimSpace("sudo " + binary + " " + strings.Join(addArgs, " ")),
		strings.TrimSpace("sudo " + binary + " " + strings.Join(delArgs, " "))
}

func manualRouteHint(targetIP string, c PathCandidate) string {
	if c.Iface == "" {
		return fmt.Sprintf("  • Добавь /32 host-route к %s через VPN-интерфейс.\n", targetIP)
	}
	iface := net.Interface{Name: c.Iface, Index: c.Index}
	add, del := manualRouteCommands(targetIP, iface)
	return fmt.Sprintf(
		"  • Wizard видит VPN-интерфейс %s, но не смог сам поставить /32 маршрут.\n"+
			"    Быстрый фикс:\n"+
			"      %s\n"+
			"    После работы можно снять маршрут:\n"+
			"      %s\n"+
			"    Долгий фикс: добавь в WireGuard AllowedIPs точный адрес %s/32.\n",
		c.Iface, add, del, targetIP,
	)
}
