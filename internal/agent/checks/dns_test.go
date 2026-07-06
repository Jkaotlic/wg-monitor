package checks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"golang.org/x/net/dns/dnsmessage"
)

func TestDNS_AllOK_PlainOnly(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{1, 2, 3, 4})
	defer stop()
	host, port := splitHostPort(t, server)

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: host, Port: port}, // no NDMSName → use default dialer
		},
		TestDomain:    "example.com",
		FailThreshold: 1,
		IfaceDialFn:   func(_ string) *net.Dialer { return &net.Dialer{} },
	}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestDNS_DoHOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("dns")
		query, _ := base64.RawURLEncoding.DecodeString(raw)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(buildDoHResponse(t, query, dnsmessage.RCodeSuccess, [4]byte{1, 2, 3, 4}, true))
	}))
	defer srv.Close()

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "doh", URL: srv.URL},
		},
		TestDomain:    "example.com",
		FailThreshold: 1,
		HTTPClient:    srv.Client(),
	}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestDNS_AllFail_TriggersFail(t *testing.T) {
	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: "127.0.0.1", Port: 1},
			{Type: "plain", Host: "127.0.0.1", Port: 1},
		},
		TestDomain:      "example.com",
		FailThreshold:   1,
		IfaceDialFn:     func(_ string) *net.Dialer { return &net.Dialer{} },
		PerProbeTimeout: 100 * time.Millisecond,
	}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "fail" {
		t.Fatalf("expected fail, got %+v", got)
	}
	if got.Details["failed_count"] != 2 {
		t.Fatalf("failed_count=%v details=%+v", got.Details["failed_count"], got.Details)
	}
}

func TestDNS_PartialFailUnderThreshold(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{1, 2, 3, 4})
	defer stop()
	host, port := splitHostPort(t, server)

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: host, Port: port},     // ok
			{Type: "plain", Host: "127.0.0.1", Port: 1}, // fail
		},
		TestDomain:      "example.com",
		FailThreshold:   2,
		IfaceDialFn:     func(_ string) *net.Dialer { return &net.Dialer{} },
		PerProbeTimeout: 100 * time.Millisecond,
	}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("expected ok with 1/2 fail under threshold=2, got %+v", got)
	}
}

func TestDNS_NoEndpoints_ReturnsOK(t *testing.T) {
	chk := DNS{Endpoints: nil, TestDomain: "example.com", FailThreshold: 1}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("expected ok on empty endpoints, got %+v", got)
	}
	if got.Details["endpoints"] != 0 {
		t.Fatalf("details: %+v", got.Details)
	}
}

func TestDNS_RefreshesDiscoveredEndpointsOnEachRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("dns")
		query, _ := base64.RawURLEncoding.DecodeString(raw)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(buildDoHResponse(t, query, dnsmessage.RCodeSuccess, [4]byte{1, 2, 3, 4}, true))
	}))
	defer srv.Close()

	calls := 0
	chk := DNS{
		EndpointProvider: func(context.Context) ([]keenetic.DNSEndpoint, error) {
			calls++
			return []keenetic.DNSEndpoint{{Type: "doh", URL: srv.URL}}, nil
		},
		TestDomain:    "example.com",
		FailThreshold: 1,
		HTTPClient:    srv.Client(),
	}

	for i := 0; i < 2; i++ {
		got := chk.Run(context.Background(), Deps{})
		if got.Status != "ok" {
			t.Fatalf("run %d got %+v", i+1, got)
		}
	}
	if calls != 2 {
		t.Fatalf("endpoint provider calls = %d, want 2", calls)
	}
}

func TestDNS_RefreshesIfaceMapOnEachRun(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{1, 2, 3, 4})
	defer stop()
	host, port := splitHostPort(t, server)

	var dialedIface string
	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: host, Port: port, NDMSName: "Wireguard5"},
		},
		TestDomain:    "example.com",
		FailThreshold: 1,
		IfaceDialFn: func(iface string) *net.Dialer {
			dialedIface = iface
			return &net.Dialer{}
		},
		IfaceMap: map[string]string{"Wireguard5": "stale0"},
		IfaceMapProvider: func(context.Context) (map[string]string, error) {
			return map[string]string{"Wireguard5": "nwg5"}, nil
		},
	}

	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
	if dialedIface != "nwg5" {
		t.Fatalf("dialed iface = %q, want refreshed nwg5", dialedIface)
	}
}

func TestDNS_SkipsNDMSEndpointWhenRefreshedIfaceMapDoesNotContainTunnel(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{1, 2, 3, 4})
	defer stop()
	host, port := splitHostPort(t, server)

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: host, Port: port, NDMSName: "Wireguard5"},
			{Type: "plain", Host: "127.0.0.1", Port: 1, NDMSName: "Wireguard1"},
		},
		TestDomain:      "example.com",
		FailThreshold:   1,
		PerProbeTimeout: 100 * time.Millisecond,
		IfaceDialFn:     func(_ string) *net.Dialer { return &net.Dialer{} },
		IfaceMapProvider: func(context.Context) (map[string]string, error) {
			return map[string]string{"Wireguard5": "nwg5"}, nil
		},
	}

	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("stale missing NDMS endpoint should be skipped, got %+v", got)
	}
	if got.Details["skipped_count"] != 1 {
		t.Fatalf("skipped_count=%v details=%+v", got.Details["skipped_count"], got.Details)
	}
}

