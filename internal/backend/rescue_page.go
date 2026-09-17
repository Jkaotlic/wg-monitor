package backend

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"errors"
	"net/http"
)

// Аварийная страница веб-управления (спека 2026-09-17-web-is-miniapp-cycle3):
// вход, сводка и раскатка бэкенда, когда приложение не грузится -- сломан
// бандл, упал JS, недоступны шрифты. Поэтому ни бандла, ни внешних ресурсов:
// разметка, стиль и скрипт лежат тремя файлами рядом и склеиваются в один
// ответ при старте. Файлы, а не const: JS пишется с шаблонными строками, а
// синтаксис проверяется node --check.
//
//go:embed rescue_static/index.html rescue_static/rescue.css rescue_static/rescue.js
var rescueStaticFS embed.FS

const (
	rescueCSSMark = "{{RESCUE_CSS}}"
	rescueJSMark  = "{{RESCUE_JS}}"
)

type rescuePage struct {
	body []byte
	csp  string
}

// rescueCSPHash -- источник для CSP: sha256 ровно тех байтов, что стоят между
// тегами. Браузер хэширует текст элемента как есть, поэтому склейка обязана
// вставлять файл без единого изменения.
func rescueCSPHash(b []byte) string {
	sum := sha256.Sum256(b)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// img-src data: -- только ради пустой иконки <link rel="icon" href="data:,">:
// без неё браузер сам запрашивает /favicon.ico, и default-src 'none'
// записывает нарушение CSP на каждом заходе.
func buildRescuePage(tmpl, css, js []byte) (rescuePage, error) {
	if bytes.Count(tmpl, []byte(rescueCSSMark)) != 1 || bytes.Count(tmpl, []byte(rescueJSMark)) != 1 {
		return rescuePage{}, errors.New("rescue: в index.html должно быть ровно по одной метке CSS и JS")
	}
	lowerCSS, lowerJS := bytes.ToLower(css), bytes.ToLower(js)
	if bytes.Contains(lowerCSS, []byte("</style")) || bytes.Contains(lowerCSS, []byte(rescueJSMark)) {
		return rescuePage{}, errors.New("rescue: rescue.css закрывает <style> или несёт метку")
	}
	if bytes.Contains(lowerJS, []byte("</script")) || bytes.Contains(lowerJS, []byte(rescueCSSMark)) {
		return rescuePage{}, errors.New("rescue: rescue.js закрывает <script> или несёт метку")
	}
	body := bytes.Replace(tmpl, []byte(rescueCSSMark), css, 1)
	body = bytes.Replace(body, []byte(rescueJSMark), js, 1)
	csp := "default-src 'none'; script-src " + rescueCSPHash(js) +
		"; style-src " + rescueCSPHash(css) +
		"; img-src data:; connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'"
	return rescuePage{body: body, csp: csp}, nil
}

func loadRescuePage() (rescuePage, error) {
	tmpl, err := rescueStaticFS.ReadFile("rescue_static/index.html")
	if err != nil {
		return rescuePage{}, err
	}
	css, err := rescueStaticFS.ReadFile("rescue_static/rescue.css")
	if err != nil {
		return rescuePage{}, err
	}
	js, err := rescueStaticFS.ReadFile("rescue_static/rescue.js")
	if err != nil {
		return rescuePage{}, err
	}
	return buildRescuePage(tmpl, css, js)
}

// rescuePageHandler собирает страницу один раз, при регистрации маршрутов:
// файлы вшиты в бинарь и до перезапуска не меняются. Страница данных не
// несёт -- без входа она та же, только показывает форму.
func rescuePageHandler() http.Handler {
	page, err := loadRescuePage()
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", page.csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write(page.body)
	})
}
