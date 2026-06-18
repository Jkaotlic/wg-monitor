package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// statusServer returns an httptest server that always responds with `code`.
func statusServer(code int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
}

// TestExternalReach_BotRejection4xxIsReachable proves that a service which
// answers our non-browser probe with a 4xx (e.g. Instagram/YouTube returning
// 403/429 to the "wg-monitor/external-reach" User-Agent) is treated as
// REACHABLE — the HTTP round-trip completed, so the tunnel path works.
// Previously any non-2xx/3xx counted as "unreachable" and produced false
// "Внешние сервисы недоступны через туннель" HARD alerts.
func TestExternalReach_BotRejection4xxIsReachable(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 429, 451} {
		srv := statusServer(code)
		c := ExternalReachCheck{
			Targets:         []ExternalReachTarget{{Name: "svc", URL: srv.URL}},
			FailThreshold:   1,
			PerProbeTimeout: 2 * time.Second,
		}
		got := c.Run(context.Background(), Deps{})
		srv.Close()
		if got.Status != "ok" {
			t.Fatalf("HTTP %d: expected reachable (status ok), got %s (details=%v)", code, got.Status, got.Details)
		}
	}
}

// TestExternalReach_2xx3xxReachable keeps the original happy path: a 204
// (YouTube generate_204) or a 200 is reachable.
func TestExternalReach_2xx3xxReachable(t *testing.T) {
	for _, code := range []int{200, 204, 301, 302} {
		srv := statusServer(code)
		c := ExternalReachCheck{
			Targets:         []ExternalReachTarget{{Name: "svc", URL: srv.URL}},
			FailThreshold:   1,
			PerProbeTimeout: 2 * time.Second,
		}
		got := c.Run(context.Background(), Deps{})
		srv.Close()
		if got.Status != "ok" {
			t.Fatalf("HTTP %d: expected reachable (status ok), got %s", code, got.Status)
		}
	}
}

// TestExternalReach_4xxRecordedAsDegradedNotFailed proves a reachable-but-403
// target is surfaced in targets_degraded (with its status code) instead of being
// silently lumped into targets_ok — and does not count toward the fail threshold.
func TestExternalReach_4xxRecordedAsDegradedNotFailed(t *testing.T) {
	ok200 := statusServer(200)
	defer ok200.Close()
	forbidden := statusServer(403)
	defer forbidden.Close()

	c := ExternalReachCheck{
		Targets: []ExternalReachTarget{
			{Name: "Telegram", URL: ok200.URL},
			{Name: "Instagram", URL: forbidden.URL},
		},
		FailThreshold:   1,
		PerProbeTimeout: 2 * time.Second,
	}
	got := c.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("403 must not fail the check: status=%s details=%v", got.Status, got.Details)
	}
	deg, _ := got.Details["targets_degraded"].([]map[string]any)
	if len(deg) != 1 {
		t.Fatalf("expected 1 degraded target, got %v (details=%v)", deg, got.Details)
	}
	if deg[0]["name"] != "Instagram" || deg[0]["status"] != 403 {
		t.Fatalf("degraded entry wrong: %+v", deg[0])
	}
	okNames, _ := got.Details["targets_ok"].([]string)
	if len(okNames) != 1 || okNames[0] != "Telegram" {
		t.Fatalf("clean ok list should hold only Telegram: %v", okNames)
	}
}

// TestExternalReach_5xxIsUnreachable proves a genuine server-side failure
// (5xx) still counts as unreachable so real outages are not masked.
func TestExternalReach_5xxIsUnreachable(t *testing.T) {
	for _, code := range []int{500, 502, 503} {
		srv := statusServer(code)
		c := ExternalReachCheck{
			Targets:         []ExternalReachTarget{{Name: "svc", URL: srv.URL}},
			FailThreshold:   1,
			PerProbeTimeout: 2 * time.Second,
		}
		got := c.Run(context.Background(), Deps{})
		srv.Close()
		if got.Status != "fail" {
			t.Fatalf("HTTP %d: expected fail, got %s", code, got.Status)
		}
	}
}

// TestExternalReach_TransportErrorIsUnreachable proves a dead endpoint
// (connection refused / blackholed — the RKN/CDN failure mode external_reach
// exists to catch) still fails.
func TestExternalReach_TransportErrorIsUnreachable(t *testing.T) {
	srv := statusServer(200)
	url := srv.URL
	srv.Close() // nothing listening now → connection refused
	c := ExternalReachCheck{
		Targets:         []ExternalReachTarget{{Name: "svc", URL: url}},
		FailThreshold:   1,
		PerProbeTimeout: 2 * time.Second,
	}
	got := c.Run(context.Background(), Deps{})
	if got.Status != "fail" {
		t.Fatalf("dead endpoint: expected fail, got %s", got.Status)
	}
}
