package revive

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
)

type panelHit struct {
	path, auth, cookie string
}

func panel(t *testing.T, status int) (*httptest.Server, func() []panelHit) {
	t.Helper()
	var (
		mu   sync.Mutex
		hits []panelHit
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, panelHit{path: r.URL.Path, auth: r.Header.Get("Authorization"), cookie: r.Header.Get("Cookie")})
		mu.Unlock()
		if status == http.StatusFound {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []panelHit {
		mu.Lock()
		defer mu.Unlock()
		return append([]panelHit(nil), hits...)
	}
}

func dialFails(err error) *http.Transport {
	return &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, err }}
}

func TestProbe_StatusCodes(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusOK, awgmstate.Reachable},
		{http.StatusUnauthorized, awgmstate.Reachable}, // панель ответила -- роутер жив
		{http.StatusForbidden, awgmstate.Reachable},
		{http.StatusNotFound, awgmstate.Reachable},
		{http.StatusFound, awgmstate.Reachable},
		{http.StatusBadGateway, awgmstate.Offline},
		{http.StatusServiceUnavailable, awgmstate.Offline},
		{http.StatusGatewayTimeout, awgmstate.Offline},
	}
	for _, c := range cases {
		srv, hits := panel(t, c.status)
		got := NewProber(time.Second).Probe(context.Background(), srv.URL+"/")
		if got != c.want {
			t.Fatalf("HTTP %d: %q, want %q", c.status, got, c.want)
		}
		h := hits()
		if len(h) != 1 {
			t.Fatalf("HTTP %d: обращений %d, ждали ровно одно (без переходов по редиректу)", c.status, len(h))
		}
		if h[0].path != "/api/system/info" {
			t.Fatalf("путь %q", h[0].path)
		}
		if h[0].auth != "" || h[0].cookie != "" {
			t.Fatalf("опрос обязан идти без учётных данных: auth=%q cookie=%q", h[0].auth, h[0].cookie)
		}
	}
}

func TestProbe_ConnectionRefusedIsOffline(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	if got := NewProber(time.Second).Probe(context.Background(), u); got != awgmstate.Offline {
		t.Fatalf("закрытый порт: %q", got)
	}
}

func TestProbe_TimeoutIsOffline(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	t.Cleanup(func() { close(block); srv.Close() })
	if got := NewProber(50*time.Millisecond).Probe(context.Background(), srv.URL); got != awgmstate.Offline {
		t.Fatalf("таймаут: %q", got)
	}
}

func TestProbe_UntrustedCertificateIsTLSError(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	if got := NewProber(time.Second).Probe(context.Background(), srv.URL); got != awgmstate.TLSError {
		t.Fatalf("чужой сертификат: %q", got)
	}
}

// Имя хоста с «tls» не сбивает классификацию: DNS-ошибка распознаётся по типу.
func TestProbe_UnknownHostIsDNSErrorEvenWithTLSInName(t *testing.T) {
	p := NewProber(time.Second)
	p.Client.Transport = dialFails(&net.DNSError{Err: "no such host", Name: "tls-router.example.com", IsNotFound: true})
	if got := p.Probe(context.Background(), "https://tls-router.example.com"); got != awgmstate.DNSError {
		t.Fatalf("неизвестное имя: %q", got)
	}
}

// Отказ соединения к хосту с «tls» в имени -- offline: адрес из текста
// url.Error в классификацию не попадает.
func TestProbe_RefusedWithTLSInHostnameIsOffline(t *testing.T) {
	p := NewProber(time.Second)
	p.Client.Transport = dialFails(&net.OpError{
		Op: "dial", Net: "tcp",
		Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443},
		Err:  &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
	})
	if got := p.Probe(context.Background(), "https://tls-router.example.com"); got != awgmstate.Offline {
		t.Fatalf("got %q, want offline", got)
	}
}

func TestProbe_BadURLIsOffline(t *testing.T) {
	if got := NewProber(time.Second).Probe(context.Background(), "::not a url"); got != awgmstate.Offline {
		t.Fatalf("got %q", got)
	}
}
