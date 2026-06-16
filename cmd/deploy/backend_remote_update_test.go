package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBackendUpdateTimerUsesLessAggressiveInterval(t *testing.T) {
	got := backendUpdateTimer("wg-monitor-backend-update.service")
	if !strings.Contains(got, "OnUnitActiveSec=2min") {
		t.Fatalf("timer should poll at 2min interval, got:\n%s", got)
	}
	if strings.Contains(got, "OnUnitActiveSec=20s") {
		t.Fatalf("timer must not keep the old 20s tight loop:\n%s", got)
	}
}

func TestDecodeBackendHealthVersion(t *testing.T) {
	got, err := decodeBackendHealthVersion(strings.NewReader(`{"version":"v0.13.0-rc132"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "v0.13.0-rc132" {
		t.Fatalf("version = %q", got)
	}
}

func TestDecodeBackendHealthVersionRejectsOversizeResponse(t *testing.T) {
	_, err := decodeBackendHealthVersion(strings.NewReader(strings.Repeat("A", int(maxBackendHealthResponseBytes)+1)))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want oversize response error, got %v", err)
	}
}

func TestBackendHealthClientForStateUsesExplicitAPIDialHost(t *testing.T) {
	state := &State{Backend: BackendState{Domain: "wgmonitor.example", APIDialHost: "83.171.224.125"}}
	base, client, err := backendHealthClientForState(state, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://wgmonitor.example" {
		t.Fatalf("base=%q", base)
	}
	if client.Timeout != 5*time.Second {
		t.Fatalf("timeout=%s", client.Timeout)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil || tr.DialContext == nil {
		t.Fatalf("health client must use proxy-free pinned transport, got %#v", client.Transport)
	}
}

func TestWaitBackendVersionWithClientUsesProvidedClient(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://wgmonitor.example/healthz" {
			t.Fatalf("url=%q", r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"version":"v0.13.0-rc132"}`)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
	if err := waitBackendVersionWithClient(client, "https://wgmonitor.example", "v0.13.0-rc132", time.Second); err != nil {
		t.Fatal(err)
	}
}
