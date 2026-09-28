package exitprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchExitIPReadsTraceLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fl=1f\nh=1.1.1.1\nip=203.0.113.9\nts=1\n"))
	}))
	defer srv.Close()
	old := TraceURL
	TraceURL = srv.URL
	defer func() { TraceURL = old }()
	ip, err := FetchExitIP(context.Background(), srv.Client(), time.Second)
	if err != nil || ip != "203.0.113.9" {
		t.Fatalf("ip=%q err=%v", ip, err)
	}
}

func TestFetchExitIPWithoutIPLineIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>captive portal</html>"))
	}))
	defer srv.Close()
	old := TraceURL
	TraceURL = srv.URL
	defer func() { TraceURL = old }()
	if _, err := FetchExitIP(context.Background(), srv.Client(), time.Second); err == nil {
		t.Fatal("страница без ip= обязана быть ошибкой, а не пустым адресом")
	}
}
