package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWizardAuth_MissingHeader_401(t *testing.T) {
	h := WizardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
	))
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestWizardAuth_WrongToken_401(t *testing.T) {
	h := WizardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
	))
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestWizardAuth_RightToken_200(t *testing.T) {
	h := WizardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
	))
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}
