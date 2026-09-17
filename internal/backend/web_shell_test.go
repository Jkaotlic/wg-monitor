package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const telegramSDKTag = `<script src="https://telegram.org/js/telegram-web-app.js"></script>`

func TestWebShellHTMLStripsTelegramSDK(t *testing.T) {
	in := []byte("<head>\n    " + telegramSDKTag + "\n    <link rel=\"stylesheet\">\n</head>")
	out, found := webShellHTML(in)
	if !found || strings.Contains(string(out), "telegram-web-app.js") || !strings.Contains(string(out), `<link rel="stylesheet">`) {
		t.Fatalf("found=%v out=%s", found, out)
	}
	same, found := webShellHTML([]byte("<head></head>"))
	if found || string(same) != "<head></head>" {
		t.Fatalf("без тега: found=%v out=%s", found, same)
	}
}

// Веб-управление нужно, когда Telegram недоступен -- в том числе заблокирован.
// Синхронный внешний скрипт повесил бы страницу до таймаута.
func TestDashboardServesMiniappShellWithoutTelegramSDK(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "secret", TelegramBotToken: "bot"})
	for _, path := range []string{"/dashboard/", "/dashboard/login"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: код %d", path, rec.Code)
		}
		if strings.Contains(body, "telegram-web-app.js") {
			t.Errorf("%s: в веб-оболочке остался SDK Telegram", path)
		}
		if !strings.Contains(body, `src="/miniapp/assets/`) {
			t.Errorf("%s: нет бандла мини-аппа", path)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "no-cache") {
			t.Errorf("%s: Cache-Control=%q", path, rec.Header().Get("Cache-Control"))
		}
	}
	// Мини-апп в Telegram не трогаем: там SDK обязателен.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/miniapp/", nil))
	if !strings.Contains(rec.Body.String(), "telegram-web-app.js") {
		t.Fatal("/miniapp/ потерял SDK Telegram")
	}
}

// Старый дашборд удалён (цикл 3). Закладки на него ведут на аварийную
// страницу -- без куки и без проверки сессии: редирект данных не несёт.
func TestDashboardClassicRedirectsToRescue(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "secret"})
	for _, path := range []string{"/dashboard/classic", "/dashboard/classic/", "/dashboard/classic/login", "/dashboard/classic/app.js", "/dashboard/classic/vendor/inter.css"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/dashboard/rescue/" {
			t.Errorf("%s: код %d location=%q, want 302 /dashboard/rescue/", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}
