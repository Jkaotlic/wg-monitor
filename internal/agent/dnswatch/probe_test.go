package dnswatch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dohServer is a TLS DoH server (httptest's certificate is valid for
// example.com and 127.0.0.1) that answers every A query with 198.51.100.7 and
// records the SNI and Host it was reached with.
type dohServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	sni    []string
	hosts  []string
	status int
}

func newDoHServer(t *testing.T, status int) *dohServer {
	t.Helper()
	d := &dohServer{status: status}
	d.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.sni = append(d.sni, r.TLS.ServerName)
		d.hosts = append(d.hosts, r.Host)
		d.mu.Unlock()
		if d.status != http.StatusOK {
			w.WriteHeader(d.status)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var q dnsmessage.Message
		if err := q.Unpack(raw); err != nil || len(q.Questions) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: q.Header.ID, Response: true, RCode: dnsmessage.RCodeSuccess},
			Questions: q.Questions,
			Answers: []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.AResource{A: [4]byte{198, 51, 100, 7}},
			}},
		}
		out, _ := resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(out)
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *dohServer) port() string {
	u, _ := url.Parse(d.srv.URL)
	return u.Port()
}

func (d *dohServer) roots() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(d.srv.Certificate())
	return &tls.Config{RootCAs: pool}
}

type lookupRecorder struct {
	mu    sync.Mutex
	hosts []string
	addr  string
	err   error
}

func (l *lookupRecorder) lookup(_ context.Context, host string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hosts = append(l.hosts, host)
	return l.addr, l.err
}

// TestNetProber_DoHDialsResolvedAddressWithHostnameSNI: a DoH candidate's
// host is resolved by the agent itself (the router's resolver is dead by
// definition), dialled at that address, and spoken to with the real host name
// in SNI and Host — or the certificate check and virtual hosting would fail.
func TestNetProber_DoHDialsResolvedAddressWithHostnameSNI(t *testing.T) {
	d := newDoHServer(t, http.StatusOK)
	lk := &lookupRecorder{addr: "127.0.0.1"}
	p := &netProber{timeout: 2 * time.Second, tlsConfig: d.roots(), lookupFn: lk.lookup}

	line := "https upstream https://example.com:" + d.port() + "/dns-query"
	if err := p.ProbeCandidate(context.Background(), line, "example.com"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(lk.hosts) != 1 || lk.hosts[0] != "example.com" {
		t.Fatalf("lookups = %q, want [example.com]", lk.hosts)
	}
	if len(d.sni) != 1 || d.sni[0] != "example.com" || d.hosts[0] != "example.com:"+d.port() {
		t.Fatalf("server saw sni=%q host=%q", d.sni, d.hosts)
	}
}

// TestNetProber_OwnUsesBootstrapIPWithoutLookup: with bootstrap_ip set, the
// own endpoint's host is never resolved — it is dialled at that address.
func TestNetProber_OwnUsesBootstrapIPWithoutLookup(t *testing.T) {
	d := newDoHServer(t, http.StatusOK)
	lk := &lookupRecorder{err: errors.New("must not be called")}
	p := &netProber{timeout: 2 * time.Second, tlsConfig: d.roots(), lookupFn: lk.lookup}

	endpoint := "https://example.com:" + d.port() + "/secret-path"
	if err := p.ProbeOwn(context.Background(), endpoint, "example.com", "127.0.0.1"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(lk.hosts) != 0 {
		t.Fatalf("bootstrap_ip set, but host was looked up: %q", lk.hosts)
	}
	if d.sni[0] != "example.com" {
		t.Fatalf("sni = %q", d.sni)
	}
}

// TestNetProber_OwnErrorHidesSecretPath: the probe error is logged and may be
// reported; the endpoint path is a credential and must not be in it.
func TestNetProber_OwnErrorHidesSecretPath(t *testing.T) {
	d := newDoHServer(t, http.StatusBadGateway)
	p := &netProber{timeout: 2 * time.Second, tlsConfig: d.roots(), lookupFn: (&lookupRecorder{addr: "127.0.0.1"}).lookup}
	err := p.ProbeOwn(context.Background(), "https://example.com:"+d.port()+"/secret-path", "example.com", "")
	if err == nil {
		t.Fatal("502 must fail the probe")
	}
	if strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("error leaks the endpoint path: %v", err)
	}

	// Transport errors carry the full URL — masked too.
	p = &netProber{timeout: 500 * time.Millisecond, lookupFn: (&lookupRecorder{addr: "127.0.0.1"}).lookup}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	err = p.ProbeOwn(context.Background(), "https://example.com:"+port+"/secret-path", "example.com", "")
	if err == nil || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("transport error must fail and hide the path, got %v", err)
	}
}

