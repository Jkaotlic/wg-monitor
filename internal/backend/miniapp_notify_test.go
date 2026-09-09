package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
)

// Тумблер личный: выключает уведомления тому, кто нажал, и никому больше.
func TestMiniappNotifyMuteIsPersonal(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	h := NewMux(Deps{
		DB:                        d,
		TelegramBotToken:          "test-bot-token",
		TelegramAdminUserID:       999,
		DashboardStaleAfterStatic: 120 * time.Second,
		DashboardStaleAfterMobile: 30 * time.Minute,
		Thresholds:                state.Thresholds{Fail: 3, Recovery: 2},
	})

	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/v1/miniapp/routers/%d/notify", ownedID),
		strings.NewReader(`{"muted":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	muted, err := d.NotifyMutes().IsMuted(telegramUserID, ownedID)
	if err != nil {
		t.Fatal(err)
	}
	if !muted {
		t.Fatal("нажавший обязан быть заглушён")
	}
	// Чужие уведомления тумблер не трогает.
	other, err := d.NotifyMutes().IsMuted(telegramUserID+1, ownedID)
	if err != nil {
		t.Fatal(err)
	}
	if other {
		t.Fatal("выключатель личный: соседа он глушить не может")
	}
}

// Заглушение не отбирает доступ: человек по-прежнему открывает экраны и
// видит на них состояние своего тумблера.
func TestMiniappMuteKeepsAccessAndShowsState(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	if err := d.NotifyMutes().SetMuted(telegramUserID, ownedID, true); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{
		DB:                        d,
		TelegramBotToken:          "test-bot-token",
		TelegramAdminUserID:       999,
		DashboardStaleAfterStatic: 120 * time.Second,
		DashboardStaleAfterMobile: 30 * time.Minute,
		Thresholds:                state.Thresholds{Fail: 3, Recovery: 2},
	})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/settings", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("заглушивший потерял доступ: %d %s", rec.Code, rec.Body.String())
	}
	var resp miniappSettingsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.NotifyMuted {
		t.Fatal("экран обязан показывать, что уведомления выключены")
	}
}
