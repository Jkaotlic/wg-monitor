package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/agent/keenetic"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dohJSONOK))
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
	failed, _ := got.Details["failed"].([]map[string]any)
	if len(failed) != 2 {
		t.Fatalf("failed=%+v", failed)
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