// TestNetProber_DoTAddressAndSNI: a DoT candidate is dialled at its IP (a
// literal is used as-is, a name is resolved by the agent) on its port (853
// by default), with sni from the line or else the host name.
func TestNetProber_DoTAddressAndSNI(t *testing.T) {
	cases := []struct {
		line, wantAddr, wantSNI string
		wantLookup              bool
	}{
		{"tls upstream 1.1.1.1 sni cloudflare-dns.com", "1.1.1.1:853", "cloudflare-dns.com", false},
		{"tls upstream 9.9.9.9:853 sni dns.quad9.net", "9.9.9.9:853", "dns.quad9.net", false},
		{"tls upstream common.dot.dns.yandex.net", "203.0.113.5:853", "common.dot.dns.yandex.net", true},
		{"tls upstream common.dot.dns.yandex.net domain ru", "203.0.113.5:853", "common.dot.dns.yandex.net", true},
		{"tls upstream 198.51.100.53:8853 sni dns.example.com", "198.51.100.53:8853", "dns.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			lk := &lookupRecorder{addr: "203.0.113.5"}
			var gotAddr, gotSNI, gotDomain string
			p := &netProber{timeout: time.Second, lookupFn: lk.lookup,
				dotFn: func(_ context.Context, addr, sni, domain string, _ *tls.Config, _ time.Duration) ([]string, error) {
					gotAddr, gotSNI, gotDomain = addr, sni, domain
					return []string{"198.51.100.7"}, nil
				}}
			if err := p.ProbeCandidate(context.Background(), tc.line, "ya.ru"); err != nil {
				t.Fatalf("probe: %v", err)
			}
			if gotAddr != tc.wantAddr || gotSNI != tc.wantSNI || gotDomain != "ya.ru" {
				t.Errorf("dot(addr=%q sni=%q domain=%q), want (%q, %q, ya.ru)", gotAddr, gotSNI, gotDomain, tc.wantAddr, tc.wantSNI)
			}
			if (len(lk.hosts) > 0) != tc.wantLookup {
				t.Errorf("lookups = %q, want lookup=%v", lk.hosts, tc.wantLookup)
			}
		})
	}
}

// TestNetProber_LookupFailureUsesLastGoodAddress: a hiccup of the plain
// resolver must not read as "the own resolver is dead" — the last address
// that resolved is used.
func TestNetProber_LookupFailureUsesLastGoodAddress(t *testing.T) {
	lk := &lookupRecorder{addr: "203.0.113.5"}
	var addrs []string
	p := &netProber{timeout: time.Second, lookupFn: lk.lookup,
		systemLookupFn: (&lookupRecorder{err: errors.New("no such host")}).lookup,
		dotFn: func(_ context.Context, addr, _, _ string, _ *tls.Config, _ time.Duration) ([]string, error) {
			addrs = append(addrs, addr)
			return []string{"198.51.100.7"}, nil
		}}
	line := "tls upstream common.dot.dns.yandex.net"
	if err := p.ProbeCandidate(context.Background(), line, "ya.ru"); err != nil {
		t.Fatal(err)
	}
	lk.addr, lk.err = "", errors.New("i/o timeout")
	if err := p.ProbeCandidate(context.Background(), line, "ya.ru"); err != nil {
		t.Fatalf("second probe must reuse the last good address, got %v", err)
	}
	if len(addrs) != 2 || addrs[1] != "203.0.113.5:853" {
		t.Fatalf("dialled %q", addrs)
	}

	// A host that never resolved fails the probe.
	if err := p.ProbeCandidate(context.Background(), "tls upstream dot.example.com", "ya.ru"); err == nil {
		t.Fatal("unresolvable host must fail the probe")
	}
}

// TestNetProber_RejectsUnprobeableLines: a line that is not an https/tls
// upstream is a dead candidate, never guessed at.
func TestNetProber_RejectsUnprobeableLines(t *testing.T) {
	p := &netProber{timeout: time.Second, lookupFn: (&lookupRecorder{addr: "203.0.113.5"}).lookup,
		dotFn: func(context.Context, string, string, string, *tls.Config, time.Duration) ([]string, error) {
			t.Fatal("must not dial")
			return nil, nil
		}}
	for _, line := range []string{"", "rebind-protect auto", "tls upstream", "udp upstream 203.0.113.5", "https upstream http://dns.example.com/q", "https upstream https:///q"} {
		if err := p.ProbeCandidate(context.Background(), line, "example.com"); err == nil {
			t.Errorf("%q must be rejected", line)
		}
	}
}

// TestNetProber_FallsBackToSystemResolver: plain DNS to 77.88.8.8 unreachable
// and no address known yet (no bootstrap_ip) must not read as "the own
// resolver is dead" — that false positive would take a working resolver out.
// The router's own resolver answers through the system resolver when it is
// alive; when it is dead that lookup fails too, so the verdict stays right.
func TestNetProber_FallsBackToSystemResolver(t *testing.T) {
	d := newDoHServer(t, http.StatusOK)
	plain := &lookupRecorder{err: errors.New("i/o timeout")}
	sys := &lookupRecorder{addr: "127.0.0.1"}
	p := &netProber{timeout: 2 * time.Second, tlsConfig: d.roots(), lookupFn: plain.lookup, systemLookupFn: sys.lookup}
	if err := p.ProbeOwn(context.Background(), "https://example.com:"+d.port()+"/secret-path", "example.com", ""); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(plain.hosts) != 1 || len(sys.hosts) != 1 || sys.hosts[0] != "example.com" {
		t.Fatalf("plain lookups %q, system lookups %q — want plain first, then system", plain.hosts, sys.hosts)
	}

	// Plain DNS answering → the system resolver is never asked.
	sys.hosts = nil
	plain.addr, plain.err = "127.0.0.1", nil
	p.lastGood = nil
	if err := p.ProbeOwn(context.Background(), "https://example.com:"+d.port()+"/secret-path", "example.com", ""); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(sys.hosts) != 0 {
		t.Fatalf("system resolver asked although plain DNS answered: %q", sys.hosts)
	}
}
