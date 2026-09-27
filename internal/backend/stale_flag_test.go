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

// MINI-01: молчащий роутер с открытой тревогой не должен выглядеть живым.
// status остаётся "alert" (контракт), а отдельный stale говорит, что отчёт
// устарел -- по тем же порогам, что offline/sleeping, независимо от тревог.
func TestDashboardAgentStaleIndependentOfIncidents(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	policy := dashboardStatusPolicy{StaticStaleAfter: 5 * time.Minute, MobileStaleAfter: 30 * time.Minute, MobileOfflineAfter: 24 * time.Hour}
	inc := []dashboardIncident{{CheckName: "agent_heartbeat"}}
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	cases := []struct {
		name      string
		user      db.User
		incidents []dashboardIncident
		stale     bool
		status    string
	}{
		{"static fresh", db.User{LastSeenAt: ago(time.Minute)}, nil, false, "online"},
		{"static silent with alert", db.User{LastSeenAt: ago(47 * time.Hour)}, inc, true, "alert"},
		{"static silent no alert", db.User{LastSeenAt: ago(10 * time.Minute)}, nil, true, "offline"},
		{"static fresh with alert", db.User{LastSeenAt: ago(time.Minute)}, inc, false, "alert"},
		{"never seen with alert", db.User{}, inc, true, "alert"},
		{"mobile sleeping with alert", db.User{Kind: db.KindMobile, LastSeenAt: ago(time.Hour)}, inc, true, "alert"},
		{"mobile fresh", db.User{Kind: db.KindMobile, LastSeenAt: ago(10 * time.Minute)}, nil, false, "online"},
	}
	for _, c := range cases {
		a := dashboardAgentFromUser(c.user, c.incidents, now, policy)
		if a.Stale != c.stale || a.Status != c.status {
			t.Errorf("%s: stale=%v status=%q, want stale=%v status=%q", c.name, a.Stale, a.Status, c.stale, c.status)
		}
	}
}

func seedSilentAlertRouter(t *testing.T) (*db.DB, int64) {
	t.Helper()
	d, ownedID, _, _ := seedMiniappFleet(t)
	setDashboardTestLastSeen(t, d, ownedID, time.Now().Add(-47*time.Hour))
	hardSince := time.Now().Add(-46 * time.Hour).UTC()
	if err := d.State().Save(ownedID, "agent_heartbeat", db.IncidentState{
		UserID: ownedID, CheckName: "agent_heartbeat", CurrentStatus: "hard", ConsecutiveFails: 4, HardSince: &hardSince,
	}); err != nil {
		t.Fatal(err)
	}
	return d, ownedID
}

func TestMiniappRoutersCarryStaleFlag(t *testing.T) {
	d, ownedID := seedSilentAlertRouter(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	for _, path := range []string{"/v1/miniapp/routers", fmt.Sprintf("/v1/miniapp/routers/%d", ownedID)} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 100))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		var row map[string]any
		if list, ok := raw["routers"]; ok {
			var rows []map[string]any
			_ = json.Unmarshal(list, &rows)
			if len(rows) != 1 {
				t.Fatalf("rows=%v", rows)
			}
			row = rows[0]
		} else {
			_ = json.Unmarshal(raw["router"], &row)
		}
		if row["status"] != "alert" || row["stale"] != true {
			t.Fatalf("%s: status=%v stale=%v, want alert + stale=true", path, row["status"], row["stale"])
		}
	}
}

func TestDashboardSummaryCarriesStaleFlag(t *testing.T) {
	d, _ := seedSilentAlertRouter(t)
	h := NewMux(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: d, DashboardToken: "secret"})
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	stale := map[string]any{}
	for _, a := range got.Agents {
		stale[a["nickname"].(string)] = a["stale"]
	}
	// router-other никогда не отчитывался -- тоже устарел, и поле едет всегда
	// (без omitempty), чтобы фронт не путал «нет поля» с «свежий».
	if stale["router-owned"] != true || stale["router-other"] != true {
		t.Fatalf("stale=%v", stale)
	}
}

// Строки «Парка» несут тот же stale, что сводка.
func TestMiniappFleetRowsCarryStaleFlag(t *testing.T) {
	d, _ := seedSilentAlertRouter(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	rec := fleetRequest(t, h, 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var raw struct {
		Routers []map[string]any `json:"routers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, r := range raw.Routers {
		got[r["nickname"].(string)] = r["stale"]
	}
	if got["router-owned"] != true || got["router-other"] != true {
		t.Fatalf("stale=%v", got)
	}
}
