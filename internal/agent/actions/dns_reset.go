package actions

import (
	"context"
	"fmt"
	"strings"
)

// dnsReferenceUpstreams is the canonical DNS-over-TLS upstream set deployed by
// DNSReset. Each entry is the ndmc sub-command spliced after "dns-proxy", i.e.
// the agent runs `ndmc -c "dns-proxy <entry>"`. Mirrors the operator's reference
// recipe (Google / Quad9 / Cloudflare global + Yandex for ru/su/рф zones).
var dnsReferenceUpstreams = []string{
	"tls upstream 8.8.8.8 sni dns.google",
	"tls upstream 9.9.9.9 sni dns.quad9.net",
	"tls upstream 1.1.1.1 sni cloudflare-dns.com",
	"tls upstream common.dot.dns.yandex.net domain ru",
	"tls upstream common.dot.dns.yandex.net domain su",
	"tls upstream common.dot.dns.yandex.net domain xn--p1ai",
}

// DNSReset brings a Keenetic/NetCraze router's DNS-proxy to the reference
// DNS-over-TLS set: it wipes every existing `dns-proxy tls/https upstream`
// entry and re-applies dnsReferenceUpstreams, then persists with
// `system configuration save`. All ndmc commands run locally via exec — the
// agent is root in Entware, the same shell the operator would use by hand.
//
// Removal echoes each upstream back in the router's own canonical form
// (`no dns-proxy <line>` taken verbatim from `show running-config`), so it
// negates correctly regardless of the sni/domain/port qualifiers on the line —
// and re-running is idempotent (a second pass finds nothing to remove).
//
// Per-interface `ip name-server ... on <iface>` entries (typically created by
// tunnels) are left untouched but listed in the transcript: removing them here
// would just invite recreation on the next tunnel restart, so they belong in
// the tunnel config, not in this reset.
//
// The full transcript (every command + its ndmc output/error) is returned so a
// destructive DNS change is reviewed, not trusted. Status is "ok" when every
// command succeeded, "partial" when some failed (e.g. an upstream that would
// not negate), or "err" when the initial config read failed.
//
// DNSReset = the same removal/apply pass as ApplyDNSProxyUpstreams, plus the
// save. Only this manual reset persists; the DNS watchdog never does.
func DNSReset(ctx context.Context, exec ExecFunc) (status, output string) {
	rc, err := exec(ctx, "ndmc", "-c", "show running-config")
	if err != nil {
		return "err", fmt.Sprintf("read running-config failed: %v\n%s", err, strings.TrimSpace(string(rc)))
	}
	existing := parseDNSProxyUpstreams(string(rc))
	plain := parsePlainNameServers(string(rc))

	var b strings.Builder
	fmt.Fprintf(&b, "DNS reset → reference DoT\n\n")
	failures := applyDNSProxyUpstreams(ctx, exec, &b,
		"remove existing dns-proxy upstreams", existing,
		"apply reference upstreams", dnsReferenceUpstreams)

	b.WriteString("\nsave:\n")
	if !ndmcStep(ctx, exec, &b, "system configuration save") {
		failures++
	}

	if len(plain) > 0 {
		fmt.Fprintf(&b, "\nNOTE: %d per-interface name-server entr(y/ies) left untouched "+
			"(tunnel-managed — remove them from the tunnel configs so they are not recreated):\n", len(plain))
		for _, p := range plain {
			fmt.Fprintf(&b, "  • %s\n", p)
		}
	}

	if failures > 0 {
		fmt.Fprintf(&b, "\n%d command(s) failed — review above before relying on DNS.\n", failures)
		return "partial", b.String()
	}
	return "ok", b.String()
}

