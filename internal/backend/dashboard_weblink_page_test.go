package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func dashboardLoginPage(t *testing.T) string {
	t.Helper()
	h := NewMux(Deps{DashboardToken: "dashboard-secret"})
	req := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("страница входа: код %d", rec.Code)
	}
	return rec.Body.String()
}

// Страница входа обменивает фрагмент адреса на обычную сессию: значение
// достаётся из location.hash, уходит POST'ом и тут же стирается из адресной
// строки. Переход по GET сюда не годится -- он положил бы грант в журнал
// сервера и в Referer соседних запросов.
func TestDashboardLoginPageTradesWebLinkFragmentForSession(t *testing.T) {
	page := dashboardLoginPage(t)
	for _, want := range []string{
		"location.hash",
		`"/v1/dashboard/web-link/redeem"`,
		"history.replaceState",
		`JSON.stringify({token})`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("на странице входа нет %q -- обмен ссылки не состоится", want)
		}
	}
	// Форма с токеном остаётся: дашборд -- аварийный вход, и вырезать её
	// значит остаться без входа ровно тогда, когда приложение недоступно.
	if !strings.Contains(page, `"/v1/dashboard/login"`) {
		t.Error("прежний вход по токену со страницы пропал")
	}
}

// Мёртвая ссылка объясняется словами и по-русски -- человек должен понять,
// что делать дальше, а не гадать над кодом ответа.
func TestDashboardLoginPageSpeaksWordsWhenLinkIsDead(t *testing.T) {
	page := dashboardLoginPage(t)
	if !strings.Contains(page, webLinkCopyDead) {
		t.Fatalf("страница входа не говорит «%s»", webLinkCopyDead)
	}
}
