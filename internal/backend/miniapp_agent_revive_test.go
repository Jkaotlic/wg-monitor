package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// Пароли-фикстуры. Сторож ищет их везде, куда может утечь секрет: ответы
// маршрутов, журнал, текст ошибки. Кириллица и дефисы -- чтобы экранирование
// JSON не спрятало утечку от strings.Contains.
const (
	miniappReviveRoot  = "root-Пароль-9f3kq"
	miniappRevivePanel = "panel-Пароль-x71zz"
	miniappReviveKey   = "awgm-key-5b2c77d0"
	miniappReviveLogin = "admin-login-q8"
)

var reviveFixtureSecrets = []string{miniappReviveRoot, miniappRevivePanel, miniappReviveKey, miniappReviveLogin}

func assertNoReviveSecrets(t *testing.T, where, text string) {
	t.Helper()
	for _, s := range reviveFixtureSecrets {
		if strings.Contains(text, s) {
			t.Errorf("%s: утёк секрет %q: %s", where, s, text)
		}
	}
}

// fakeRevive -- сервис оживления без ключа и без воркера. Запоминает вызовы,
// отвечает тем, что положил тест.
type fakeRevive struct {
	enabled       bool
	scheduledFor  []int64
	scheduled     []revive.ScheduleRequest
	intent        revive.Intent
	scheduleErr   error
	cancelledFor  []int64
	cancelCleared bool
	cancelErr     error
	views         map[int64]*revive.IntentView
	statusErr     error
}

func (f *fakeRevive) Enabled() bool { return f.enabled }

func (f *fakeRevive) Schedule(_ context.Context, routerID int64, req revive.ScheduleRequest) (revive.Intent, error) {
	f.scheduledFor = append(f.scheduledFor, routerID)
	f.scheduled = append(f.scheduled, req)
	return f.intent, f.scheduleErr
}

func (f *fakeRevive) Cancel(_ context.Context, routerID int64) (bool, error) {
	f.cancelledFor = append(f.cancelledFor, routerID)
	return f.cancelCleared, f.cancelErr
}

func (f *fakeRevive) StatusFor(routerID int64) (*revive.IntentView, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return f.views[routerID], nil
}

var reviveTestExpires = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func reviveTestMux(t *testing.T) (*db.DB, int64, http.Handler, *fakeRevive, *bytes.Buffer) {
	t.Helper()
	stubLatestVersion(t, "v0.33.0")
	d, ownedID, _, _ := seedMiniappFleet(t)
	fake := &fakeRevive{
		enabled: true,
		intent:  revive.Intent{Status: "waiting", ExpiresAt: reviveTestExpires},
		views:   map[int64]*revive.IntentView{},
	}
	logs := &bytes.Buffer{}
	h := NewMux(Deps{
		DB:                  d,
		Logger:              slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		ReviveOverride:      fake,
	})
	return d, ownedID, h, fake, logs
}

