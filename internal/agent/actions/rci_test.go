package actions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRCIClient_PostsBodyAndReturnsAnswer(t *testing.T) {
	var gotMethod, gotPath, gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotCT = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		buf := make([]byte, 16)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		_, _ = w.Write([]byte(`{"continued": true}`))
	}))
	defer srv.Close()
	out, err := newRCIClient(srv.URL)(context.Background(), http.MethodPost, "/rci/components/commit", []byte(`{}`))
	if err != nil {
		t.Fatalf("rci: %v", err)
	}
	if string(out) != `{"continued": true}` {
		t.Fatalf("out = %q", out)
	}
	if gotMethod != "POST" || gotPath != "/rci/components/commit" || gotBody != "{}" || gotCT != "application/json" {
		t.Fatalf("запрос: %s %s %q %q", gotMethod, gotPath, gotBody, gotCT)
	}
}

// Неизвестный путь RCI отвечает 404 с пустым телом (роутер, 02.10) -- только
// это и закрытый порт значат «RCI нет». 401/403/405 -- ответ роутера: отказ.
func TestRCIClient_Only404IsUnreachable(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		_, err := newRCIClient(srv.URL)(context.Background(), http.MethodPost, "/rci/components/list", []byte(`{}`))
		srv.Close()
		if err == nil {
			t.Fatalf("HTTP %d: ошибки нет", code)
		}
		if got, want := errors.Is(err, ErrRCIUnreachable), code == http.StatusNotFound; got != want {
			t.Fatalf("HTTP %d: unreachable=%v, err = %v", code, got, err)
		}
		if got, want := errors.Is(err, ErrRCIRejected), code != http.StatusNotFound; got != want {
			t.Fatalf("HTTP %d: rejected=%v, err = %v", code, got, err)
		}
	}
}

func TestRCIClient_CancelledContextIsNotUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newRCIClient(base)(ctx, http.MethodPost, "/rci/components/list", []byte(`{}`))
	if err == nil || errors.Is(err, ErrRCIUnreachable) {
		t.Fatalf("отменённый запрос выдан за «RCI нет»: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRCIClient_ConnectionRefusedIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // порт закрыт -- отказ в соединении
	_, err := newRCIClient(base)(context.Background(), http.MethodPost, "/rci/components/list", []byte(`{}`))
	if !errors.Is(err, ErrRCIUnreachable) {
		t.Fatalf("err = %v, ждали ErrRCIUnreachable", err)
	}
}

func TestRCIClient_ServerErrorIsNotUnreachableAndCarriesNoBody(t *testing.T) {
	secret := strings.Repeat("S", 5000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(secret))
	}))
	defer srv.Close()
	_, err := newRCIClient(srv.URL)(context.Background(), http.MethodPost, "/rci/components/list", []byte(`{}`))
	if err == nil || errors.Is(err, ErrRCIUnreachable) {
		t.Fatalf("err = %v", err)
	}
	if len(err.Error()) > 300 {
		t.Fatalf("ошибка длиной %d -- тело утекло", len(err.Error()))
	}
}

func TestRCIClient_CapsAnswerSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("x", 64*1024))
		for i := 0; i < (rciMaxAnswerBytes/len(chunk))+2; i++ {
			_, _ = w.Write(chunk)
		}
	}))
	defer srv.Close()
	out, err := newRCIClient(srv.URL)(context.Background(), http.MethodPost, "/rci/components/list", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("len(out)=%d err=%v", len(out), err)
	}
	if len(err.Error()) > 300 {
		t.Fatalf("ошибка длиной %d -- тело утекло", len(err.Error()))
	}
}

func TestRCIClient_DoesNotFollowRedirects(t *testing.T) {
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/rci/components/list", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := newRCIClient(srv.URL)(context.Background(), http.MethodPost, "/rci/components/list", []byte(`{}`))
	if err == nil {
		t.Fatal("ждали ошибку на перенаправление")
	}
	if hit {
		t.Fatal("клиент ушёл по перенаправлению")
	}
}

func TestRCIClient_RefusesPathsOutsideRCI(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	for _, p := range []string{"/auth", "rci/components/list", "/rci/../auth", "//example.com/rci/x", "/rci/x?a=b", "/rci/x#y"} {
		if _, err := newRCIClient(srv.URL)(context.Background(), http.MethodPost, p, nil); err == nil {
			t.Fatalf("путь %q принят", p)
		}
	}
	if hit {
		t.Fatal("запрос ушёл")
	}
}

func TestDefaultRCIBaseIsLoopback(t *testing.T) {
	if rciLoopbackBase != "http://127.0.0.1:79" {
		t.Fatalf("rciLoopbackBase = %q", rciLoopbackBase)
	}
}
