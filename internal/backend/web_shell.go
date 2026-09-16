package backend

import (
	"bytes"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
)

// Веб-управление -- это мини-апп, открытый в обычном браузере (спека
// 2026-09-16-web-is-miniapp). Страница та же, что у Telegram, но без
// telegram-web-app.js: скрипт синхронный и внешний, а веб-управление нужнее
// всего ровно тогда, когда Telegram недоступен или заблокирован в сети --
// с этим тегом страница висела бы до таймаута. Какой режим включить, клиент
// решает по адресу (/dashboard), а не по наличию SDK.
var telegramSDKScript = regexp.MustCompile(`[ \t]*<script[^>]+telegram-web-app\.js[^>]*></script>\r?\n?`)

func webShellHTML(index []byte) ([]byte, bool) {
	loc := telegramSDKScript.FindIndex(index)
	if loc == nil {
		return index, false
	}
	out := make([]byte, 0, len(index))
	out = append(out, index[:loc[0]]...)
	out = append(out, index[loc[1]:]...)
	return bytes.Clone(out), true
}

// webShellHandler читает index.html бандла один раз: он вшит в бинарь и до
// перезапуска не меняется.
func webShellHandler(logger *slog.Logger) http.Handler {
	raw, err := fs.ReadFile(miniappStaticFS, "miniapp_static/index.html")
	if err != nil {
		panic(err)
	}
	page, found := webShellHTML(raw)
	if !found && logger != nil {
		logger.Warn("веб-управление: в index.html не найден скрипт Telegram -- отдаю страницу как есть")
	}
	return staticCacheHeadersForPage(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	}))
}
