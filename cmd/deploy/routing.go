package main

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// routeCollision describes a routing-table ambiguity that's likely to make
// TCP connect to TargetIP go to the wrong physical box. Populated by
// detectRoutingCollision when the wizard is about to SSH a Keenetic LAN-IP
// (e.g. 192.168.31.1) and the operator's PC has TWO live interfaces whose
// subnets contain that IP — typically own LAN + an SSTP/VPN tunnel.
type routeCollision struct {
	TargetIP  net.IP // parsed
	Local     []ifaceMatch
	PointToPt []ifaceMatch // FlagPointToPoint, likely SSTP/PPP/WG/OpenVPN
}

// ifaceMatch is one interface whose subnet contains TargetIP. Index is the
// OS-level interface index suitable for `route ADD ... IF <idx>` on Windows.
type ifaceMatch struct {
	Name   string
	Index  int
	IPNet  string // e.g. "192.168.31.5/24"
	Flags  net.Flags
}

// detectRoutingCollision returns non-nil when targetHost (IPv4 dotted-quad)
// falls inside the subnet of ≥2 live interfaces of the operator's machine.
// Returns nil when target isn't a usable IPv4, there's no collision, or
// enumeration failed (best-effort — we don't want detection bugs blocking
// deploy).
//
// The two-bucket split (Local vs PointToPt) lets the caller default-suggest
// the point-to-point interface as the "right" SSTP-side path while still
// letting the operator pick manually if multiple tunnels are active.
func detectRoutingCollision(targetHost string) *routeCollision {
	ip := net.ParseIP(targetHost)
	if ip == nil {
		return nil
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil // IPv6 / hostname — out of scope
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	rc := &routeCollision{TargetIP: ip4}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			if !ipnet.Contains(ip4) {
				continue
			}
			m := ifaceMatch{
				Name:  iface.Name,
				Index: iface.Index,
				IPNet: ipnet.String(),
				Flags: iface.Flags,
			}
			if iface.Flags&net.FlagPointToPoint != 0 {
				rc.PointToPt = append(rc.PointToPt, m)
			} else {
				rc.Local = append(rc.Local, m)
			}
			break // one match per iface is enough
		}
	}
	if len(rc.Local)+len(rc.PointToPt) < 2 {
		return nil
	}
	return rc
}

// setupRouteFix is the call site used by deploy actions immediately before
// ConnectSSH. It detects routing-table ambiguity (operator's LAN + SSTP
// both in the same /24 as targetHost), and on confirm installs a /32 host
// route via the chosen interface so Windows reliably sends packets through
// the SSTP tunnel. Returns a cleanup function for `defer` — always safe to
// call, even when no route was added (no-op then).
//
// Skipped silently when:
//   - targetHost is empty or not an IPv4 literal (hostname / IPv6) — no
//     point detecting since we can't pre-compute the subnet
//   - no collision is detected — single matching interface, deploy proceeds
//   - operator declines the auto-fix (warning printed, deploy still tried)
//
// WG_YES_TO_ALL=1 → auto-accepts the route fix without prompting (matches
// the wizard's existing yes-to-all convention used by [3] install-agent).
func setupRouteFix(targetHost string) func() {
	noop := func() {}
	rc := detectRoutingCollision(targetHost)
	if rc == nil {
		return noop
	}
	PrintWarn(describeCollision(rc))

	candidates := rc.PointToPt
	if len(candidates) == 0 {
		// All collisions are local — operator likely has overlapping LAN
		// segments, no SSTP up. We can't auto-pick a tunnel that doesn't
		// exist. Warn and proceed; deploy will likely fail with a clearer
		// timeout than before.
		PrintWarn("SSTP/VPN не обнаружен — не могу автоматически починить маршрут. Подними туннель и повтори, либо вручную: route ADD " + targetHost + " MASK 255.255.255.255 <sstp-gateway>")
		return noop
	}

	var chosen ifaceMatch
	switch len(candidates) {
	case 1:
		chosen = candidates[0]
		PrintInfo(fmt.Sprintf("кандидат для /32-маршрута: %s (%s)", chosen.Name, chosen.IPNet))
	default:
		PrintInfo("несколько point-to-point интерфейсов в той же подсети:")
		for i, c := range candidates {
			fmt.Printf("  [%d] %s (%s)\n", i+1, c.Name, c.IPNet)
		}
		idx := parseIntOr(Ask("Который соответствует target SSTP? [1]", "1"), 1)
		if idx < 1 || idx > len(candidates) {
			idx = 1
		}
		chosen = candidates[idx-1]
	}

	yes := os.Getenv("WG_YES_TO_ALL") == "1"
	if !yes {
		resp := strings.ToLower(strings.TrimSpace(Ask(
			fmt.Sprintf("Добавить временный /32 маршрут к %s через %s? [Y/n]", targetHost, chosen.Name), "")))
		if resp != "" && resp != "y" && resp != "yes" && resp != "д" && resp != "да" {
			PrintWarn("маршрут не добавляю — если коннект упадёт, добавь руками: route ADD " + targetHost + " MASK 255.255.255.255 0.0.0.0 IF " + fmt.Sprint(chosen.Index) + " METRIC 1")
			return noop
		}
	}

	rollback, err := addHostRouteThroughInterface(targetHost, chosen.Index)
	if err != nil {
		PrintFail("route add failed: " + err.Error())
		return noop
	}
	PrintOK(fmt.Sprintf("временный /32-маршрут к %s через %s добавлен (METRIC 1)", targetHost, chosen.Name))
	return rollback
}

// describeCollision renders the collision into operator-readable text. Lists
// every candidate interface with name + subnet + role tag, so the operator
// can sanity-check before agreeing to a route fix.
func describeCollision(rc *routeCollision) string {
	if rc == nil {
		return ""
	}
	s := fmt.Sprintf("обнаружено %d интерфейса в подсети с %s:\n",
		len(rc.Local)+len(rc.PointToPt), rc.TargetIP)
	for _, m := range rc.Local {
		s += fmt.Sprintf("  • %s (%s)  — локальная сеть\n", m.Name, m.IPNet)
	}
	for _, m := range rc.PointToPt {
		s += fmt.Sprintf("  • %s (%s)  — point-to-point (вероятно SSTP/VPN)\n", m.Name, m.IPNet)
	}
	return s
}
