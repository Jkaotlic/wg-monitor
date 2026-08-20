package backend

import (
	"net/http"
	"regexp"
	"strings"
)

// hashedAssetRe -- имя файла со сборочным хешем: index-BckuXIbO.js. Такой
// файл иммутабелен по построению: меняется содержимое -- меняется имя.
var hashedAssetRe = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.(js|css|woff2?|svg|png|jpg|jpeg|webp|ico)$`)

// staticCacheHeaders расставляет кеширование по роли файла.
//
// Оболочка (index.html и всё, что не несёт хеша в имени) обязана
// перепроверяться при каждом открытии: она ссылается на хешированные файлы, и
// пока браузер держит старую оболочку, он тянет и старые скрипты -- сколько
// бэкенд ни обновляй. Найдено на живом стенде 20.08.2026, когда обновление
// не показалось ни в дашборде, ни в мини-аппе.
//
// Хешированные файлы, наоборот, живут в кеше долго: их имя меняется вместе с
// содержимым, и перепроверять их на каждом заходе -- это лишний круг по сети
// на телефоне, ради которого всё и затевалось.
func staticCacheHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hashedAssetRe.MatchString(r.URL.Path) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		next.ServeHTTP(w, r)
	})
}

// staticCacheHeadersForPage -- то же для одиночной страницы, у которой нет
// файлового пути (страница входа в дашборд собирается в коде).
func staticCacheHeadersForPage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-cache") {
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		next.ServeHTTP(w, r)
	})
}
