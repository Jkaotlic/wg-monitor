package keenetic

import (
	"regexp"
	"strconv"
	"strings"
)

// DNSEndpoint represents one DNS resolver discovered from the Keenetic NDM
// running-config. Plain DNS is per-interface (NDMSName is set); DoH/DoT are
// global to the dns-proxy and have empty NDMSName.
type DNSEndpoint struct {
	Type     string // "plain", "doh", "dot"
	Host     string // for plain and dot
	Port     int    // for plain (default 53) and dot (typically 853)
	URL      string // for doh
	NDMSName string // NDM-side iface name (e.g. "Wireguard0"); empty for global
}

var (
	// ip name-server <IP> "<suffix>" on <NDMSName>
	rePlain = regexp.MustCompile(`^\s*ip\s+name-server\s+(\S+)\s+"([^"]*)"\s+on\s+(\S+)\s*$`)
	// inside `dns-proxy` block:  https upstream <URL> dnsm
	reDoH = regexp.MustCompile(`^\s*https\s+upstream\s+(\S+)(?:\s+(\S+))?\s*$`)
	// inside `dns-proxy` block:  tls upstream <host>:<port> dnss?
	reDoT = regexp.MustCompile(`^\s*tls\s+upstream\s+(\S+):(\d+)(?:\s+(\S+))?\s*$`)
)

// ParseDNSEndpoints walks `ndmc show running-config` output and returns all
// DNS endpoints in source order: per-interface plain entries first (matching
// the visual order on KeeneticOS web-UI DNS panel), then global DoH/DoT.
//
// Block tracking: `https upstream` and `tls upstream` are only valid inside a
// top-level `dns-proxy` block, terminated by a `!` on its own line.
func ParseDNSEndpoints(cfg string) []DNSEndpoint {
	var out []DNSEndpoint
	inDNSProxy := false
	for _, line := range strings.Split(cfg, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		// Block enter/exit
		if strings.TrimSpace(trimmed) == "dns-proxy" && !strings.HasPrefix(trimmed, " ") {
			inDNSProxy = true
			continue
		}
		if inDNSProxy && strings.TrimSpace(trimmed) == "!" {
			inDNSProxy = false
			continue
		}

		// Plain name-server (anywhere)
		if m := rePlain.FindStringSubmatch(trimmed); m != nil {
			out = append(out, DNSEndpoint{
				Type:     "plain",
				Host:     m[1],
				Port:     53,
				NDMSName: m[3],
			})
			continue
		}

		if !inDNSProxy {
			continue
		}

		if m := reDoH.FindStringSubmatch(trimmed); m != nil {
			out = append(out, DNSEndpoint{Type: "doh", URL: m[1]})
			continue
		}
		if m := reDoT.FindStringSubmatch(trimmed); m != nil {
			port, err := strconv.Atoi(m[2])
			if err != nil || port < 1 || port > 65535 {
				continue
			}
			out = append(out, DNSEndpoint{Type: "dot", Host: m[1], Port: port})
		}
	}
	return out
}
