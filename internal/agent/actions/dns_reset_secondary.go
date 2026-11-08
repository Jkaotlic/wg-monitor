package actions

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// This fixed mode adds redundancy without replacing the operator's DNS policy.
// There is deliberately no caller-controlled upstream or shell command.
const dnsSecondaryLine = "tls upstream 1.0.0.1 sni cloudflare-dns.com"

func dnsSecondaryKey(line string) string {
	f := strings.Fields(line)
	if len(f) >= 3 && f[0] == "tls" {
		if host, port, err := net.SplitHostPort(f[2]); err == nil && port == "853" {
			f[2] = host
		}
	}
	return strings.Join(f, " ")
}

func dnsAddSecondary(ctx context.Context, exec ExecFunc, opts DNSResetOpts) (string, string, wire.DNSResetResult) {
	var res wire.DNSResetResult
	rc, err := exec(ctx, "ndmc", "-c", "show running-config")
	if err != nil {
		return "err", "cannot read running-config; DNS unchanged", res
	}
	existing := parseDNSProxyUpstreams(string(rc))
	tlsCount := 0
	alreadyConfigured := false
	for _, line := range existing {
		if dnsSecondaryKey(line) == dnsSecondaryLine {
			alreadyConfigured = true
			continue
		}
		if strings.HasPrefix(line, "tls upstream ") {
			tlsCount++
		}
		if dnsServerID(line) == "1.0.0.1" {
			return "err", "1.0.0.1 already has different DNS settings; nothing changed", res
		}
	}
	if opts.DryRun && alreadyConfigured {
		return "ok", "Preview only; secondary DNS already configured; nothing changed", res
	}
	if !alreadyConfigured && tlsCount >= dnsref.KeeneticDoTLimit {
		return "err", "DoT upstream limit reached; nothing changed", res
	}
	if opts.Probe == nil {
		return "err", "secondary DNS probe unavailable; nothing changed", res
	}
	pctx, cancel := context.WithTimeout(ctx, dnsProbeTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- opts.Probe(pctx, dnsSecondaryLine, dnsProbeDomain(dnsref.PurposeForeign)) }()
	select {
	case err = <-done:
	case <-pctx.Done():
		err = pctx.Err()
	}
	probe := wire.DNSProbe{Server: "1.0.0.1", Purpose: string(dnsref.PurposeForeign), OK: err == nil}
	if err != nil {
		probe.Error = truncateProbeError(err.Error())
	}
	res.Probes = []wire.DNSProbe{probe}
	if err != nil {
		return "err", "secondary DNS probe failed; nothing changed: " + probe.Error, res
	}
	if opts.DryRun {
		return "ok", "Preview only; existing DNS unchanged. Add: dns-proxy " + dnsSecondaryLine, res
	}
	if opts.SnapshotDir == "" {
		return "err", "snapshot directory unavailable; nothing changed", res
	}
	snapshot, err := writeDNSSnapshot(opts.SnapshotDir, string(rc))
	if err != nil {
		return "err", fmt.Sprintf("snapshot failed; nothing changed: %v", err), res
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Add secondary DNS; preserve existing upstreams\nsnapshot: %s\n", snapshot)
	if !alreadyConfigured && !ndmcStep(ctx, exec, &b, "dns-proxy "+dnsSecondaryLine) {
		return "partial", b.String() + "DNS add failed; configuration not saved", res
	}
	after, err := exec(ctx, "ndmc", "-c", "show running-config")
	if err != nil {
		return "partial", b.String() + "cannot confirm running-config; configuration not saved", res
	}
	present := map[string]bool{}
	for _, line := range parseDNSProxyUpstreams(string(after)) {
		present[dnsSecondaryKey(line)] = true
	}
	for _, line := range append(existing, dnsSecondaryLine) {
		if !present[dnsSecondaryKey(line)] {
			return "partial", b.String() + "DNS preservation/addition not confirmed; configuration not saved", res
		}
	}
	b.WriteString("All previous upstreams and secondary DNS confirmed\n")
	if !ndmcStep(ctx, exec, &b, "system configuration save") {
		return "partial", b.String() + "configuration save failed", res
	}
	return "ok", b.String(), res
}