func splitHostPort(t *testing.T, hp string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(hp)
	if err != nil {
		t.Fatalf("SplitHostPort %q: %v", hp, err)
	}
	var port int
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	return host, port
}

// startSlowMockUDPDNS behaves like startMockUDPDNS but delays every reply by
// `delay`, replying to each incoming query on its own goroutine so the
// server itself never serializes concurrently-arriving queries — the only
// thing that can serialize probes is the DNS check's OWN client-side logic.
// Simulates a resolver that is merely slow, not down.
func startSlowMockUDPDNS(t *testing.T, answerIP [4]byte, delay time.Duration) (string, func()) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	stop := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-stop:
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			query := append([]byte(nil), buf[:n]...)
			go func(query []byte, raddr *net.UDPAddr) {
				select {
				case <-time.After(delay):
				case <-stop:
					return
				}
				var msg dnsmessage.Message
				if err := msg.Unpack(query); err != nil {
					return
				}
				resp := dnsmessage.Message{
					Header: dnsmessage.Header{
						ID:            msg.Header.ID,
						Response:      true,
						Authoritative: true,
					},
					Questions: msg.Questions,
					Answers: []dnsmessage.Resource{{
						Header: dnsmessage.ResourceHeader{
							Name:  msg.Questions[0].Name,
							Type:  dnsmessage.TypeA,
							Class: dnsmessage.ClassINET,
							TTL:   60,
						},
						Body: &dnsmessage.AResource{A: answerIP},
					}},
				}
				out, _ := resp.Pack()
				_, _ = conn.WriteToUDP(out, raddr)
			}(query, raddr)
		}
	}()
	return conn.LocalAddr().String(), func() { close(stop); conn.Close() }
}

// startBlackholeUDP returns a UDP endpoint that accepts queries but never
// replies, simulating a completely unresponsive resolver — used to force
// ProbePlainDNS to block until a deadline fires (the parent ctx's, in
// TestDNS_CanceledProbe_YieldsInconclusiveNotFailure below).
func startBlackholeUDP(t *testing.T) (string, func()) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	stop := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-stop:
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			_, _, _ = conn.ReadFromUDP(buf)
			// never reply
		}
	}()
	return conn.LocalAddr().String(), func() { close(stop); conn.Close() }
}

// TestDNS_ConcurrentProbing_CompletesWithinTightBudget is the Task A2 "(a)"
// test: a slow-but-not-down resolver, probed on 2 endpoints x (1
// reachability + 3 RKN domains) queries. Sequential worst case is
// 2*4*delay = 1.6s; the ctx budget below (1.2s) is comfortably under that,
// so this can only pass if endpoints AND RKN domains are actually probed
// concurrently rather than in the old sequential loop.
func TestDNS_ConcurrentProbing_CompletesWithinTightBudget(t *testing.T) {
	const delay = 250 * time.Millisecond
	srv1, stop1 := startSlowMockUDPDNS(t, [4]byte{1, 2, 3, 4}, delay)
	defer stop1()
	srv2, stop2 := startSlowMockUDPDNS(t, [4]byte{1, 2, 3, 5}, delay)
	defer stop2()
	host1, port1 := splitHostPort(t, srv1)
	host2, port2 := splitHostPort(t, srv2)

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: host1, Port: port1},
			{Type: "plain", Host: host2, Port: port2},
		},
		TestDomain:      "example.com",
		FailThreshold:   1,
		IfaceDialFn:     func(_ string) *net.Dialer { return &net.Dialer{} },
		PerProbeTimeout: 2 * time.Second,
		RKNTestDomains:  []string{"rutracker.org", "lostfilm.tv", "linkedin.com"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := chk.Run(ctx, Deps{})
	elapsed := time.Since(start)

	if elapsed > 1000*time.Millisecond {
		t.Fatalf("Run took %v; expected concurrent probing to finish well under the 1.2s budget (sequential worst case is 1.6s)", elapsed)
	}
	if got.Status != "ok" {
		t.Fatalf("expected ok, got %+v", got)
	}
	if got.Details["failed_count"] != 0 {
		t.Fatalf("expected no failures, got details=%+v", got.Details)
	}
	if got.Details["skipped_count"] != 0 {
		t.Fatalf("expected nothing skipped/inconclusive within budget, got details=%+v", got.Details)
	}
	if got.Details["rkn_probed"] != 2 || got.Details["rkn_suspect"] != 0 {
		t.Fatalf("expected both endpoints fully RKN-probed and clean, got details=%+v", got.Details)
	}
}