// ApplyDNSProxyUpstreams swaps dns-proxy upstreams in the router's LIVE config
// only: `no dns-proxy <line>` for each entry of remove, then
// `dns-proxy <entry>` for each entry of add, and nothing else — it never reads
// the config and NEVER runs `system configuration save`.
//
// Runtime-only is the whole point (DNS watchdog): a reboot must bring the
// router back to the saved, intended DNS setup (its own resolver first). A
// saved switch would outlive a night-time outage and strand the router on the
// fallback resolver for good.
//
// remove entries are passed through verbatim (the caller takes them from
// `show running-config`, same as DNSReset). A failed command does not stop the
// rest. Status is "ok" when every command succeeded, "partial" otherwise; the
// transcript lists every command with its ndmc output/error.
func ApplyDNSProxyUpstreams(ctx context.Context, exec ExecFunc, remove, add []string) (status string, output string) {
	var b strings.Builder
	b.WriteString("dns-proxy upstreams → live config only (not saved)\n\n")
	failures := applyDNSProxyUpstreams(ctx, exec, &b,
		"remove dns-proxy upstreams", remove,
		"apply dns-proxy upstreams", add)
	if failures > 0 {
		fmt.Fprintf(&b, "\n%d command(s) failed — review above before relying on DNS.\n", failures)
		return "partial", b.String()
	}
	return "ok", b.String()
}

// applyDNSProxyUpstreams is the removal/apply pass shared by DNSReset and
// ApplyDNSProxyUpstreams. It writes both sections to b and returns how many
// commands failed.
func applyDNSProxyUpstreams(ctx context.Context, exec ExecFunc, b *strings.Builder,
	removeTitle string, remove []string, addTitle string, add []string) (failures int) {
	fmt.Fprintf(b, "%s (%d):\n", removeTitle, len(remove))
	if len(remove) == 0 {
		b.WriteString("  (none found)\n")
	}
	for _, line := range remove {
		if !ndmcStep(ctx, exec, b, "no dns-proxy "+line) {
			failures++
		}
	}

	fmt.Fprintf(b, "\n%s (%d):\n", addTitle, len(add))
	for _, line := range add {
		if !ndmcStep(ctx, exec, b, "dns-proxy "+line) {
			failures++
		}
	}
	return failures
}

// ndmcStep runs one `ndmc -c <command>`, appends a ✓/✗ line plus any ndmc
// output to b, and reports whether the command succeeded.
func ndmcStep(ctx context.Context, exec ExecFunc, b *strings.Builder, command string) bool {
	out, runErr := exec(ctx, "ndmc", "-c", command)
	if runErr != nil {
		fmt.Fprintf(b, "  ✗ %s — %v\n", command, runErr)
	} else {
		fmt.Fprintf(b, "  ✓ %s\n", command)
	}
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		fmt.Fprintf(b, "      %s\n", strings.ReplaceAll(trimmed, "\n", "\n      "))
	}
	return runErr == nil
}

// parseDNSProxyUpstreams returns every `tls upstream …` / `https upstream …`
// line inside the top-level `dns-proxy` block of `ndmc show running-config`,
// trimmed to its canonical form (no indentation). Block tracking mirrors
// keenetic.ParseDNSEndpoints: the block opens on a bare top-level `dns-proxy`
// line and closes on a `!`. Form-agnostic on purpose — it captures the line
// verbatim so the caller can negate it as-is.
func parseDNSProxyUpstreams(cfg string) []string {
	var out []string
	in := false
	for _, raw := range strings.Split(cfg, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "dns-proxy" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			in = true
			continue
		}
		if !in {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "!" {
			in = false
			continue
		}
		if strings.HasPrefix(trimmed, "tls upstream ") || strings.HasPrefix(trimmed, "https upstream ") {
			out = append(out, trimmed)
		}
	}
	return out
}

// parsePlainNameServers returns every `ip name-server …` line (per-interface
// plain DNS), trimmed. These are reported but not removed by DNSReset.
func parsePlainNameServers(cfg string) []string {
	var out []string
	for _, raw := range strings.Split(cfg, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if strings.HasPrefix(trimmed, "ip name-server ") {
			out = append(out, trimmed)
		}
	}
	return out
}
