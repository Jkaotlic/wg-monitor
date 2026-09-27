package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Агенты v0.46 шлют «не смог проверить» как ok с details.unverified=true.
// Это не наблюдение: строку храним, автомат тревог не двигаем, мини-аппу --
// статус "unknown" («не проверено»).

const unverifiedTok = "7777777777777777777777777777777777777777777777777777777777777777"

func unverifiedEnv(t *testing.T) (*db.DB, int64, *fakeDisp, http.Handler) {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	uid, _ := d.Users().Insert("unverified-owl", unverifiedTok, "198.51.100.40", "awg0")
	if err := d.Users().SetTelegramUserID(uid, 100); err != nil {
		t.Fatal(err)
	}
	disp := &fakeDisp{db: d}
	h := NewMux(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: d, Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2}, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	return d, uid, disp, h
}

func postUnverifiedReport(t *testing.T, h http.Handler, ts time.Time, checks []wire.Check) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(wire.Report{Timestamp: ts, Checks: checks})
	req := httptest.NewRequest(http.MethodPost, "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+unverifiedTok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Статус "unknown" с провода не роняет весь отчёт (422) и тем более
// heartbeat: он приводится к ok+unverified.
func TestReportUnknownStatusNeverDropsHeartbeat(t *testing.T) {
	d, uid, _, h := unverifiedEnv(t)
	rec := postUnverifiedReport(t, h, time.Now().UTC(), []wire.Check{
		{Name: "agent_heartbeat", Status: "ok"}, {Name: "dns", Status: "unknown"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("отчёт отвергнут: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := d.Events().LatestEvent(uid, "agent_heartbeat"); !ok {
		t.Fatal("heartbeat потерян")
	}
	row, ok, _ := d.Events().LatestEvent(uid, "dns")
	if !ok || row.Status != "ok" {
		t.Fatalf("строка dns: %+v ok=%v", row, ok)
	}
	var det map[string]any
	_ = json.Unmarshal([]byte(row.DetailsJSON), &det)
	if det["unverified"] != true {
		t.Fatalf("unverified не записан: %s", row.DetailsJSON)
	}
}

// Непроверенное ok не закрывает открытую тревогу и не двигает автомат.
func TestUnverifiedCheckDoesNotMoveFSM(t *testing.T) {
	d, uid, disp, h := unverifiedEnv(t)
	hard := time.Now().UTC().Add(-time.Hour)
	if err := d.State().Save(uid, "dns", db.IncidentState{UserID: uid, CheckName: "dns", CurrentStatus: "hard", ConsecutiveFails: 5, HardSince: &hard}); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-5 * time.Minute)
	for i := 0; i < 4; i++ {
		rec := postUnverifiedReport(t, h, base.Add(time.Duration(i)*time.Minute), []wire.Check{
			{Name: "agent_heartbeat", Status: "ok"},
			{Name: "dns", Status: "ok", Details: map[string]any{"unverified": true}},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("отчёт %d: %d", i, rec.Code)
		}
	}
	st, err := d.State().Get(uid, "dns")
	if err != nil || st.CurrentStatus != "hard" || st.ConsecutiveOKs != 0 {
		t.Fatalf("автомат сдвинут непроверенным ok: %+v err=%v", st, err)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	for _, c := range disp.checks {
		if c.Name == "dns" {
			t.Fatalf("диспетчер звали по непроверенной проверке: %+v", c)
		}
	}
}

// Мини-апп видит «не проверено» (unknown), а не «работает»: в списке
// проверок, в туннелях и в точках списка роутеров.
func TestMiniappShowsUnverifiedAsUnknown(t *testing.T) {
	d, uid, _, h := unverifiedEnv(t)
	now := time.Now().UTC()
	for _, r := range []struct{ check, details string }{
		{"agent_heartbeat", `{}`},
		{"dns", `{"unverified":true}`},
		{"tunnels", `{}`},
		{"tunnel_awg12", `{"tunnel_id":"awg12","status":"running","enabled":true,"unverified":true}`},
	} {
		if err := d.Events().Insert(uid, r.check, "ok", r.details, now); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string) []byte {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 100))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	var ev miniappRouterEventsResp
	_ = json.Unmarshal(get(fmt.Sprintf("/v1/miniapp/routers/%d/events", uid)), &ev)
	status := map[string]string{}
	for _, c := range ev.Checks {
		status[c.CheckName] = c.Status
	}
	if status["dns"] != "unknown" || status["agent_heartbeat"] != "ok" {
		t.Fatalf("проверки: %v", status)
	}
	if len(ev.Tunnels) != 1 || ev.Tunnels[0].Status != "unknown" {
		t.Fatalf("туннели: %+v", ev.Tunnels)
	}
	var list miniappRoutersResp
	_ = json.Unmarshal(get("/v1/miniapp/routers"), &list)
	dots := map[string]string{}
	for _, c := range list.Routers[0].Checks {
		dots[c.CheckName] = c.Status
	}
	if dots["dns"] != "unknown" {
		t.Fatalf("точки: %v", dots)
	}
}

// Непроверенный инвентарь туннелей -- не инвентарь: по нему призраков не
// отсеивают.
func TestUnverifiedTunnelsIsNotInventory(t *testing.T) {
	d, uid, _, h := unverifiedEnv(t)
	rec := postUnverifiedReport(t, h, time.Now().UTC(), []wire.Check{
		{Name: "agent_heartbeat", Status: "ok"}, {Name: "tunnels", Status: "unknown"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	if _, ok, _ := d.Users().TunnelsInventoryOKAt(uid); ok {
		t.Fatal("непроверенный tunnels записан как успешный инвентарь")
	}
}
