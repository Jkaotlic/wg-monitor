package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// reach -- каким был бы status без тревог: online | sleeping | offline по
// возрасту отчёта и порогам static/mobile. Рядом со stale; фронт берёт reach,
// если он есть.
func TestDashboardAgentReachIgnoresIncidents(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	policy := dashboardStatusPolicy{StaticStaleAfter: 5 * time.Minute, MobileStaleAfter: 30 * time.Minute, MobileOfflineAfter: 24 * time.Hour}
	inc := []dashboardIncident{{CheckName: "dns"}}
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	for _, c := range []struct {
		name string
		user db.User
		want string
	}{
		{"static fresh", db.User{LastSeenAt: ago(time.Minute)}, "online"},
		{"static silent", db.User{LastSeenAt: ago(time.Hour)}, "offline"},
		{"never seen", db.User{}, "offline"},
		{"mobile sleeping", db.User{Kind: db.KindMobile, LastSeenAt: ago(time.Hour)}, "sleeping"},
		{"mobile gone", db.User{Kind: db.KindMobile, LastSeenAt: ago(48 * time.Hour)}, "offline"},
	} {
		for _, incidents := range [][]dashboardIncident{nil, inc} {
			if got := dashboardAgentFromUser(c.user, incidents, now, policy).Reach; got != c.want {
				t.Errorf("%s (тревог %d): reach=%q, want %q", c.name, len(incidents), got, c.want)
			}
		}
	}
}

func TestReachFieldOnAllEndpoints(t *testing.T) {
	d, ownedID := seedSilentAlertRouter(t)
	h := NewMux(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: d, DashboardToken: "secret",
		TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	get := func(path string, auth func(*http.Request)) map[string]json.RawMessage {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		auth(req)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &raw)
		return raw
	}
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret") }
	admin := func(r *http.Request) { r.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999)) }
	reachOf := func(list json.RawMessage, key string) string {
		var rows []map[string]any
		_ = json.Unmarshal(list, &rows)
		for _, r := range rows {
			if r[key] == "router-owned" {
				s, _ := r["reach"].(string)
				return s
			}
		}
		return "<нет строки>"
	}
	if got := reachOf(get("/v1/dashboard/summary", bearer)["agents"], "nickname"); got != "offline" {
		t.Errorf("сводка: reach=%q", got)
	}
	if got := reachOf(get("/v1/miniapp/routers", admin)["routers"], "nickname"); got != "offline" {
		t.Errorf("список: reach=%q", got)
	}
	if got := reachOf(get("/v1/miniapp/fleet", admin)["routers"], "nickname"); got != "offline" {
		t.Errorf("парк: reach=%q", got)
	}
	var router map[string]any
	_ = json.Unmarshal(get(fmt.Sprintf("/v1/miniapp/routers/%d", ownedID), admin)["router"], &router)
	if router["reach"] != "offline" || router["status"] != "alert" {
		t.Errorf("экран роутера: reach=%v status=%v", router["reach"], router["status"])
	}
}

// «Не на связи» в строке «Парка», когда строки роутера нет (сбой чтения
// users), -- по reach, а не по status: тревога не делает роутер доступным.
func TestFleetAwayFromReach(t *testing.T) {
	if !fleetAwayFromSummary(dashboardSummaryAgent{Status: "alert", Reach: "offline"}) {
		t.Fatal("молчащий роутер с тревогой -- не на связи")
	}
	if fleetAwayFromSummary(dashboardSummaryAgent{Status: "alert", Reach: "online"}) {
		t.Fatal("живой роутер с тревогой -- на связи")
	}
	if !fleetAwayFromSummary(dashboardSummaryAgent{Status: "sleeping", Reach: "sleeping"}) {
		t.Fatal("спящий -- не на связи")
	}
}
