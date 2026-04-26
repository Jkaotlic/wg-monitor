package backend

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
	mw := AuthMiddleware(l)
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
	mw := AuthMiddleware(l)
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

var _ = errors.New // import keeper
