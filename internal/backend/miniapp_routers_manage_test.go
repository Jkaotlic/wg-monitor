package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Строки /v1/miniapp/routers по-сырому: omitempty-поля проверяем по наличию
// ключа, а не по нулевому значению структуры.
func miniappRoutersRows(t *testing.T, h http.Handler, telegramUserID int64) (string, map[string]map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/routers: код %d тело %s", rec.Code, rec.Body.String())
	}
	var raw struct {
		Routers []map[string]any `json:"routers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	rows := map[string]map[string]any{}
	for _, r := range raw.Routers {
		rows[r["nickname"].(string)] = r
	}
	return rec.Body.String(), rows
}

// Адрес панели -- владельцу и админу, строкой списка: нажатие открывает
// админку awg-manager в браузере (решение оператора 18.09, отменяет 14.09).
// Оператор роутера адреса не получает.
func TestMiniappRoutersPanelURLForOwnerAndAdminOnly(t *testing.T) {
	d, ownedID, otherID, ownerTG := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, "https://awg.example.com")
	setAWGMURL(t, d, otherID, "javascript:alert(1)")
	if err := d.RouterOperators().Add(ownedID, 555, 999); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	for _, who := range []int64{ownerTG, 999} {
		_, rows := miniappRoutersRows(t, h, who)
		if got := rows["router-owned"]["panel_url"]; got != "https://awg.example.com" {
			t.Errorf("tg %d: panel_url = %v", who, got)
		}
	}
	// Негодный адрес -- поля нет вовсе, даже админу.
	_, rows := miniappRoutersRows(t, h, 999)
	if _, ok := rows["router-other"]["panel_url"]; ok {
		t.Errorf("негодный адрес уехал: %v", rows["router-other"])
	}

	body, rows := miniappRoutersRows(t, h, 555)
	if _, ok := rows["router-owned"]; !ok {
		t.Fatalf("оператор не видит свой роутер: %s", body)
	}
	if strings.Contains(body, "panel_url") || strings.Contains(body, "awg.example.com") {
		t.Errorf("оператору уехал адрес панели: %s", body)
	}
}

// Логин и пароль в адресе панели -- секрет: в ссылку клиенту они не попадают.
func TestPanelAddressDropsUserinfo(t *testing.T) {
	withCreds := url.URL{Scheme: "https", User: url.UserPassword("admin", "secret"), Host: "awg.example.com:2222", Path: "/"}
	raw := withCreds.String()
	got, ok := panelAddress(&raw)
	if !ok || got != "https://awg.example.com:2222/" {
		t.Fatalf("panelAddress = %q, %v", got, ok)
	}
}

// Роутер формы workrouter: несущий awg14 жив, запасной awg10 мёртв.
func seedWorkrouterEvents(t *testing.T, d *db.DB, routerID int64, active, fallback string) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	hydra := strings.NewReplacer("%ACTIVE%", active, "%FALLBACK%", fallback).Replace(workrouterHydraPolicies)
	rows := []struct{ check, status, details string }{
		{"agent_heartbeat", "ok", ""},
		{"hydraroute", "ok", hydra},
		{"tunnels", "ok", `{"tunnel_count":2}`},
		{"tunnel_awg10", "fail", `{"tunnel_id":"awg10","tunnel_name":"nl2","status":"running","enabled":true,"handshake_age_sec":1440,"active_default_known":true}`},
		{"tunnel_awg14", "ok", `{"tunnel_id":"awg14","tunnel_name":"hipvps","status":"running","enabled":true,"handshake_age_sec":40,"active_default_known":true}`},
	}
	for _, r := range rows {
		if err := d.Events().Insert(routerID, r.check, r.status, r.details, now); err != nil {
			t.Fatal(err)
		}
	}
}

// Все тревоги -- только по запасному звену, несущий жив: строка списка
// говорит «резерв не работает», а не «тревога».
func TestMiniappRoutersReserveOnlyAlert(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	seedWorkrouterEvents(t, d, ownedID, "awg14", "awg10")
	seedHardIncident(t, d, ownedID, "tunnel_awg10")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	_, rows := miniappRoutersRows(t, h, ownerTG)
	if got := rows["router-owned"]["reserve_only_alert"]; got != true {
		t.Fatalf("reserve_only_alert = %v, хотим true: %v", got, rows["router-owned"])
	}
}

// Обратный случай: активное звено набора -- мёртвое. Это настоящая тревога.
func TestMiniappRoutersReserveOnlyAlertFalseWhenCarrierDead(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	seedWorkrouterEvents(t, d, ownedID, "awg10", "awg14")
	seedHardIncident(t, d, ownedID, "tunnel_awg10")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	body, rows := miniappRoutersRows(t, h, ownerTG)
	if _, ok := rows["router-owned"]["reserve_only_alert"]; ok {
		t.Fatalf("несущий мёртв -- плашки «резерв» нет: %s", body)
	}
}

// Тревога не по туннелю (DNS) рядом с тревогой запасного -- тоже настоящая.
func TestMiniappRoutersReserveOnlyAlertFalseWithOtherIncident(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	seedWorkrouterEvents(t, d, ownedID, "awg14", "awg10")
	seedHardIncident(t, d, ownedID, "tunnel_awg10")
	seedHardIncident(t, d, ownedID, "dns")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	body, rows := miniappRoutersRows(t, h, ownerTG)
	if _, ok := rows["router-owned"]["reserve_only_alert"]; ok {
		t.Fatalf("тревога DNS -- не резерв: %s", body)
	}
}

// Старый агент без сводки политик: несущий неизвестен -- не угадываем.
func TestMiniappRoutersReserveOnlyAlertNeedsPolicies(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	now := time.Now().UTC().Add(-time.Minute)
	_ = d.Events().Insert(ownedID, "hydraroute", "ok", `{"installed":true,"running":true,"routes_hrneo":31}`, now)
	_ = d.Events().Insert(ownedID, "tunnel_awg10", "fail", `{"tunnel_id":"awg10","status":"running","active_default_known":true}`, now)
	_ = d.Events().Insert(ownedID, "tunnel_awg14", "ok", `{"tunnel_id":"awg14","status":"running","active_default_known":true}`, now)
	seedHardIncident(t, d, ownedID, "tunnel_awg10")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	body, rows := miniappRoutersRows(t, h, ownerTG)
	if _, ok := rows["router-owned"]["reserve_only_alert"]; ok {
		t.Fatalf("без сводки политик -- без плашки: %s", body)
	}
}

// Билетов панели больше нет: переход -- прямой ссылкой panel_url.
func TestPanelTicketRoutesRemoved(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, "https://awg.example.com")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/panel/ticket", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", ownerTG))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("выдача билета жива: %d %s", rec.Code, rec.Body.String())
	}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/v1/panel/abcdef0123456789", nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /v1/panel/{ticket}: код %d", m, rec.Code)
		}
	}
}