// TestDNS_CanceledProbe_YieldsInconclusiveNotFailure is the Task A2 "(b)"
// test: the parent ctx expires while a probe is blocked waiting on an
// unresponsive resolver. That must be classified as inconclusive/skipped,
// NOT counted toward failed_count or turned into a check failure.
func TestDNS_CanceledProbe_YieldsInconclusiveNotFailure(t *testing.T) {
	server, stop := startBlackholeUDP(t)
	defer stop()
	host, port := splitHostPort(t, server)

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: host, Port: port},
		},
		TestDomain:    "example.com",
		FailThreshold: 1,
		IfaceDialFn:   func(_ string) *net.Dialer { return &net.Dialer{} },
		// Deliberately much longer than the ctx budget below, so the OUTER
		// ctx — not this probe's own per-probe timeout — is what cuts the
		// probe short.
		PerProbeTimeout: 2 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	got := chk.Run(ctx, Deps{})
	if got.Status != "ok" {
		t.Fatalf("a canceled/timed-out probe must be inconclusive, not a check failure; got %+v", got)
	}
	if got.Details["failed_count"] != 0 {
		t.Fatalf("canceled probe must not increment failed_count, got details=%+v", got.Details)
	}
	if got.Details["skipped_count"] != 1 {
		t.Fatalf("canceled probe should be recorded as skipped/inconclusive (skipped_count=1), got details=%+v", got.Details)
	}
}

// TestDNS_RKNSpoofedAnswers_StillFails is the Task A2 "(c)" regression guard:
// every query (reachability AND all RKN-test domains) gets answered with a
// spoof IP, mirroring what RU ISPs return for RKN-blocked domains.
// Reachability must still succeed (the resolver DID answer); the RKN layer
// must still catch the spoofing and fail the check — parallelizing the
// per-domain probes must not weaken real detection.
func TestDNS_RKNSpoofedAnswers_StillFails(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{0, 0, 0, 0})
	defer stop()
	host, port := splitHostPort(t, server)

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: host, Port: port},
		},
		TestDomain:     "example.com",
		FailThreshold:  1,
		IfaceDialFn:    func(_ string) *net.Dialer { return &net.Dialer{} },
		RKNTestDomains: []string{"rutracker.org", "lostfilm.tv", "linkedin.com"},
	}

	got := chk.Run(context.Background(), Deps{})
	if got.Status != "fail" {
		t.Fatalf("expected fail: all RKN-test domains returned a spoof IP, got %+v", got)
	}
	if got.Details["rkn_probed"] != 1 || got.Details["rkn_suspect"] != 1 {
		t.Fatalf("expected rkn_probed=1 rkn_suspect=1, got details=%+v", got.Details)
	}
	if got.Details["failed_count"] != 0 {
		t.Fatalf("endpoint itself is reachable (answers, just spoofed) — failed_count must stay 0, got details=%+v", got.Details)
	}
}

// TestProbeInconclusive documents and pins the classification rule at the
// center of Task A2: checking err alone is NOT enough. ProbePlainDNS/ProbeDoH
// each enforce PerProbeTimeout via their own internally-derived child
// context, so an ordinary "this resolver never answered" failure produces
// the exact same context.DeadlineExceeded shape as an outer cancellation
// would. The only reliable signal is the state of the ctx the probe was
// actually handed.
func TestProbeInconclusive(t *testing.T) {
	genuineErr := errors.New("connection refused")
	// Shape produced by a probe's OWN PerProbeTimeout firing — unrelated to
	// the outer ctx's state.
	innerTimeoutErr := fmt.Errorf("read: %w", context.DeadlineExceeded)

	t.Run("nil error is never inconclusive", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if probeInconclusive(ctx, nil) {
			t.Fatal("nil error must not be inconclusive")
		}
	})

	t.Run("healthy outer ctx: genuine error counts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if probeInconclusive(ctx, genuineErr) {
			t.Fatal("genuine error under a healthy ctx must count as a real failure")
		}
	})

	t.Run("healthy outer ctx: inner-timeout-shaped error still counts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if probeInconclusive(ctx, innerTimeoutErr) {
			t.Fatal("a context.DeadlineExceeded-shaped error must NOT be inconclusive when the outer ctx is still healthy — it means the probe's OWN PerProbeTimeout fired, a genuine reachability failure")
		}
	})

	t.Run("outer ctx canceled: any error is inconclusive", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if !probeInconclusive(ctx, genuineErr) {
			t.Fatal("any error after the outer ctx is canceled must be inconclusive")
		}
	})

	t.Run("outer ctx deadline-exceeded: any error is inconclusive", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
		defer cancel()
		time.Sleep(5 * time.Millisecond)
		if !probeInconclusive(ctx, genuineErr) {
			t.Fatal("any error after the outer ctx deadline has passed must be inconclusive")
		}
	})
}
