package backend

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Оболочка приложения (index.html) обязана перепроверяться при каждом
// открытии, а файлы с хешем в имени -- нет.
//
// Найдено на живом стенде 20.08.2026: после обновления бэкенда оператор не
// увидел новый экран. Оболочка кешируется браузером и вебвью Telegram по
// эвристике (Last-Modified), а раз index.html старый -- он тянет и старые
// assets, сколько бэкенд ни обновляй. Явный no-cache на оболочке эту петлю
// разрывает, а иммутабельный кеш на хешированных файлах оставляет им
// нормальный срок жизни: их имя меняется вместе с содержимым.
func TestStaticShellIsRevalidatedAndHashedAssetsAreCached(t *testing.T) {
	h := NewMux(Deps{TelegramBotToken: "test-bot-token", DashboardToken: "dash-token"})

	shells := []string{"/miniapp/", "/dashboard/login"}
	for _, path := range shells {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: код %d", path, rec.Code)
		}
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-cache") {
			t.Errorf("%s: Cache-Control = %q, оболочка обязана перепроверяться", path, cc)
		}
	}
}

func TestStaticHashedAssetKeepsLongCache(t *testing.T) {
	h := NewMux(Deps{TelegramBotToken: "test-bot-token"})

	// Берём НАСТОЯЩИЙ файл из собранной оболочки: имя с хешем генерирует
	// сборка, и выдуманное имя проверило бы только 404.
	entries, err := fs.ReadDir(miniappStaticFS, "miniapp_static/assets")
	if err != nil || len(entries) == 0 {
		t.Skip("собранной оболочки мини-аппа нет -- проверять нечего")
	}
	asset := ""
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") {
			asset = e.Name()
			break
		}
	}
	if asset == "" {
		t.Skip("в сборке нет js-файла")
	}

	req := httptest.NewRequest(http.MethodGet, "/miniapp/assets/"+asset, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: код %d", asset, rec.Code)
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, хешированный файл может лежать в кеше долго", cc)
	}
}
