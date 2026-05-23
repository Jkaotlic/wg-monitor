package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type fakeLookup struct {
	want string
	user *db.User
}

func (f *fakeLookup) GetByToken(raw string) (*db.User, error) {
	if raw == f.want {
		return f.user, nil
	}
	return nil, db.ErrUserNotFound
}

func TestAuthMiddleware_OK(t *testing.T) {
	l := &fakeLookup{want: "tok-abc", user: &db.User{ID: 7, Nickname: "vasya"}}
	mw := AuthMiddleware(l, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/report", nil)
	req.Header.Set("Authorization", "Bearer tok-abc")
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := NicknameFromContext(r.Context()); got != "vasya" {
			t.Fatalf("nick: %s", got)
		}
		if got := UserIDFromContext(r.Context()); got != 7 {
			t.Fatalf("uid: %d", got)
		}
		w.WriteHeader(204)
	})).ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("code: %d", rec.Code)
	}
}

func TestAuthMiddleware_Reject(t *testing.T) {
	l := &fakeLookup{want: "right"}
	mw := AuthMiddleware(l, nil)
	for _, hdr := range []string{"", "Bearer ", "Bearer wrong", "tok-abc"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/report", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		called := false
		mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(204)
		})).ServeHTTP(rec, req)
		if called {
			t.Fatalf("hdr %q: handler should not have been called", hdr)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("hdr %q: code %d", hdr, rec.Code)
		}
	}
}

func TestAuthMiddleware_RejectLogsTokenFingerprintOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{}))
	l := &fakeLookup{want: "right"}
	mw := AuthMiddleware(l, logger)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/report", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret-token")
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not have been called")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", rec.Code)
	}
	got := buf.String()
	sum := sha256.Sum256([]byte("wrong-secret-token"))
	wantPrefix := hex.EncodeToString(sum[:])[:12]
	for _, want := range []string{
		`reason=token-not-found`,
		`presented_len=18`,
		`presented_hash_prefix=` + wantPrefix,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "wrong-secret-token") {
		t.Fatalf("log leaked raw token: %q", got)
	}
}
