package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	webTestBot   = "test-bot-token"
	webTestDash  = "dash-secret"
	webTestAdmin = int64(999)
)

func webDashCookie(t *testing.T) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", nil)
	return dashboardSessionCookie(req, webTestDash)
}

// Кука дашборда -- это вход админа из браузера: личность берётся из
// конфига, а не из куки, потому что у общего токена нет своего человека.
func TestMiniappIdentifyDashboardCookieIsAdmin(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	r.AddCookie(webDashCookie(t))
	uid, via, code := miniappIdentify(r, webTestBot, webTestDash, webTestAdmin)
	if uid != webTestAdmin || via != miniappViaWeb || code != "" {
		t.Fatalf("uid=%d via=%q code=%q", uid, via, code)
	}
}

// Обе куки сразу (админ открывал мини-апп в том же браузере): побеждает
// личная кука мини-аппа -- она называет конкретного человека.
func TestMiniappIdentifyMiniappCookieWins(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	r.AddCookie(miniappSessionCookie(r, webTestBot, 555))
	r.AddCookie(webDashCookie(t))
	uid, via, code := miniappIdentify(r, webTestBot, webTestDash, webTestAdmin)
	if uid != 555 || via != miniappViaTelegram || code != "" {
		t.Fatalf("uid=%d via=%q code=%q", uid, via, code)
	}
}

func TestMiniappIdentifyAdminNotConfigured(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	r.AddCookie(webDashCookie(t))
	_, _, code := miniappIdentify(r, webTestBot, webTestDash, 0)
	if code != "admin_not_configured" {
		t.Fatalf("code=%q", code)
	}
}

func TestMiniappIdentifyRejectsForgedExpiredBearerAndNoDashboard(t *testing.T) {
	forged := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	forged.AddCookie(&http.Cookie{Name: dashboardSessionCookieName, Value: "v3:9999999999:deadbeef"})

	expired := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	expired.AddCookie(&http.Cookie{Name: dashboardSessionCookieName,
		Value: dashboardSessionValue(webTestDash, time.Now().Add(-time.Minute))})

	bearer := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	bearer.Header.Set("Authorization", "Bearer "+webTestDash)

	// Дашборд выключен (токен пуст): кука, подписанная пустым токеном, не
	// должна стать дверью.
	noDash := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	noDash.AddCookie(&http.Cookie{Name: dashboardSessionCookieName,
		Value: dashboardSessionValue("", time.Now().Add(time.Hour))})

	for name, r := range map[string]*http.Request{"поддельная": forged, "просроченная": expired, "bearer": bearer} {
		if _, _, code := miniappIdentify(r, webTestBot, webTestDash, webTestAdmin); code != "unauthorized" {
			t.Errorf("%s: code=%q", name, code)
		}
	}
	if _, _, code := miniappIdentify(noDash, webTestBot, "", webTestAdmin); code != "unauthorized" {
		t.Errorf("пустой токен дашборда: code=%q", code)
	}
}

func webAuthHandler(t *testing.T) http.Handler {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Via", string(miniappViaFromContext(r.Context())))
		w.WriteHeader(http.StatusOK)
	})
	return MiniAppAuthMiddleware(webTestBot, webTestDash, webTestAdmin, nil)(inner)
}

// Межсайтовая форма не умеет слать application/json: для входа из браузера
// это вторая стенка после SameSite=Strict.
func TestMiniappAuthWebRequiresJSONOnWrites(t *testing.T) {
	h := webAuthHandler(t)
	cases := []struct {
		method, ct string
		want       int
	}{
		{http.MethodGet, "", http.StatusOK},
		{http.MethodPost, "", http.StatusUnsupportedMediaType},
		{http.MethodPost, "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{http.MethodPost, "text/plain", http.StatusUnsupportedMediaType},
		{http.MethodDelete, "", http.StatusUnsupportedMediaType},
		{http.MethodPut, "application/json; charset=utf-8", http.StatusOK},
		{http.MethodPost, "application/json", http.StatusOK},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "/v1/miniapp/routers/1/repair", strings.NewReader("{}"))
		if c.ct != "" {
			r.Header.Set("Content-Type", c.ct)
		}
		r.AddCookie(webDashCookie(t))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s ct=%q: код %d, ждали %d", c.method, c.ct, rec.Code, c.want)
		}
		if c.want == http.StatusUnsupportedMediaType && !strings.Contains(rec.Body.String(), errCodeUnsupportedCT) {
			t.Errorf("%s ct=%q: тело %s", c.method, c.ct, rec.Body.String())
		}
	}
}

// Вход из Telegram этой стенки не получает: поведение прежнее.
func TestMiniappAuthTelegramWritesUnchanged(t *testing.T) {
	h := webAuthHandler(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/miniapp/routers/1/repair", nil)
	r.AddCookie(miniappSessionCookie(r, webTestBot, 555))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Via") != "telegram" {
		t.Fatalf("код %d via=%q", rec.Code, rec.Header().Get("X-Via"))
	}
}

func TestMiniappAuthAdminNotConfiguredAnswers401WithCode(t *testing.T) {
	h := MiniAppAuthMiddleware(webTestBot, webTestDash, 0, nil)(http.NotFoundHandler())
	r := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	r.AddCookie(webDashCookie(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"admin_not_configured"`) {
		t.Fatalf("код %d тело %s", rec.Code, rec.Body.String())
	}
}

type whoAmI struct {
	OK             bool   `json:"ok"`
	TelegramUserID int64  `json:"telegram_user_id"`
	IsAdmin        bool   `json:"is_admin"`
	Via            string `json:"via"`
}

func webMux(t *testing.T) http.Handler {
	t.Helper()
	return NewMux(Deps{DashboardToken: webTestDash, TelegramBotToken: webTestBot, TelegramAdminUserID: webTestAdmin})
}

func TestMiniappWhoAmIForBothIdentities(t *testing.T) {
	h := webMux(t)

	web := httptest.NewRequest(http.MethodGet, "/v1/miniapp/session", nil)
	web.AddCookie(webDashCookie(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, web)
	var got whoAmI
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil ||
		got != (whoAmI{OK: true, TelegramUserID: webTestAdmin, IsAdmin: true, Via: "web"}) {
		t.Fatalf("web: код %d тело %s", rec.Code, rec.Body.String())
	}

	tg := httptest.NewRequest(http.MethodGet, "/v1/miniapp/session", nil)
	tg.AddCookie(miniappSessionCookie(tg, webTestBot, 555))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, tg)
	got = whoAmI{}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil ||
		got != (whoAmI{OK: true, TelegramUserID: 555, IsAdmin: false, Via: "telegram"}) {
		t.Fatalf("telegram: код %d тело %s", rec.Code, rec.Body.String())
	}

	none := httptest.NewRequest(http.MethodGet, "/v1/miniapp/session", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, none)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("без куки: код %d", rec.Code)
	}
}

// «Выйти» в веб-управлении выходит совсем: кука мини-аппа главнее куки
// дашборда, и оставь её -- следующий запрос вернул бы человека обратно.
func TestDashboardLogoutClearsBothSessions(t *testing.T) {
	h := webMux(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/dashboard/logout", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("код %d", rec.Code)
	}
	cleared := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 && c.Value == "" && c.Path == "/" {
			cleared[c.Name] = true
		}
	}
	for _, name := range []string{dashboardSessionCookieName, miniappSessionCookieName} {
		if !cleared[name] {
			t.Errorf("кука %s не погашена: %+v", name, rec.Result().Cookies())
		}
	}
}
