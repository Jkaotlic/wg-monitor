package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
)

// Экран настроек печатает пороги, по которым бот решает «роутер молчит» и
// «это уже тревога». Живут они в backend.yaml и больше нигде -- в макете они
// были взяты на глаз (60/120 сек), и рисовать их числами из головы значило бы
// показать человеку настройку, которой нет.
func TestMiniappSettingsReportsLiveThresholds(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	// Версию агента пишет его же отчёт (UpdateLastSeenAgentVersion), и на
	// экране это «какая программа сейчас на роутере», а не «какую туда
	// когда-то положил деплой».
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.16.0"); err != nil {
		t.Fatalf("set agent version: %v", err)
	}
	h := NewMux(Deps{
		DB:                        d,
		TelegramBotToken:          "test-bot-token",
		TelegramAdminUserID:       999,
		DashboardStaleAfterStatic: 120 * time.Second,
		DashboardStaleAfterMobile: 30 * time.Minute,
		Thresholds:                state.Thresholds{Fail: 3, Recovery: 2},
		MobileFailThreshold:       5,
	})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/settings", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp miniappSettingsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SilenceAfterSec != 120 {
		t.Errorf("silence_after_sec = %d, want 120 (порог статичного роутера)", resp.SilenceAfterSec)
	}
	if resp.AlertAfterFails != 3 || resp.RecoveryAfterOKs != 2 {
		t.Errorf("пороги тревоги = %d/%d, want 3/2", resp.AlertAfterFails, resp.RecoveryAfterOKs)
	}
	if resp.AgentVersion == "" {
		t.Errorf("agent_version пуст: версия агента живёт в users и на экране обязана быть")
	}
	// Роль нужна экрану, чтобы не рисовать кнопку, которой сервер всё равно
	// откажет: установка прошивки -- только владельцу.
	if resp.Role != "owner" {
		t.Errorf("role = %q, want owner", resp.Role)
	}
}

func TestMiniappSettingsReportsOperatorRole(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	if err := d.RouterOperators().Add(ownedID, 555, 100); err != nil {
		t.Fatalf("grant operator: %v", err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/settings", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 555))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp miniappSettingsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Role != "operator" {
		t.Errorf("role = %q, want operator", resp.Role)
	}
}

// Чужой роутер отвечает 404 до того, как выяснится, существует ли он, --
// та же граница, что у всех остальных экранов.
func TestMiniappSettingsStrangerGets404(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/settings", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 777))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for stranger, got %d: %s", rec.Code, rec.Body.String())
	}
}
