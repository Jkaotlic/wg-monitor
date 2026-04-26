package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddleware_RejectsMissingHeader(t *testing.T) {
	tokens := map[string]string{"deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe": "testkeen"}
	mw := AuthMiddleware(tokens)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("POST", "/v1/report", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code: got %d want 401", rec.Code)
	}
	if called {
		t.Error("inner handler must not be called on bad auth")
	}
}

func TestAuthMiddleware_RejectsBadToken(t *testing.T) {
	tokens := map[string]string{"deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe": "testkeen"}
	mw := AuthMiddleware(tokens)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("POST", "/v1/report", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code: got %d want 401", rec.Code)
	}
}

func TestAuthMiddleware_AcceptsValidToken_AttachesNickname(t *testing.T) {
	const token = "deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe"
	tokens := map[string]string{token: "testkeen"}
	mw := AuthMiddleware(tokens)
	var gotNick string
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNick = NicknameFromContext(r.Context())
	}))
	req := httptest.NewRequest("POST", "/v1/report", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("code: got %d want 200", rec.Code)
	}
	if gotNick != "testkeen" {
		t.Errorf("nickname: got %q want testkeen", gotNick)
	}
}

func TestAuthMiddleware_RejectsMalformedHeader(t *testing.T) {
	const token = "deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe"
	tokens := map[string]string{token: "testkeen"}
	mw := AuthMiddleware(tokens)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	cases := []string{"Bearer", "", "Basic " + token, "Bearer  " + token, "bearer " + token}
	for _, hdr := range cases {
		req := httptest.NewRequest("POST", "/v1/report", nil)
		req.Header.Set("Authorization", hdr)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("hdr=%q: code %d want 401", hdr, rec.Code)
		}
	}
}