func deleteMiniapp(t *testing.T, h http.Handler, path string, telegramUserID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func reviveBody(confirm string, extra map[string]any) string {
	body := map[string]any{
		"root_password": miniappReviveRoot,
		"awgm_login":    miniappReviveLogin,
		"awgm_password": miniappRevivePanel,
		"awgm_api_key":  miniappReviveKey,
		"confirm":       confirm,
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func revivePath(id int64) string { return fmt.Sprintf("/v1/miniapp/routers/%d/agent/revive", id) }

// Владелец роутера -- не админ: 404 до всего, и сервис не вызван ни разу.
// Та же форма отказа, когда оживление выключено: по коду нельзя узнать,
// есть ли такая поверхность.
func TestMiniappAgentReviveHiddenFromNonAdmin(t *testing.T) {
	_, ownedID, h, fake, logs := reviveTestMux(t)
	for _, path := range []string{revivePath(ownedID), revivePath(424242)} {
		rec := postMiniappJSON(t, h, path, reviveBody("router-owned", nil), 100)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s владельцу: код %d, ждали 404", path, rec.Code)
		}
		assertNoReviveSecrets(t, "ответ владельцу", rec.Body.String())
		rec = deleteMiniapp(t, h, path, 100)
		if rec.Code != http.StatusNotFound {
			t.Errorf("DELETE %s владельцу: код %d, ждали 404", path, rec.Code)
		}
	}
	fake.enabled = false
	if rec := postMiniappJSON(t, h, revivePath(ownedID), reviveBody("router-owned", nil), 100); rec.Code != http.StatusNotFound {
		t.Errorf("выключенное оживление владельцу: код %d, ждали 404", rec.Code)
	}
	if len(fake.scheduledFor) != 0 || len(fake.cancelledFor) != 0 {
		t.Fatalf("не-админ дошёл до сервиса: schedule=%v cancel=%v", fake.scheduledFor, fake.cancelledFor)
	}
	assertNoReviveSecrets(t, "журнал", logs.String())
}

func TestMiniappAgentReviveSchedules(t *testing.T) {
	_, ownedID, h, fake, logs := reviveTestMux(t)
	rec := postMiniappJSON(t, h, revivePath(ownedID), reviveBody("Router-Owned ", nil), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status    string `json:"status"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "waiting" || resp.ExpiresAt != "2026-10-15T12:00:00Z" {
		t.Fatalf("ответ: %+v", resp)
	}
	assertNoReviveSecrets(t, "ответ 202", rec.Body.String())
	if len(fake.scheduled) != 1 || fake.scheduledFor[0] != ownedID {
		t.Fatalf("сервис вызван: %v", fake.scheduledFor)
	}
	got := fake.scheduled[0]
	want := revive.ScheduleRequest{
		RootPassword: miniappReviveRoot, AWGMLogin: miniappReviveLogin, AWGMPassword: miniappRevivePanel,
		AWGMAPIKey: miniappReviveKey, AWGMURL: "", ExpiresDays: 30, RequestedBy: 999,
	}
	if got != want {
		t.Fatalf("запрос к сервису: %+v", got)
	}
	// Журнал сказал, что оживление поставлено, -- и ни одного секрета в нём.
	if !strings.Contains(logs.String(), "miniapp agent revive scheduled") {
		t.Fatalf("строки о постановке нет в журнале: %s", logs.String())
	}
	assertNoReviveSecrets(t, "журнал", logs.String())
}

func TestMiniappAgentRevivePassesPanelAddressAndRunningStatus(t *testing.T) {
	_, ownedID, h, fake, _ := reviveTestMux(t)
	fake.intent = revive.Intent{Status: "running", ExpiresAt: reviveTestExpires}
	body := `{"root_password":"` + miniappReviveRoot + `","awgm_url":" https://router.example.com:2222 ","expires_days":7,"confirm":"router-owned"}`
	rec := postMiniappJSON(t, h, revivePath(ownedID), body, 999)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"running"`) {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	got := fake.scheduled[0]
	if got.AWGMURL != "https://router.example.com:2222" || got.ExpiresDays != 7 || got.AWGMLogin != "" || got.AWGMAPIKey != "" {
		t.Fatalf("запрос к сервису: адрес %q срок %d", got.AWGMURL, got.ExpiresDays)
	}
}

func TestMiniappAgentReviveExpiresDaysBounds(t *testing.T) {
	_, ownedID, h, fake, _ := reviveTestMux(t)
	for _, days := range []int{-1, 31, 91, 365} {
		rec := postMiniappJSON(t, h, revivePath(ownedID), reviveBody("router-owned", map[string]any{"expires_days": days}), 999)
		code, errField, msg := decodeDeployError(t, rec)
		if rec.Code != http.StatusBadRequest || code != "bad_request" || errField != "bad_request" || !strings.Contains(msg, "30 дней") {
			t.Errorf("срок %d: код %d %q %q", days, rec.Code, code, msg)
		}
	}
	if len(fake.scheduled) != 0 {
		t.Fatalf("неверный срок дошёл до сервиса: %v", fake.scheduled)
	}
}

// Внутренние имена в тексте для людей: поля запроса, таблица, слова воркера.
var reviveInternalNames = regexp.MustCompile(`awgm_url|root_password|awgm_|api_key|intent|probe|revive|waiting`)

func TestMiniappAgentReviveRefusals(t *testing.T) {
	_, ownedID, h, fake, logs := reviveTestMux(t)
	check := func(name string, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
		t.Helper()
		if rec.Code != wantStatus {
			t.Fatalf("%s: код %d, ждали %d (%s)", name, rec.Code, wantStatus, rec.Body.String())
		}
		code, errField, msg := decodeDeployError(t, rec)
		if code != wantCode || errField != wantCode {
			t.Fatalf("%s: code=%q error=%q, ждали %q в обоих", name, code, errField, wantCode)
		}
		if msg == "" || reviveInternalNames.MatchString(msg) || !regexp.MustCompile(`[А-Яа-яЁё]`).MatchString(msg) {
			t.Fatalf("%s: текст для людей %q", name, msg)
		}
		assertNoReviveSecrets(t, name, rec.Body.String())
	}

	check("подтверждение", postMiniappJSON(t, h, revivePath(ownedID), reviveBody("router-other", nil), 999),
		http.StatusBadRequest, "confirm_mismatch")
	if len(fake.scheduled) != 0 {
		t.Fatal("неверное имя дошло до сервиса")
	}
	check("битое тело", postMiniappJSON(t, h, revivePath(ownedID), `{`, 999), http.StatusBadRequest, "bad_request")
	check("нет роутера", postMiniappJSON(t, h, revivePath(424242), reviveBody("router-owned", nil), 999),
		http.StatusNotFound, "not_found")

	for _, tc := range []struct {
		code   string
		status int
	}{
		{"no_awgm_url", http.StatusBadRequest},
		{"invalid_awgm_url", http.StatusBadRequest},
		{"no_credentials", http.StatusBadRequest},
		{"awgm_url_already_set", http.StatusConflict},
		{"agent_alive", http.StatusConflict},
		{"revive_disabled", http.StatusServiceUnavailable},
		{"revive_running", http.StatusConflict},
		{"router_not_found", http.StatusNotFound},
	} {
		fake.scheduleErr = &revive.Error{Code: tc.code}
		check(tc.code, postMiniappJSON(t, h, revivePath(ownedID), reviveBody("router-owned", nil), 999), tc.status, tc.code)
	}

	// Неизвестная ошибка сервиса с паролем внутри: наружу -- общий текст,
	// в журнал -- только код, не текст ошибки.
	fake.scheduleErr = fmt.Errorf("relay: login %s/%s refused", miniappReviveLogin, miniappRevivePanel)
	check("неизвестная", postMiniappJSON(t, h, revivePath(ownedID), reviveBody("router-owned", nil), 999),
		http.StatusInternalServerError, errCodeInternal)
	assertNoReviveSecrets(t, "журнал", logs.String())
}

func TestMiniappAgentReviveDisabled(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	for name, deps := range map[string]Deps{
		"сервиса нет":     {DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999},
		"сервис выключен": {DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, ReviveOverride: &fakeRevive{}},
	} {
		h := NewMux(deps)
		for _, rec := range []*httptest.ResponseRecorder{
			postMiniappJSON(t, h, revivePath(ownedID), reviveBody("router-owned", nil), 999),
			deleteMiniapp(t, h, revivePath(ownedID), 999),
		} {
			code, _, msg := decodeDeployError(t, rec)
			if rec.Code != http.StatusServiceUnavailable || code != "revive_disabled" || msg != "Оживление агента не настроено на сервере." {
				t.Errorf("%s: код %d %q %q", name, rec.Code, code, msg)
			}
		}
	}
}

func TestMiniappAgentReviveCancel(t *testing.T) {
	_, ownedID, h, fake, logs := reviveTestMux(t)
	fake.cancelCleared = true
	rec := deleteMiniapp(t, h, revivePath(ownedID), 999)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"cleared":true}` {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	if len(fake.cancelledFor) != 1 || fake.cancelledFor[0] != ownedID {
		t.Fatalf("отмена: %v", fake.cancelledFor)
	}
	fake.cancelCleared = false
	if rec := deleteMiniapp(t, h, revivePath(ownedID), 999); strings.TrimSpace(rec.Body.String()) != `{"cleared":false}` {
		t.Fatalf("снимать нечего: %s", rec.Body.String())
	}
	fake.cancelErr = errors.New("db locked " + miniappReviveRoot)
	rec = deleteMiniapp(t, h, revivePath(ownedID), 999)
	code, _, msg := decodeDeployError(t, rec)
	if rec.Code != http.StatusInternalServerError || code != errCodeInternal || msg != "Не удалось отменить оживление." {
		t.Fatalf("сбой отмены: %d %q %q", rec.Code, code, msg)
	}
	// Идущую переустановку не отменить: 409 с понятным текстом, не 500.
	fake.cancelErr = &revive.Error{Code: "revive_running"}
	rec = deleteMiniapp(t, h, revivePath(ownedID), 999)
	code, _, msg = decodeDeployError(t, rec)
	if rec.Code != http.StatusConflict || code != "revive_running" || msg != "Оживление уже идёт — дождитесь итога." {
		t.Fatalf("отмена во время переустановки: %d %q %q", rec.Code, code, msg)
	}
	before := len(fake.cancelledFor)
	if rec := deleteMiniapp(t, h, revivePath(424242), 999); rec.Code != http.StatusNotFound {
		t.Fatalf("нет роутера: код %d", rec.Code)
	}
	if len(fake.cancelledFor) != before {
		t.Fatal("отмена несуществующего роутера дошла до сервиса")
	}
	assertNoReviveSecrets(t, "журнал", logs.String())
}

// Тело запроса не печатается даже по ошибке: fmt и slog видят заглушку.
func TestMiniappAgentReviveRequestMasksItself(t *testing.T) {
	req := miniappAgentReviveReq{RootPassword: miniappReviveRoot, AWGMLogin: miniappReviveLogin,
		AWGMPassword: miniappRevivePanel, AWGMAPIKey: miniappReviveKey, Confirm: "x"}
	assertNoReviveSecrets(t, "fmt", fmt.Sprintf("%v %+v %#v %s", req, req, req, &req))
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "req", req, "ptr", &req)
	assertNoReviveSecrets(t, "slog", buf.String())
}
