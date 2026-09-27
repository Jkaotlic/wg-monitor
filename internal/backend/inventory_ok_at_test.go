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

	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// GHOST-01 (ревью): время последнего успешного инвентаря хранится у роутера
// (users.tunnels_inventory_ok_at) и пишется при приёме отчёта. Выборка по
// events проходила назад все строки tunnels=fail (статуса нет в индексе):
// 400-550 мс на роутер при долгом отказе awg-manager, умножить на список.
func TestReportStoresTunnelsInventoryOKAt(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	tok := "tok-owned-0000000000000000000000000000000000000000000000000000"
	mux := NewMux(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: d, Dispatcher: &fakeDisp{db: d}, Thresholds: state.Thresholds{Fail: 3, Recovery: 2}})
	post := func(ts time.Time, status string) {
		body, _ := json.Marshal(wire.Report{Timestamp: ts, Checks: []wire.Check{{Name: "agent_heartbeat", Status: "ok"}, {Name: "tunnels", Status: status}}})
		req := httptest.NewRequest(http.MethodPost, "/v1/report", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
		}
	}
	okTS := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	post(okTS, "ok")
	post(okTS.Add(5*time.Minute), "fail")
	got, ok, err := d.Users().TunnelsInventoryOKAt(ownedID)
	if err != nil || !ok || !got.Equal(okTS) {
		t.Fatalf("tunnels_inventory_ok_at = %v ok=%v err=%v, want %v", got, ok, err, okTS)
	}
}

// Экран берёт время инвентаря из строки роутера, а не из events: строк
// tunnels=ok в окне нет вовсе (давно вычищены), а отметка говорит, что
// awg12 удалён до последнего успешного инвентаря.
func TestMiniappEventsUsesStoredInventoryTime(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	now := time.Now().UTC().Truncate(time.Second)
	ins := func(check, status, details string, ts time.Time) {
		t.Helper()
		if err := d.Events().Insert(ownedID, check, status, details, ts); err != nil {
			t.Fatal(err)
		}
	}
	ins("tunnel_awg12", "ok", `{"tunnel_id":"awg12","status":"running","enabled":true}`, now.Add(-20*time.Minute))
	ins("tunnel_awg13", "ok", `{"tunnel_id":"awg13","status":"running","enabled":true}`, now.Add(-10*time.Minute))
	ins("tunnels", "fail", `{}`, now.Add(-time.Minute))
	if err := d.Users().SetTunnelsInventoryOKAt(ownedID, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp miniappRouterEventsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Tunnels) != 1 || resp.Tunnels[0].TunnelID != "awg13" {
		t.Fatalf("tunnels=%+v, want only awg13", resp.Tunnels)
	}
}

// До миграции отметки нет: один раз -- старая выборка, и результат
// запоминается у роутера.
func TestMiniappEventsBackfillsInventoryTimeOnce(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	now := time.Now().UTC().Truncate(time.Second)
	okAt := now.Add(-10 * time.Minute)
	for _, r := range []struct {
		check, status string
		ts            time.Time
	}{{"tunnels", "ok", okAt}, {"tunnels", "fail", now.Add(-time.Minute)}} {
		if err := d.Events().Insert(ownedID, r.check, r.status, "{}", r.ts); err != nil {
			t.Fatal(err)
		}
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	h.ServeHTTP(httptest.NewRecorder(), req)
	got, ok, err := d.Users().TunnelsInventoryOKAt(ownedID)
	if err != nil || !ok || !got.Equal(okAt) {
		t.Fatalf("отметка не запомнена: %v ok=%v err=%v", got, ok, err)
	}
}
