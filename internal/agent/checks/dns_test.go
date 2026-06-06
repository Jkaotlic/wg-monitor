package checks

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/agent/keenetic"
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
