package actions

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeDNSExec serves a canned running-config for `show running-config` and
// records every other `ndmc -c "<cmd>"` invocation in order. Commands listed in
// failOn return an error so partial-failure handling can be exercised.
type fakeDNSExec struct {
	runningConfig string
	rcErr         error
	failOn        map[string]bool
	calls         []string
}

func (f *fakeDNSExec) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "ndmc" || len(args) != 2 || args[0] != "-c" {
		return nil, fmt.Errorf("unexpected exec: %s %v", name, args)
	}
	cmd := args[1]
	if cmd == "show running-config" {
		if f.rcErr != nil {
			return nil, f.rcErr
		}
		return []byte(f.runningConfig), nil
	}
	f.calls = append(f.calls, cmd)
	if f.failOn[cmd] {
		return []byte("ndmc: rejected"), fmt.Errorf("command rejected")
	}
	return nil, nil
}

const sampleRunningConfig = `! configuration
system
    hostname Keenetic
!
ip name-server 8.8.8.8 "" on Wireguard0
ip name-server 1.1.1.1 "" on ISP
dns-proxy
    tls upstream 8.8.8.8:853
    tls upstream 9.9.9.9:853 sni dns.quad9.net
    https upstream https://dns.google/dns-query
!
interface Wireguard0
    description vpn
    tls upstream 1.2.3.4:853
!
`

func wantReferenceAdds() []string {
	out := make([]string, 0, len(dnsReferenceUpstreams))
	for _, u := range dnsReferenceUpstreams {
		out = append(out, "dns-proxy "+u)
	}
	return out
}

func TestDNSResetRemovesExistingThenAppliesReferenceThenSaves(t *testing.T) {
	f := &fakeDNSExec{runningConfig: sampleRunningConfig}
	status, out := DNSReset(context.Background(), f.exec)

	if status != "ok" {
		t.Fatalf("status = %q, want ok\n%s", status, out)
	}

	var want []string
	// Removal is by identifier only (KeenOS rejects sni/domain/dnsm and the
	// `no dns-proxy …` prefix with `argument parse error`), default :853 dropped.
	want = append(want,
		"dns-proxy no tls upstream 8.8.8.8",
		"dns-proxy no tls upstream 9.9.9.9",
		"dns-proxy no https upstream https://dns.google/dns-query",
	)
	want = append(want, wantReferenceAdds()...)
	want = append(want, "system configuration save")

	if len(f.calls) != len(want) {
		t.Fatalf("got %d calls, want %d\ngot:  %v\nwant: %v", len(f.calls), len(want), f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, f.calls[i], want[i])
		}
	}

	// The per-interface name-servers are reported, never removed.
	for _, c := range f.calls {
		if strings.Contains(c, "ip name-server") {
			t.Errorf("name-server must not be touched, got call %q", c)
		}
	}
	if !strings.Contains(out, `ip name-server 8.8.8.8 "" on Wireguard0`) {
		t.Errorf("transcript should list untouched name-servers, got:\n%s", out)
	}
}

func TestDNSResetEmptyDNSProxyJustAppliesReference(t *testing.T) {
	f := &fakeDNSExec{runningConfig: "system\n    hostname Keenetic\n!\n"}
	status, out := DNSReset(context.Background(), f.exec)
	if status != "ok" {
		t.Fatalf("status = %q, want ok\n%s", status, out)
	}
	want := append(wantReferenceAdds(), "system configuration save")
	if len(f.calls) != len(want) {
		t.Fatalf("got %d calls, want %d\ngot: %v", len(f.calls), len(want), f.calls)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, f.calls[i], want[i])
		}
	}
	if !strings.Contains(out, "(none found)") {
		t.Errorf("expected (none found) marker, got:\n%s", out)
	}
}

func TestDNSResetPartialWhenCommandFails(t *testing.T) {
	f := &fakeDNSExec{
		runningConfig: sampleRunningConfig,
		failOn:        map[string]bool{"dns-proxy no tls upstream 8.8.8.8": true},
	}
	status, out := DNSReset(context.Background(), f.exec)
	if status != "partial" {
		t.Fatalf("status = %q, want partial\n%s", status, out)
	}
	if !strings.Contains(out, "✗ dns-proxy no tls upstream 8.8.8.8") {
		t.Errorf("expected failure marker in transcript, got:\n%s", out)
	}
	// A failed removal must not abort the rest: reference + save still run.
	if f.calls[len(f.calls)-1] != "system configuration save" {
		t.Errorf("save should still run after a failed removal, last call = %q", f.calls[len(f.calls)-1])
	}
}

