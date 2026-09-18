package actions

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Имя бэкенда не резолвится -- агент повторяет по запасному IP и там получает
// 503 «занято». Тип ответа обязан дожить до вызывающего: иначе агент не ждёт
// Retry-After, маркер release_proxy_busy не ставится и попытка сгорает
// (verify-done 18.09, спека A2).
func TestFallbackIPBusyKeepsHTTPErrorType(t *testing.T) {
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer busy.Close()
	// Основной клиент -- закрытый порт: транспортная ошибка, как у DNS-отказа.
	dead := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, errors.New("dial tcp: lookup backend: no such host") },
	}}
	ctx := context.Background()

	_, err := httpGetWithFallback(ctx, dead, busy.Client(), busy.URL+"/checksums.txt", "pinned IP")
	var he *selfUpdateHTTPError
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable || he.RetryAfter != "17" {
		t.Fatalf("503 запасного IP потерян в обёртке: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "agent.new")
	_, err = httpGetToFileWithFallback(ctx, dead, busy.Client(), busy.URL+"/bin", "pinned IP", dst, 1<<20)
	he = nil
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable {
		t.Fatalf("503 запасного IP на бинаре потерян в обёртке: %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatal("после отказа не должно остаться файла")
	}
}
