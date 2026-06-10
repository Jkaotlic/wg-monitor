package backend

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestDashboardRoutesAbsentWhenTokenEmpty(t *testing.T) {
	h := NewMux(Deps{})
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardAuth_MissingHeader_401(t *testing.T) {
	h := DashboardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestDashboardAuth_WrongToken_401(t *testing.T) {
	h := DashboardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestDashboardAuth_RightToken_200(t *testing.T) {
	h := DashboardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestDashboardSummaryRouteRequiresBearerToken(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := NewMux(Deps{DB: d, DashboardToken: "secret"})

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: want 401, got %d", missing.Code)
	}

	okReq := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	okReq.Header.Set("Authorization", "Bearer secret")
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, okReq)
	if ok.Code != http.StatusOK {
		t.Fatalf("authorized: want 200, got %d body=%s", ok.Code, ok.Body.String())
	}
}

func TestDashboardSummaryIncludesFleetCounts(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	onlineID, err := d.Users().Insert("bronya", "token-a", "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeen(onlineID); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(onlineID, "v0.13.0"); err != nil {
		t.Fatal(err)
	}

	alertID, err := d.Users().InsertWithKind("alyaba", "token-b", "2.2.2.2", "awg1", db.KindMobile)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().MarkPendingDeploy(alertID, "v0.13.1", "2026-06-10T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	hardSince := time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC)
	if err := d.State().Save(alertID, "agent_heartbeat", db.IncidentState{
		UserID:           alertID,
		CheckName:        "agent_heartbeat",
		CurrentStatus:    "hard",
		ConsecutiveFails: 4,
		HardSince:        &hardSince,
	}); err != nil {
		t.Fatal(err)
	}

	h := NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d,
		DashboardToken: "secret",
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Totals struct {
			Agents         int `json:"agents"`
			Online         int `json:"online"`
			Offline        int `json:"offline"`
			Alerts         int `json:"alerts"`
			PendingDeploys int `json:"pending_deploys"`
		} `json:"totals"`
		Agents []struct {
			Nickname        string `json:"nickname"`
			Kind            string `json:"kind"`
			Status          string `json:"status"`
			AgentVersion    string `json:"agent_version"`
			PendingVersion  string `json:"pending_version"`
			ActiveIncidents []struct {
				CheckName string `json:"check_name"`
				FailCount int    `json:"fail_count"`
			} `json:"active_incidents"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Totals.Agents != 2 || got.Totals.Online != 1 || got.Totals.Alerts != 1 || got.Totals.PendingDeploys != 1 {
		t.Fatalf("totals=%+v", got.Totals)
	}
	if got.Totals.Offline != 0 {
		t.Fatalf("offline=%d want 0 because hard incident is counted as alert", got.Totals.Offline)
	}
	if len(got.Agents) != 2 {
		t.Fatalf("agents len=%d", len(got.Agents))
	}
	if got.Agents[0].Nickname != "bronya" || got.Agents[0].Status != "online" || got.Agents[0].AgentVersion != "v0.13.0" {
		t.Fatalf("first agent=%+v", got.Agents[0])
	}
	if got.Agents[1].Nickname != "alyaba" || got.Agents[1].Kind != db.KindMobile || got.Agents[1].Status != "alert" {
		t.Fatalf("second agent=%+v", got.Agents[1])
	}
	if got.Agents[1].PendingVersion != "v0.13.1" {
		t.Fatalf("pending_version=%q", got.Agents[1].PendingVersion)
	}
	if len(got.Agents[1].ActiveIncidents) != 1 || got.Agents[1].ActiveIncidents[0].CheckName != "agent_heartbeat" || got.Agents[1].ActiveIncidents[0].FailCount != 4 {
		t.Fatalf("incidents=%+v", got.Agents[1].ActiveIncidents)
	}
}
