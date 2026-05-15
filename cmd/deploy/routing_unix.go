//go:build linux || darwin

package main

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
)

// addHostRouteThroughInterface installs a /32 host route for targetIP via
// the interface at ifIndex. Linux uses iproute2 (`ip route add ... metric 1`),
// macOS uses BSD `route -n add -host ... -interface ...`. We try the direct
// call first (wizard running as root, e.g. inside docker), and on permission
// failure retry once through `sudo` — the operator's terminal is a TTY, so
// sudo can prompt for password and cache credentials for the same-session
// cleanup. Returns a rollback that mirrors whichever path succeeded.
func addHostRouteThroughInterface(targetIP string, ifIndex int) (func(), error) {
	iface, err := net.InterfaceByIndex(ifIndex)
	if err != nil {
		return nil, fmt.Errorf("resolve iface name for index %d: %w", ifIndex, err)
	}
	binary, addArgs, delArgs, manualHint := unixRouteCmds(runtime.GOOS, targetIP, iface.Name)
	if binary == "" {
		return nil, fmt.Errorf("unsupported OS %q for auto route fix; run manually: %s", runtime.GOOS, manualHint)
	}

	// First attempt: direct (works when wizard runs as root or has CAP_NET_ADMIN).
	if out, err := exec.Command(binary, addArgs...).CombinedOutput(); err == nil {
		return makeUnixRollback(false, binary, delArgs, targetIP), nil
	} else if !looksLikePermissionDenied(out, err) {
		// A non-permission failure (e.g. duplicate route already present
		// because of a previous wizard crash). Surface it — operator can
		// `route delete` and retry.
		return nil, fmt.Errorf("%s %s: %v: %s", binary, strings.Join(addArgs, " "), err, strings.TrimSpace(string(out)))
	}

	// Retry through sudo. Print a heads-up so the operator doesn't think we're hung.
	PrintInfo(fmt.Sprintf("повторяю через sudo (введи пароль) — нужен root чтобы написать в routing table: sudo %s %s",
		binary, strings.Join(addArgs, " ")))
	sudoArgs := append([]string{binary}, addArgs...)
	cmd := exec.Command("sudo", sudoArgs...)
	cmd.Stdin = nil // let sudo grab the terminal
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sudo %s %s: %v: %s",
			binary, strings.Join(addArgs, " "), err, strings.TrimSpace(string(out)))
	}
	return makeUnixRollback(true, binary, delArgs, targetIP), nil
}

// unixRouteCmds returns (binary, addArgs, delArgs, manualHint) for the
// host's OS. Empty binary signals an OS we can't auto-fix; the caller then
// just returns the manualHint to the operator.
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

// looksLikePermissionDenied returns true when an exec.Command failure is
// almost certainly a "you're not root" outcome rather than a real config
// problem. We check both the stderr/stdout text (matches what `ip` /
// `route` actually print on most distros) and the typical EPERM-style
// exit status. False positives just retry through sudo — also fine.
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

// makeUnixRollback closes over the chosen invocation path so cleanup uses
// the same binary + args (and the same sudo decision) as the add. Sudo
// will reuse the credentials cache, so the operator doesn't see a second
// password prompt for the cleanup that runs seconds-to-minutes later.
func makeUnixRollback(usedSudo bool, binary string, delArgs []string, targetIP string) func() {
	return func() {
		var cmd *exec.Cmd
		if usedSudo {
			cmd = exec.Command("sudo", append([]string{binary}, delArgs...)...)
		} else {
			cmd = exec.Command(binary, delArgs...)
		}
		if dout, derr := cmd.CombinedOutput(); derr != nil {
			PrintWarn(fmt.Sprintf("не смог снять /32 маршрут к %s: %v: %s — удали руками: %s %s %s",
				targetIP, derr, strings.TrimSpace(string(dout)),
				sudoPrefix(usedSudo), binary, strings.Join(delArgs, " ")))
			return
		}
		PrintInfo(fmt.Sprintf("временный /32-маршрут к %s снят", targetIP))
	}
}

func sudoPrefix(usedSudo bool) string {
	if usedSudo {
		return "sudo"
	}
	return ""
}
