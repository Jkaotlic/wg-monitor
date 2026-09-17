package backend

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

func getRescue(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// Хэш CSP считается здесь заново, из ОТДАННОГО тела, а не функцией сервера:
// иначе тест сверял бы код сам с собой, и расхождение байтов между файлом и
// ответом (лишний перевод строки при склейке) браузер поймал бы первым.
func cspHashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestRescuePageServedWithoutSessionAsLoginState(t *testing.T) {
	old := serverVersion
	SetVersion("v9.8.7")
	t.Cleanup(func() { SetVersion(old) })
	h := NewMux(Deps{DashboardToken: "secret"})
	rec := getRescue(t, h, "/dashboard/rescue/")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type=%q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control=%q, want no-store", cc)
	}
	if xf := rec.Header().Get("X-Frame-Options"); xf != "DENY" {
		t.Errorf("X-Frame-Options=%q", xf)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"wg-monitor · аварийная страница",
		`href="/dashboard/"`,
		`id="login-form"`,
		"Токен доступа",
		`<div id="app" hidden>`,
		"Раскатить",
		"Это откат — разрешить",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на странице нет %q", want)
		}
	}
	// Данных сводки в разметке нет: их приносит fetch после входа.
	for _, leak := range []string{`"totals"`, `"agents"`, "v9.8.7"} {
		if strings.Contains(body, leak) {
			t.Errorf("страница без входа несёт данные: %q", leak)
		}
	}
}

func TestRescuePageCSPHashesMatchInlineBlocks(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "secret"})
	rec := getRescue(t, h, "/dashboard/rescue/")
	body := rec.Body.String()
	if n := strings.Count(body, "<script"); n != 1 {
		t.Fatalf("<script встречается %d раз, want 1", n)
	}
	if n := strings.Count(body, "<style"); n != 1 {
		t.Fatalf("<style встречается %d раз, want 1", n)
	}
	script := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindStringSubmatch(body)
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(body)
	if script == nil || style == nil {
		t.Fatal("блоки <script>/<style> без атрибутов не найдены")
	}
	want := "default-src 'none'; script-src " + cspHashOf(script[1]) +
		"; style-src " + cspHashOf(style[1]) +
		"; img-src data:; connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'"
	if got := rec.Header().Get("Content-Security-Policy"); got != want {
		t.Fatalf("CSP:\n got %s\nwant %s", got, want)
	}
}

// Страница нужна, когда приложение не грузится: ни внешних адресов, ни
// бандла, ни того, что CSP на хэшах всё равно заблокирует.
func TestRescuePageIsSelfContained(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "secret"})
	body := getRescue(t, h, "/dashboard/rescue/").Body.String()
	// Единственный <link> -- пустая иконка data:, иначе браузер сам сходит
	// за /favicon.ico и CSP запишет нарушение. Ради неё в CSP img-src data:.
	if n := strings.Count(body, "<link"); n != 1 || !strings.Contains(body, `<link rel="icon" href="data:,">`) {
		t.Errorf("<link> на странице: %d, want ровно пустую иконку data:", n)
	}
	for _, bad := range []string{"http://", "https://", "/miniapp/assets", "<img", "url(", "@import", "innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(body, bad) {
			t.Errorf("на странице %q", bad)
		}
	}
	if m := regexp.MustCompile(`\sstyle="`).FindString(body); m != "" {
		t.Error("атрибут style= заблокирует CSP")
	}
	if m := regexp.MustCompile(`\son[a-z]+="`).FindString(body); m != "" {
		t.Errorf("обработчик в атрибуте %q заблокирует CSP", m)
	}
}

func TestRescuePageRedirectsAndGating(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "secret"})
	rec := getRescue(t, h, "/dashboard/rescue")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/dashboard/rescue/" {
		t.Fatalf("/dashboard/rescue: код %d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	// Приложение на своих адресах осталось оболочкой.
	for _, path := range []string{"/dashboard/", "/dashboard/login"} {
		rec := getRescue(t, h, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `/miniapp/assets/`) {
			t.Errorf("%s: код %d, не оболочка приложения", path, rec.Code)
		}
	}
	// Без токена дашборда веб-управления нет вовсе -- и аварийной страницы тоже.
	off := NewMux(Deps{})
	if rec := getRescue(t, off, "/dashboard/rescue/"); rec.Code == http.StatusOK {
		t.Fatalf("без DashboardToken страница отдаётся: код %d", rec.Code)
	}
}

// Цвета страницы -- копия токенов приложения: разъехаться молча они не могут.
func TestRescueCSSTokensMatchMiniappStyle(t *testing.T) {
	app, err := os.ReadFile("../../miniapp/src/style.css")
	if err != nil {
		t.Fatalf("style.css приложения: %v", err)
	}
	css, err := rescueStaticFS.ReadFile("rescue_static/rescue.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bg", "surf", "surf2", "ink", "dim", "line", "sig", "ok", "warn", "bad", "on-sig"} {
		re := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*([^;]+);`)
		a := re.FindSubmatch(app)
		r := re.FindSubmatch(css)
		if a == nil || r == nil {
			t.Errorf("--%s: в приложении %v, на странице %v", name, a != nil, r != nil)
			continue
		}
		if strings.TrimSpace(string(a[1])) != strings.TrimSpace(string(r[1])) {
			t.Errorf("--%s: приложение %s, страница %s", name, a[1], r[1])
		}
	}
}

func TestBuildRescuePageRejectsBrokenInput(t *testing.T) {
	tmpl := []byte("<style>{{RESCUE_CSS}}</style><script>{{RESCUE_JS}}</script>")
	if _, err := buildRescuePage(tmpl, []byte("a{}"), []byte("x()</script>")); err == nil {
		t.Error("JS с </script> принят")
	}
	if _, err := buildRescuePage(tmpl, []byte("a{}</style>"), []byte("x()")); err == nil {
		t.Error("CSS с </style> принят")
	}
	if _, err := buildRescuePage([]byte("<style></style>"), []byte("a{}"), []byte("x()")); err == nil {
		t.Error("шаблон без меток принят")
	}
	page, err := buildRescuePage(tmpl, []byte("a{}"), []byte("x()"))
	if err != nil {
		t.Fatal(err)
	}
	if string(page.body) != "<style>a{}</style><script>x()</script>" {
		t.Fatalf("склейка: %s", page.body)
	}
	if !strings.Contains(page.csp, "script-src "+cspHashOf("x()")+";") {
		t.Fatalf("csp: %s", page.csp)
	}
}
