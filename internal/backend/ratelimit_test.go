package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimitMiddlewareLimitsMissingUserIDWithGlobalBucket(t *testing.T) {
	limiter := newUserRateLimiter(0.01, 1)
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }

	hits := 0
	handler := RateLimitMiddleware(limiter, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/report", nil)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, req)
	if first.Code != http.StatusNoContent {
		t.Fatalf("first status=%d", first.Code)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, req)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d, want 429", second.Code)
	}
	if hits != 1 {
		t.Fatalf("handler hits=%d, want 1", hits)
	}
}
