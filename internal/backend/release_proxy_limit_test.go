package backend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Раздача бинарей идёт через память: чтобы отличить «файл кончился раньше
// времени» от «файл слишком большой», ответ проверяется целиком до первого
// записанного байта. Значит, каждый параллельный скачивающий держит свой
// кусок памяти -- а на Raspberry Pi её мало.
//
// Вчера это выстрелило: перезапуски бэкенда раз за разом ставили в очередь
// self_update четырём роутерам сразу, и они пошли за бинарями одновременно.
// Одновременных раздач теперь не больше двух, остальным -- «занято, зайдите
// позже», а не общая нехватка памяти.
func TestReleaseProxy_LimitsConcurrentDownloads(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, maxConcurrentReleaseProxy+1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("binary"))
	}))
	defer upstream.Close()

	oldBase := releaseDownloadBase
	releaseDownloadBase = upstream.URL
	t.Cleanup(func() { releaseDownloadBase = oldBase })

	handler := releaseAssetProxyHandler(Deps{})
	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/v1/releases/download/v0.19.0/wg-monitor-agent-linux-arm64", nil)
		req.SetPathValue("version", "v0.19.0")
		req.SetPathValue("asset", "wg-monitor-agent-linux-arm64")
		return req
	}

	var wg sync.WaitGroup
	for i := 0; i < maxConcurrentReleaseProxy; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), newReq())
		}()
	}
	for i := 0; i < maxConcurrentReleaseProxy; i++ {
		<-started
	}

	// Все места заняты -- следующий получает вежливый отказ, а не ожидание.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newReq())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("статус %d, ждали 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("отказ без Retry-After: агенту нечем понять, когда возвращаться")
	}
	if !strings.Contains(rec.Body.String(), "busy") {
		t.Errorf("тело отказа: %q", rec.Body.String())
	}

	close(release)
	wg.Wait()

	// Место освободилось -- раздача снова работает.
	after := httptest.NewRecorder()
	handler.ServeHTTP(after, newReq())
	if after.Code != http.StatusOK {
		t.Fatalf("после освобождения статус %d", after.Code)
	}
	body, _ := io.ReadAll(after.Body)
	if string(body) != "binary" {
		t.Fatalf("тело: %q", body)
	}
}
