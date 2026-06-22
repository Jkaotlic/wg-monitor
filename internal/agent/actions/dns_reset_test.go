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
	want = append(want,
		"no dns-proxy tls upstream 8.8.8.8:853",
		"no dns-proxy tls upstream 9.9.9.9:853 sni dns.quad9.net",
		"no dns-proxy https upstream https://dns.google/dns-query",
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
		failOn:        map[string]bool{"no dns-proxy tls upstream 8.8.8.8:853": true},
	}
	status, out := DNSReset(context.Background(), f.exec)
	if status != "partial" {
		t.Fatalf("status = %q, want partial\n%s", status, out)
	}
	if !strings.Contains(out, "✗ no dns-proxy tls upstream 8.8.8.8:853") {
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
