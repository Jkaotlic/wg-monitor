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

func TestDashboardClassicStaysBehindSession(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "secret"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/classic/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/dashboard/classic/login" {
		t.Fatalf("без куки: код %d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/classic/login", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"/dashboard/classic/"`) {
		t.Fatalf("старая страница входа: код %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/classic/", nil)
	req.AddCookie(dashboardSessionCookie(req, "secret"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `src="./app.js"`) {
		t.Fatalf("с кукой: код %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/dashboard/classic/app.js", nil)
	req.AddCookie(dashboardSessionCookie(req, "secret"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"/dashboard/classic/login"`) {
		t.Fatalf("app.js: код %d, редирект входа не переведён на classic", rec.Code)
	}
}