func TestDNSResetReadConfigError(t *testing.T) {
	f := &fakeDNSExec{rcErr: fmt.Errorf("boom")}
	status, out := DNSReset(context.Background(), f.exec)
	if status != "err" {
		t.Fatalf("status = %q, want err", status)
	}
	if len(f.calls) != 0 {
		t.Errorf("no mutating commands should run when config read fails, got %v", f.calls)
	}
	if !strings.Contains(out, "read running-config failed") {
		t.Errorf("expected read-error message, got: %s", out)
	}
}

func TestParseDNSProxyUpstreams(t *testing.T) {
	cfg := `dns-proxy
    tls upstream 8.8.8.8:853
    tls upstream common.dot.dns.yandex.net domain ru
    https upstream https://dns.google/dns-query
    other-directive foo
!
interface Wireguard0
    tls upstream 9.9.9.9:853
!
`
	got := parseDNSProxyUpstreams(cfg)
	want := []string{
		"tls upstream 8.8.8.8:853",
		"tls upstream common.dot.dns.yandex.net domain ru",
		"https upstream https://dns.google/dns-query",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("upstream[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParsePlainNameServers(t *testing.T) {
	got := parsePlainNameServers(sampleRunningConfig)
	want := []string{
		`ip name-server 8.8.8.8 "" on Wireguard0`,
		`ip name-server 1.1.1.1 "" on ISP`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("name-server[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDNSProxyRemovalCommand pins the removal grammar verified on a live
// Keenetic: `dns-proxy no <tls|https> upstream <identifier>` — the identifier
// alone. Any sni/domain/dnsm qualifier, or the `no dns-proxy …` prefix, is an
// `argument parse error`. The default DoT port :853 is dropped so the
// identifier matches the verified `<IP-or-host>` form whether or not the
// router echoed the port; a non-default port is kept (it names a different
// upstream).
func TestDNSProxyRemovalCommand(t *testing.T) {
	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{"tls upstream 203.0.113.1:853 sni dns.example.com", "dns-proxy no tls upstream 203.0.113.1", true},
		{"tls upstream 203.0.113.1 sni dns.example.com", "dns-proxy no tls upstream 203.0.113.1", true},
		{"tls upstream 203.0.113.1:853", "dns-proxy no tls upstream 203.0.113.1", true},
		{"tls upstream 203.0.113.1:853 dnss", "dns-proxy no tls upstream 203.0.113.1", true},
		{"tls upstream dot.example.com:853 domain ru", "dns-proxy no tls upstream dot.example.com", true},
		{"tls upstream dot.example.com domain su", "dns-proxy no tls upstream dot.example.com", true},
		{"tls upstream 198.51.100.53:8853 sni dns.example.com", "dns-proxy no tls upstream 198.51.100.53:8853", true},
		{"https upstream https://dns.example.com/dns-query dnsm", "dns-proxy no https upstream https://dns.example.com/dns-query", true},
		{"https upstream https://dns.example.com/dns-query domain ru", "dns-proxy no https upstream https://dns.example.com/dns-query", true},
		{"https upstream https://dns.example.com:8443/q", "dns-proxy no https upstream https://dns.example.com:8443/q", true},
		{"    tls upstream 203.0.113.1:853   ", "dns-proxy no tls upstream 203.0.113.1", true},
		{"tls upstream", "", false},
		{"rebind-protect auto", "", false},
		{"udp upstream 203.0.113.1", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := dnsProxyRemovalCommand(tc.line)
		if ok != tc.ok || got != tc.want {
			t.Errorf("dnsProxyRemovalCommand(%q) = (%q, %v), want (%q, %v)", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

// recordAllNDMC wraps fakeDNSExec so that EVERY ndmc command — reads included —
// is visible to the test, not only the mutating ones fakeDNSExec records.
func recordAllNDMC(f *fakeDNSExec, every *[]string) ExecFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		*every = append(*every, strings.Join(args, " "))
		return f.exec(ctx, name, args...)
	}
}

// TestApplyDNSProxyUpstreamsNeverSaves: the DNS watchdog changes only the live
// (runtime) config. A saved config would survive a reboot and could leave the
// router on the fallback resolver for good, so `system configuration save`
// must never be issued here. Order: every removal first, then every add.
func TestApplyDNSProxyUpstreamsNeverSaves(t *testing.T) {
	f := &fakeDNSExec{runningConfig: sampleRunningConfig}
	var every []string
	remove := []string{
		"https upstream https://dns.example.com/secret-path dnsm",
		"tls upstream 198.51.100.53:853 sni dns.example.com",
	}
	add := []string{"tls upstream 203.0.113.53 sni dot.example.com"}

	status, out := ApplyDNSProxyUpstreams(context.Background(), recordAllNDMC(f, &every), remove, add)
	if status != "ok" {
		t.Fatalf("status = %q, want ok\n%s", status, out)
	}
	want := []string{
		"dns-proxy no https upstream https://dns.example.com/secret-path",
		"dns-proxy no tls upstream 198.51.100.53",
		// Adds keep their qualifiers: sni/domain are valid on add.
		"dns-proxy tls upstream 203.0.113.53 sni dot.example.com",
	}
	if len(f.calls) != len(want) {
		t.Fatalf("got %d calls, want %d\ngot:  %v\nwant: %v", len(f.calls), len(want), f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, f.calls[i], want[i])
		}
	}
	for _, c := range every {
		if strings.Contains(c, "configuration save") {
			t.Fatalf("runtime-only apply must never save the config, got ndmc %q (all: %v)", c, every)
		}
	}
}

// TestApplyDNSProxyUpstreamsRemovesEachIdentifierOnce: one
// `dns-proxy no tls upstream <host>` drops every domain-scoped line of that
// host, so a second removal of the same identifier would fail on a line that
// is already gone. Removals are deduplicated by identifier, first-seen order.
func TestApplyDNSProxyUpstreamsRemovesEachIdentifierOnce(t *testing.T) {
	f := &fakeDNSExec{}
	var every []string
	remove := []string{
		"tls upstream dot.example.com:853 domain ru",
		"tls upstream 203.0.113.9:853 sni dns.example.com",
		"tls upstream dot.example.com domain su",
		"tls upstream dot.example.com:853 domain xn--p1ai",
		"tls upstream 203.0.113.9 sni dns.example.com",
	}
	status, out := ApplyDNSProxyUpstreams(context.Background(), recordAllNDMC(f, &every), remove, nil)
	if status != "ok" {
		t.Fatalf("status = %q, want ok\n%s", status, out)
	}
	want := []string{
		"dns-proxy no tls upstream dot.example.com",
		"dns-proxy no tls upstream 203.0.113.9",
	}
	if len(f.calls) != len(want) {
		t.Fatalf("got %d calls, want %d\ngot:  %v\nwant: %v", len(f.calls), len(want), f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, f.calls[i], want[i])
		}
	}
}

// TestApplyDNSProxyUpstreamsUnparsableRemovalIsPartial: a line that is not a
// tls/https upstream cannot be turned into a valid removal. It must not be
// sent to ndmc in some guessed form; it is reported and makes the run partial.
func TestApplyDNSProxyUpstreamsUnparsableRemovalIsPartial(t *testing.T) {
	f := &fakeDNSExec{}
	var every []string
	remove := []string{"rebind-protect auto", "tls upstream 203.0.113.9:853"}
	status, out := ApplyDNSProxyUpstreams(context.Background(), recordAllNDMC(f, &every), remove, nil)
	if status != "partial" {
		t.Fatalf("status = %q, want partial\n%s", status, out)
	}
	if len(f.calls) != 1 || f.calls[0] != "dns-proxy no tls upstream 203.0.113.9" {
		t.Fatalf("only the parsable line may reach ndmc, got %v", f.calls)
	}
	if !strings.Contains(out, "✗") || !strings.Contains(out, "rebind-protect auto") {
		t.Errorf("unparsable line must be reported as failed, got:\n%s", out)
	}
}

func TestApplyDNSProxyUpstreamsPartialWhenCommandFails(t *testing.T) {
	remove := []string{"https upstream https://dns.example.com/secret-path dnsm"}
	add := []string{"tls upstream 203.0.113.53 sni dot.example.com"}
	f := &fakeDNSExec{failOn: map[string]bool{"dns-proxy no https upstream https://dns.example.com/secret-path": true}}
	var every []string

	status, out := ApplyDNSProxyUpstreams(context.Background(), recordAllNDMC(f, &every), remove, add)
	if status != "partial" {
		t.Fatalf("status = %q, want partial\n%s", status, out)
	}
	if !strings.Contains(out, "✗ dns-proxy no https upstream https://dns.example.com/secret-path") {
		t.Errorf("expected failure marker in transcript, got:\n%s", out)
	}
	// A failed removal must not abort the add, and still nothing is saved.
	if last := f.calls[len(f.calls)-1]; last != "dns-proxy "+add[0] {
		t.Errorf("add should still run after a failed removal, last call = %q", last)
	}
	for _, c := range every {
		if strings.Contains(c, "configuration save") {
			t.Fatalf("runtime-only apply must never save the config, got ndmc %q", c)
		}
	}
}

func TestApplyDNSProxyUpstreamsNothingToDo(t *testing.T) {
	f := &fakeDNSExec{}
	var every []string
	status, out := ApplyDNSProxyUpstreams(context.Background(), recordAllNDMC(f, &every), nil, nil)
	if status != "ok" {
		t.Fatalf("status = %q, want ok\n%s", status, out)
	}
	if len(every) != 0 {
		t.Fatalf("nothing to remove or add must mean no ndmc calls, got %v", every)
	}
}
