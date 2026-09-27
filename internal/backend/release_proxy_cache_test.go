package backend

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Прод, 18.09: все 19 провалов раскатки -- 503 на крошечные checksums.txt и
// .sig, пока два слота держали перекачку бинарей. Подпись и суммы слота не
// занимают: они маленькие, одинаковые для всех и отдаются из памяти.
func TestReleaseProxy_ChecksumsServedWhileBinarySlotsBusy(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, maxConcurrentReleaseProxy+1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = w.Write([]byte("abc  wg-monitor-agent-linux-arm64\n"))
		case strings.HasSuffix(r.URL.Path, "/checksums.txt.sig"):
			_, _ = w.Write([]byte("sig"))
		default:
			started <- struct{}{}
			<-release
			_, _ = w.Write([]byte("binary"))
		}
	}))
	defer upstream.Close()
	swapReleaseDownloadBase(t, upstream.URL)

	handler := releaseAssetProxyHandler(Deps{})
	var wg sync.WaitGroup
	for i := 0; i < maxConcurrentReleaseProxy; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), releaseProxyReq("v0.45.0", "wg-monitor-agent-linux-arm64"))
		}()
	}
	for i := 0; i < maxConcurrentReleaseProxy; i++ {
		<-started
	}

	for _, asset := range []string{"checksums.txt", "checksums.txt.sig"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, releaseProxyReq("v0.45.0", asset))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s при занятых слотах: статус %d, тело %q", asset, rec.Code, rec.Body.String())
		}
	}
	// Бинарь по-прежнему упирается в слот.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, releaseProxyReq("v0.45.0", "wg-monitor-agent-linux-mipsle"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("бинарь при занятых слотах: статус %d, ждали 503", rec.Code)
	}
	close(release)
	wg.Wait()
}

// Одиннадцать роутеров разом -- один поход на GitHub: остальные ждут того же
// ответа и получают его из памяти.
func TestReleaseProxy_ChecksumsFetchedOnceForConcurrentCallers(t *testing.T) {
	var hits atomic.Int32
	gate := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-gate
		_, _ = w.Write([]byte("sums"))
	}))
	defer upstream.Close()
	swapReleaseDownloadBase(t, upstream.URL)

	handler := releaseAssetProxyHandler(Deps{})
	const callers = 11
	var wg sync.WaitGroup
	codes := make([]int, callers)
	bodies := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, releaseProxyReq("v0.45.1", "checksums.txt"))
			codes[i], bodies[i] = rec.Code, rec.Body.String()
		}(i)
	}
	// Даём всем войти в ожидание, затем отпускаем единственный запрос.
	for hits.Load() == 0 {
		runtime.Gosched()
	}
	close(gate)
	wg.Wait()
	for i := range codes {
		if codes[i] != http.StatusOK || bodies[i] != "sums" {
			t.Fatalf("вызов %d: статус %d тело %q", i, codes[i], bodies[i])
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, releaseProxyReq("v0.45.1", "checksums.txt"))
	if rec.Code != http.StatusOK || rec.Body.String() != "sums" {
		t.Fatalf("повтор из памяти: %d %q", rec.Code, rec.Body.String())
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("походов на GitHub %d, ждали 1", n)
	}
}

// Ошибку не запоминаем: следующий роутер обязан получить шанс на удачу.
func TestReleaseProxy_ChecksumsFailureNotCached(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("sums"))
	}))
	defer upstream.Close()
	swapReleaseDownloadBase(t, upstream.URL)

	handler := releaseAssetProxyHandler(Deps{})
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, releaseProxyReq("v0.45.2", "checksums.txt"))
	if first.Code != http.StatusBadGateway {
		t.Fatalf("первый: статус %d, ждали 502", first.Code)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, releaseProxyReq("v0.45.2", "checksums.txt"))
	if second.Code != http.StatusOK || second.Body.String() != "sums" {
		t.Fatalf("второй: %d %q", second.Code, second.Body.String())
	}
}

// Лимиты размера прежние: подпись больше 8 КиБ -- не подпись.
func TestReleaseProxy_ChecksumsSizeLimits(t *testing.T) {
	cases := []struct {
		asset string
		size  int64
	}{
		{"checksums.txt", maxVerifiedChecksumsBytes + 1},
		{"checksums.txt.sig", maxVerifiedChecksumsSigBytes + 1},
	}
	for _, c := range cases {
		t.Run(c.asset, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat("x", int(c.size))))
			}))
			defer upstream.Close()
			swapReleaseDownloadBase(t, upstream.URL)
			rec := httptest.NewRecorder()
			releaseAssetProxyHandler(Deps{}).ServeHTTP(rec, releaseProxyReq("v0.45.3", c.asset))
			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "too large") {
				t.Fatalf("статус %d тело %q", rec.Code, rec.Body.String())
			}
		})
	}
}

// Кэш ограничен: старые версии вытесняются, память не растёт с каждым выпуском.
func TestReleaseProxy_SmallAssetCacheBounded(t *testing.T) {
	c := newReleaseSmallAssetCache(3)
	for i := 0; i < 10; i++ {
		c.put("k"+strconv.Itoa(i), releaseSmallAsset{body: []byte("x")})
	}
	if n := c.len(); n != 3 {
		t.Fatalf("записей %d, ждали 3", n)
	}
	if _, ok := c.get("k9"); !ok {
		t.Fatal("свежая запись вытеснена")
	}
	if _, ok := c.get("k0"); ok {
		t.Fatal("старая запись не вытеснена")
	}
}

// Retry-After с разбросом: одинаковое «30» у всех возвращало толпу разом.
func TestReleaseProxy_BusyRetryAfterJittered(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		n, err := strconv.Atoi(releaseProxyRetryAfter())
		if err != nil {
			t.Fatal(err)
		}
		if n < 10 || n > 40 {
			t.Fatalf("Retry-After %d вне 10..40", n)
		}
		seen[n] = true
	}
	if len(seen) < 5 {
		t.Fatalf("разброса нет: %v", seen)
	}
}

func releaseProxyReq(version, asset string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/releases/download/"+version+"/"+asset, nil)
	req.SetPathValue("version", version)
	req.SetPathValue("asset", asset)
	return req
}

func swapReleaseDownloadBase(t *testing.T, base string) {
	t.Helper()
	old := releaseDownloadBase
	releaseDownloadBase = base
	t.Cleanup(func() { releaseDownloadBase = old })
}
