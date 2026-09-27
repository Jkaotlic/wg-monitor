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
