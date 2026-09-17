package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// Цель оператора: «дашборд и миниапп должны иметь одинаковый набор функций».
// Оживление из браузера -- настоящий mux, настоящая кука входа в
// веб-управление, никакой подписи Telegram.
func TestWebEntryCanScheduleRevive(t *testing.T) {
	stubLatestVersion(t, "v0.33.0")
	d, ownedID, _, _ := seedMiniappFleet(t)
	fake := &fakeRevive{
		enabled: true,
		intent:  revive.Intent{Status: "waiting", ExpiresAt: reviveTestExpires},
		views:   map[int64]*revive.IntentView{},
	}
	h := NewMux(Deps{
		DB:                  d,
		DashboardToken:      webTestDash,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		ReviveOverride:      fake,
	})

	login := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", strings.NewReader(`{"token":"`+webTestDash+`"}`))
	login.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK || len(loginRec.Result().Cookies()) != 1 {
		t.Fatalf("вход: код %d", loginRec.Code)
	}
	cookie := loginRec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodPost, revivePath(ownedID), strings.NewReader(reviveBody("Router-Owned ", nil)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("оживление из браузера: код %d тело %s", rec.Code, rec.Body.String())
	}
	if len(fake.scheduled) != 1 || fake.scheduled[0].RequestedBy != 999 {
		t.Fatalf("сервис: %+v", fake.scheduled)
	}
}
