package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func setAWGMURL(t *testing.T, d *db.DB, routerID int64, raw string) {
	t.Helper()
	if _, err := d.SQL().Exec(`UPDATE users SET awgm_url=? WHERE id=?`, raw, routerID); err != nil {
		t.Fatal(err)
	}
}

func miniappSettingsFor(t *testing.T, h http.Handler, routerID, telegramUserID int64) (*httptest.ResponseRecorder, miniappSettingsResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/settings", routerID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp miniappSettingsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec, resp
}

// panelScope отвечает на «откроется ли кнопка из кафе» -- словом, а не хостом.
func TestPanelScope(t *testing.T) {
	cases := map[string]string{
		"https://panel.example.com": "public",
		"http://203.0.113.10:2222":  "public",
		"http://192.168.1.1:2222":   "private",
		"http://10.0.0.1":           "private",
		"http://127.0.0.1:2222":     "private",
		"http://[fe80::1]:2222":     "private",
		"http://router:2222":        "private",
		"http://keenetic.local":     "private",
		"":                          "",
		"javascript:alert(1)":       "",
		"ftp://panel.example.com":   "",
		"https://":                  "",
	}
	for in, want := range cases {
		if got := panelScope(in); got != want {
			t.Errorf("panelScope(%q) = %q, хотим %q", in, got, want)
		}
	}
}

// Настройки говорят «панель известна» и «публичная/частная», но не адрес и не
// хост: адрес -- секрет того же класса, что dyn-DNS роутера.
func TestMiniappSettingsTellsPanelKnownWithoutAddress(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, "https://panel.example.com/admin")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	for _, who := range []int64{ownerTG, 999} {
		rec, resp := miniappSettingsFor(t, h, ownedID, who)
		if !resp.PanelKnown || resp.PanelScope != "public" {
			t.Errorf("tg %d: panel_known=%v panel_scope=%q", who, resp.PanelKnown, resp.PanelScope)
		}
		for _, forbidden := range []string{"awgm_url", "awgm_auth", "panel_host", "panel.example.com", "/admin"} {
			if strings.Contains(rec.Body.String(), forbidden) {
				t.Errorf("tg %d: в ответе настроек %q: %s", who, forbidden, rec.Body.String())
			}
		}
	}
}

func TestMiniappSettingsMarksPrivatePanelScope(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, "http://192.168.1.1:2222")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	if _, resp := miniappSettingsFor(t, h, ownedID, ownerTG); resp.PanelScope != "private" {
		t.Errorf("panel_scope = %q, хотим private", resp.PanelScope)
	}
}

// Пустой и негодный адрес -- признака нет вовсе: «адрес не сохранён», а не
// кнопка, которая ведёт в никуда.
func TestMiniappSettingsOmitsPanelWhenURLMissingOrInvalid(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	for _, raw := range []string{"", "javascript:alert(1)"} {
		setAWGMURL(t, d, ownedID, raw)
		rec, _ := miniappSettingsFor(t, h, ownedID, ownerTG)
		if strings.Contains(rec.Body.String(), "panel_known") {
			t.Errorf("адрес %q: ключ panel_known в ответе: %s", raw, rec.Body.String())
		}
	}
}

// Оператору роутера строки панели нет вовсе (решение оператора № 9).
func TestMiniappSettingsHidesPanelFromOperator(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, "https://panel.example.com")
	if err := d.RouterOperators().Add(ownedID, 555, 999); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	rec, resp := miniappSettingsFor(t, h, ownedID, 555)
	if rec.Code != http.StatusOK {
		t.Fatalf("оператор: код %d", rec.Code)
	}
	if resp.PanelKnown || strings.Contains(rec.Body.String(), "panel_") {
		t.Errorf("оператор получил признак панели: %s", rec.Body.String())
	}
}
