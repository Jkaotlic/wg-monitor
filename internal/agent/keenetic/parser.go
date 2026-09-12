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
	SNI      string // dot: значение квалификатора `sni`; пусто — не задан
	Zone     string // dot/doh: значение квалификатора `domain`; пусто — глобальный апстрим
}

var (
	// ip name-server <IP> "<suffix>" on <NDMSName>
	rePlain = regexp.MustCompile(`^\s*ip\s+name-server\s+(\S+)\s+"([^"]*)"\s+on\s+(\S+)\s*$`)
	// inside `dns-proxy` block: https upstream <URL> [квалификаторы...]
	reDoH = regexp.MustCompile(`^\s*https\s+upstream\s+(\S+)(.*)$`)
	// inside `dns-proxy` block: tls upstream <host>[:<port>] [квалификаторы...]
	// Порт необязателен: KeenOS принимает и записывает строку без него, а
	// значение по умолчанию 853 уже подставляет проба (dns_dot.go:128-131).
	// Скобочная форма IPv6 стоит первой альтернативой: Go выбирает первую
	// подошедшую (leftmost-first), и `[^\s:]+` иначе откусил бы у `[2001:db8::1]`
	// только `[2001`.
	reDoT = regexp.MustCompile(`^\s*tls\s+upstream\s+(\[[^\]]+\]|[^\s:]+)(?::(\d+))?(.*)$`)
)

// parseQualifiers разбирает хвост строки dns-proxy: любое число токенов
// `<ключ> <значение>` плюс одиночные флаги вроде `dnsm`/`dnss`. Прежняя
// регулярка допускала максимум один хвостовой токен, и из-за этого все зонные
// строки и все строки с sni выпадали из разбора молча.
func parseQualifiers(tail string) (sni, zone string) {
	f := strings.Fields(tail)
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case "sni":
			if i+1 < len(f) {
				sni = f[i+1]
				i++
			}
		case "domain":
			if i+1 < len(f) {
				zone = f[i+1]
				i++
			}
		}
	}
	return sni, zone
}

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
			_, zone := parseQualifiers(m[2])
			out = append(out, DNSEndpoint{Type: "doh", URL: m[1], Zone: zone})
			continue
		}
		if m := reDoT.FindStringSubmatch(trimmed); m != nil {
			port := 853
			if m[2] != "" {
				p, err := strconv.Atoi(m[2])
				if err != nil || p < 1 || p > 65535 {
					continue
				}
				port = p
			}
			sni, zone := parseQualifiers(m[3])
			out = append(out, DNSEndpoint{Type: "dot", Host: m[1], Port: port, SNI: sni, Zone: zone})
		}
	}
	return out
}
