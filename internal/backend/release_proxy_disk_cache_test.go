package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func releaseCacheEnv(t *testing.T, body string, sums map[string]string) (http.HandlerFunc, *atomic.Int32, string) {
	t.Helper()
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	oldBase := releaseDownloadBase
	releaseDownloadBase = upstream.URL
	t.Cleanup(func() { releaseDownloadBase = oldBase })
	orig := verifiedChecksumsFetcher
	verifiedChecksumsFetcher = func(context.Context, string, string) (map[string]string, error) { return sums, nil }
	t.Cleanup(func() { verifiedChecksumsFetcher = orig })
	dir := t.TempDir()
	return releaseAssetProxyHandler(Deps{ReleaseCacheDir: dir}), &hits, dir
}

func releaseReq() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/releases/download/v0.46.0/wg-monitor-agent-linux-arm64", nil)
	req.SetPathValue("version", "v0.46.0")
	req.SetPathValue("asset", "wg-monitor-agent-linux-arm64")
	return req
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// SEC-01: раздача без авторизации и два слота до 90 с -- два медленных
// клиента стопорили раскатку парка. Проверенный по подписанным суммам бинарь
// кэшируется на диске и отдаётся оттуда без слота; слот -- только на поход
// за ним на GitHub.
func TestReleaseProxy_ServesVerifiedBinaryFromDiskWithoutSlot(t *testing.T) {
	h, hits, _ := releaseCacheEnv(t, "binary", map[string]string{"wg-monitor-agent-linux-arm64": sha("binary")})
	first := httptest.NewRecorder()
	h.ServeHTTP(first, releaseReq())
	if first.Code != http.StatusOK || first.Body.String() != "binary" {
		t.Fatalf("первый: %d %q", first.Code, first.Body.String())
	}
	// Оба слота заняты (медленные скачивающие) -- кэш отдаётся всё равно.
	for i := 0; i < maxConcurrentReleaseProxy; i++ {
		releaseProxySlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < maxConcurrentReleaseProxy; i++ {
			<-releaseProxySlots
		}
	}()
	second := httptest.NewRecorder()
	h.ServeHTTP(second, releaseReq())
	body, _ := io.ReadAll(second.Body)
	if second.Code != http.StatusOK || string(body) != "binary" {
		t.Fatalf("из кэша при занятых слотах: %d %q", second.Code, body)
	}
	if hits.Load() != 1 {
		t.Fatalf("на GitHub ходили %d раз, ждали 1", hits.Load())
	}
}

// Не сошлась сумма -- не отдаём и не кэшируем.
func TestReleaseProxy_RejectsBinaryWithWrongChecksum(t *testing.T) {
	h, _, dir := releaseCacheEnv(t, "tampered", map[string]string{"wg-monitor-agent-linux-arm64": sha("binary")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, releaseReq())
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("подменённый бинарь: %d %q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "v0.46.0", "wg-monitor-agent-linux-arm64")); !os.IsNotExist(err) {
		t.Fatalf("подменённый бинарь попал в кэш: %v", err)
	}
}

// Кэш вытесняет по последней отдаче, а не по времени скачивания: выпуск,
// который парк качает прямо сейчас, не выбрасывается ради свежескачанного.
func TestReleaseProxy_PruneKeepsRecentlyServedVersion(t *testing.T) {
	h, _, dir := releaseCacheEnv(t, "binary", map[string]string{"wg-monitor-agent-linux-arm64": sha("binary")})
	old := time.Now().Add(-48 * time.Hour)
	for i, v := range []string{"v0.40.0", "v0.41.0", "v0.42.0"} {
		p := filepath.Join(dir, v)
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "wg-monitor-agent-linux-arm64"), []byte("binary"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := old.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	serve := func(v string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/releases/download/"+v+"/wg-monitor-agent-linux-arm64", nil)
		req.SetPathValue("version", v)
		req.SetPathValue("asset", "wg-monitor-agent-linux-arm64")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := serve("v0.40.0"); c != http.StatusOK { // самый старый, но его только что качали
		t.Fatalf("из кэша: %d", c)
	}
	if c := serve("v0.46.0"); c != http.StatusOK { // новый -- скачивание и чистка
		t.Fatalf("новый: %d", c)
	}
	if _, err := os.Stat(filepath.Join(dir, "v0.40.0")); err != nil {
		t.Fatalf("вытеснен выпуск, который только что отдавали: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "v0.41.0")); !os.IsNotExist(err) {
		t.Fatalf("давно не отдававшийся выпуск остался: %v", err)
	}
}
