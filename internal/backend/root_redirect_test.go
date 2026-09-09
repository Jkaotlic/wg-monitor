package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The bare domain is what users type and what the Telegram button falls back
// to; without a handler on "/" the Go mux answers a bare "404 page not found",
// which reads as "the service is down". Mirror the /dashboard idiom instead.
func TestRootRedirectsToMiniapp(t *testing.T) {
	mux := NewMux(Deps{TelegramBotToken: "test-token"})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("GET / = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "/miniapp/" {
		t.Fatalf("Location = %q, want %q", got, "/miniapp/")
	}
}

// Guard against the classic ServeMux trap: registering "/" instead of "/{$}"
// makes the redirect a catch-all and silently swallows every genuine 404.
func TestUnknownPathStays404(t *testing.T) {
	mux := NewMux(Deps{TelegramBotToken: "test-token"})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no-such-page", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /no-such-page = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
