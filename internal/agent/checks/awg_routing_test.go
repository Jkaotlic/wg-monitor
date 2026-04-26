package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAwgRoutingMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("89.125.101.122"))
	}))
	defer srv.Close()

	chk := AwgRouting{Iface: "ignored", URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
	if got.Details["got_ip"] != "89.125.101.122" {
		t.Fatalf("details: %+v", got.Details)
	}
}

func TestAwgRoutingMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("1.2.3.4"))
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" || got.Details["got_ip"] != "1.2.3.4" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestAwgRoutingHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" {
		t.Fatalf("expected fail on 502, got %+v", got)
	}
}
