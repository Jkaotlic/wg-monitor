package actions

import (
	"net/netip"
	"strings"
	"unicode"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func normalizeRouteTarget(raw string) wire.RouteTarget {
	value := strings.TrimSpace(raw)
	if value == "" {
		return wire.RouteTarget{Type: "opaque", Value: value}
	}

	if prefix, err := netip.ParsePrefix(value); err == nil {
		return wire.RouteTarget{Type: "cidr", Value: prefix.Masked().String()}
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return wire.RouteTarget{Type: "cidr", Value: netip.PrefixFrom(addr, addr.BitLen()).String()}
	}

	domain := strings.TrimRight(strings.ToLower(value), ".")
	ascii, err := domainToASCII(domain)
	if err != nil || !isComparableDomain(ascii) {
		return wire.RouteTarget{Type: "opaque", Value: value}
	}
	return wire.RouteTarget{Type: "domain", Value: ascii}
}

func classifyRouteOverlaps(candidate wire.RouteRuleSummary, existing []wire.RouteRuleSummary) []wire.RouteOverlap {
	candidate = normalizeRouteRuleSummary(candidate)
	var overlaps []wire.RouteOverlap
	for _, target := range candidate.Targets {
		if target.Type == "opaque" {
			overlaps = append(overlaps, wire.RouteOverlap{
				Severity: "info",
				Reason:   "target cannot be safely compared",
				Target:   target,
			})
			continue
		}
		for _, rule := range existing {
			normalizedRule := normalizeRouteRuleSummary(rule)
			for _, existingTarget := range normalizedRule.Targets {
				if !routeTargetsOverlap(target, existingTarget) {
					continue
				}
				severity := "block"
				reason := "target overlaps existing route bound to a different destination"
				if !normalizedRule.Enabled {
					severity = "warn"
					reason = "target overlaps disabled existing route"
				} else if normalizedRule.Bind == candidate.Bind {
					severity = "warn"
					reason = "target overlaps existing route bound to the same destination"
				}
				overlaps = append(overlaps, wire.RouteOverlap{
					Severity: severity,
					Reason:   reason,
					Existing: normalizedRule,
					Target:   target,
				})
			}
		}
	}
	return overlaps
}

func normalizeRouteRuleSummary(rule wire.RouteRuleSummary) wire.RouteRuleSummary {
	out := rule
	out.Targets = make([]wire.RouteTarget, 0, len(rule.Targets))
	for _, target := range rule.Targets {
		raw := target.Value
		if raw == "" {
			raw = target.Type
		}
		out.Targets = append(out.Targets, normalizeRouteTarget(raw))
	}
	return out
}

func routeTargetsOverlap(a, b wire.RouteTarget) bool {
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case "domain":
		return domainsOverlap(a.Value, b.Value)
	case "cidr":
		return cidrsOverlap(a.Value, b.Value)
	default:
		return false
	}
}

func domainsOverlap(a, b string) bool {
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

func cidrsOverlap(a, b string) bool {
	ap, err := netip.ParsePrefix(a)
	if err != nil {
		return false
	}
	bp, err := netip.ParsePrefix(b)
	if err != nil {
		return false
	}
	if ap.Addr().Is4() != bp.Addr().Is4() {
		return false
	}
	return ap.Contains(bp.Addr()) || bp.Contains(ap.Addr())
}

func isComparableDomain(domain string) bool {
	if domain == "" || len(domain) > 253 {
		return false
	}
	if strings.ContainsAny(domain, "/:*?[]\\") {
		return false
	}
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
				return false
			}
		}
	}
	return true
}

func domainToASCII(domain string) (string, error) {
	labels := strings.Split(domain, ".")
	for i, label := range labels {
		if label == "" {
			return "", errInvalidDomain
		}
		asciiOnly := true
		for _, r := range label {
			if r > unicode.MaxASCII {
				asciiOnly = false
				break
			}
		}
		if asciiOnly {
			continue
		}
		encoded, err := punycodeEncode(label)
		if err != nil {
			return "", err
		}
		labels[i] = "xn--" + encoded
	}
	return strings.Join(labels, "."), nil
}

var errInvalidDomain = domainError("invalid domain")

type domainError string

func (e domainError) Error() string { return string(e) }

func punycodeEncode(label string) (string, error) {
	const (
		base        = 36
		tmin        = 1
		tmax        = 26
		initialBias = 72
		initialN    = 128
		delimiter   = '-'
	)

	var out []rune
	input := []rune(label)
	for _, r := range input {
		if r < 0x80 {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
				return "", errInvalidDomain
			}
			out = append(out, r)
		}
	}
	basicLen := len(out)
	handled := basicLen
	if basicLen > 0 {
		out = append(out, delimiter)
	}

	n := initialN
	delta := 0
	bias := initialBias
	for handled < len(input) {
		m := int(^uint(0) >> 1)
		for _, r := range input {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		if m == int(^uint(0)>>1) {
			return "", errInvalidDomain
		}
		delta += (m - n) * (handled + 1)
		n = m
		for _, r := range input {
			rr := int(r)
			if rr < n {
				delta++
				continue
			}
			if rr != n {
				continue
			}
			q := delta
			for k := base; ; k += base {
				t := k - bias
				if t < tmin {
					t = tmin
				} else if t > tmax {
					t = tmax
				}
				if q < t {
					break
				}
				out = append(out, encodePunycodeDigit(t+(q-t)%(base-t)))
				q = (q - t) / (base - t)
			}
			out = append(out, encodePunycodeDigit(q))
			bias = adaptPunycodeBias(delta, handled+1, handled == basicLen)
			delta = 0
			handled++
		}
		delta++
		n++
	}
	return string(out), nil
}

func adaptPunycodeBias(delta, numPoints int, firstTime bool) int {
	const (
		base = 36
		tmin = 1
		tmax = 26
		skew = 38
		damp = 700
	)
	if firstTime {
		delta /= damp
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > ((base-tmin)*tmax)/2 {
		delta /= base - tmin
		k += base
	}
	return k + ((base-tmin+1)*delta)/(delta+skew)
}

func encodePunycodeDigit(digit int) rune {
	if digit < 26 {
		return rune('a' + digit)
	}
	return rune('0' + digit - 26)
}
