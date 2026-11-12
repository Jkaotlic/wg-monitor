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

// SEC-02: дедуп мелких файлов -- по ключу, поэтому РАЗНЫЕ теги раньше
// запускали неограниченную пачку параллельных походов на GitHub (по одному на
// версию). Теперь походы за суммами делят те же два места, что и бинари:
// третьему -- вежливый отказ.
func TestReleaseProxy_SmallFetchesGloballyCapped(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, maxConcurrentReleaseProxy+1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte("sums"))
	}))
	defer upstream.Close()
	swapReleaseDownloadBase(t, upstream.URL)

	handler := releaseAssetProxyHandler(Deps{})
	versions := []string{"v0.70.0", "v0.70.1", "v0.70.2"}
	var wg sync.WaitGroup
	for i := 0; i < maxConcurrentReleaseProxy; i++ {
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), releaseProxyReq(v, "checksums.txt"))
		}(versions[i])
	}
	for i := 0; i < maxConcurrentReleaseProxy; i++ {
		<-started
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, releaseProxyReq(versions[2], "checksums.txt"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("третий параллельный fetch: статус %d, ждали 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("отказ без Retry-After")
	}

	close(release)
	wg.Wait()
}

// SEC-02: кэш бинарей держит три версии, слота два -- перебор старых тегов
// держал оба места занятыми и останавливал раскатку парку. Промахи кэша
// (поход на GitHub) теперь лимитированы по адресу; попадания в кэш --
// бесплатны, чтобы не штрафовать парк за чужой перебор.
func TestReleaseProxy_MissRateLimitedPerRemote(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("sums"))
	}))
	defer upstream.Close()
	swapReleaseDownloadBase(t, upstream.URL)

	limiter := newRemoteRateLimiter(0.001, 2) // 2 промаха подряд, дальше -- жди
	handler := releaseAssetProxyHandler(Deps{ReleaseProxyLimiter: limiter})

	for i, version := range []string{"v0.71.0", "v0.71.1", "v0.71.2"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, releaseProxyReq(version, "checksums.txt"))
		if i < 2 && rec.Code != http.StatusOK {
			t.Fatalf("%s: статус %d, ждали 200 (в пределах всплеска)", version, rec.Code)
		}
		if i == 2 {
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("%s: статус %d, ждали 429", version, rec.Code)
			}
			if rec.Header().Get("Retry-After") == "" {
				t.Error("429 без Retry-After")
			}
		}
	}
	// Попадание в кэш лимит не тратит: повтор первой версии свободен.
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, releaseProxyReq("v0.71.0", "checksums.txt"))
		if rec.Code != http.StatusOK {
			t.Fatalf("повтор из кэша: статус %d, ждали 200", rec.Code)
		}
	}
}
