package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDashboardRoutesAbsentWhenTokenEmpty(t *testing.T) {
	h := NewMux(Deps{})
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardAuth_MissingHeader_401(t *testing.T) {
	h := DashboardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestDashboardAuth_WrongToken_401(t *testing.T) {
	h := DashboardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestDashboardAuth_RightToken_200(t *testing.T) {
	h := DashboardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestDashboardSummaryRouteRequiresBearerToken(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "secret"})

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: want 401, got %d", missing.Code)
	}

	okReq := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	okReq.Header.Set("Authorization", "Bearer secret")
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, okReq)
	if ok.Code != http.StatusOK {
		t.Fatalf("authorized: want 200, got %d body=%s", ok.Code, ok.Body.String())
	}
}
