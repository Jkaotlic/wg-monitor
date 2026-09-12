package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRemoteRateLimiterKeysByAddressNotByPort(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	if ok, _ := l.Allow("198.51.100.7:40001"); !ok {
		t.Fatal("первая попытка с адреса обязана пройти")
	}
	// Тот же адрес, другой исходящий порт -- тот же перебор. Порт у
	// подбирающего меняется каждым запросом, и ключ по нему не ограничил бы
	// ничего вовсе.
	ok, retry := l.Allow("198.51.100.7:40002")
	if ok {
		t.Fatal("вторая попытка с того же адреса должна упереться в лимит")
	}
	if retry <= 0 {
		t.Fatalf("retryAfter = %v, want положительное ожидание", retry)
	}
}

func TestRemoteRateLimiterSeparatesDifferentAddresses(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	if ok, _ := l.Allow("198.51.100.7:40001"); !ok {
		t.Fatal("первый адрес: первая попытка обязана пройти")
	}
	if ok, _ := l.Allow("203.0.113.9:40001"); !ok {
		t.Fatal("сосед не должен страдать от чужого перебора")
	}
}

func TestRemoteRateLimiterRefillsOverTime(t *testing.T) {
	l := newRemoteRateLimiter(1, 1) // один запрос в секунду
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	if ok, _ := l.Allow("198.51.100.7:40001"); !ok {
		t.Fatal("первая попытка обязана пройти")
	}
	if ok, _ := l.Allow("198.51.100.7:40001"); ok {
		t.Fatal("вторая попытка подряд должна упереться в лимит")
	}
	now = now.Add(2 * time.Second)
	if ok, _ := l.Allow("198.51.100.7:40001"); !ok {
		t.Fatal("через две секунды человек обязан войти снова")
	}
}

// Лимитер стоит на публичных входах, и ключ у него -- чужой адрес. Значит
// карта ведёрок не имеет права расти от перебора: иначе защита от подбора
// становится способом съесть память бэкенда.
func TestRemoteRateLimiterForgetsRefilledAddresses(t *testing.T) {
	l := newRemoteRateLimiter(1, 1)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	for i := 0; i < remoteRateLimiterMaxBuckets+50; i++ {
		l.Allow(testRemoteAddr(i))
	}
	// Сутки спустя все ведёрки полны -- помнить о них нечего.
	now = now.Add(24 * time.Hour)
	l.Allow("198.51.100.7:40001")
	if got := l.size(); got > remoteRateLimiterMaxBuckets {
		t.Fatalf("ведёрок в памяти = %d, want не больше %d", got, remoteRateLimiterMaxBuckets)
	}
}

func TestRemoteRateLimitMiddlewareSaysRetryAfterAndSpeaksWords(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	hits := 0
	h := remoteRateLimitMiddleware(l, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", nil)
	req.RemoteAddr = "198.51.100.7:40001"
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	if first.Code != http.StatusNoContent {
		t.Fatalf("первый запрос = %d, want 204", first.Code)
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("второй запрос = %d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Error("429 обязан сказать, через сколько пробовать снова")
	}
	if hits != 1 {
		t.Fatalf("хендлер вызван %d раз, want 1", hits)
	}
}

// nil-лимитер -- это «ограничение выключено», а не паника на каждом запросе.
func TestRemoteRateLimiterNilIsPassThrough(t *testing.T) {
	var l *remoteRateLimiter
	if ok, _ := l.Allow("198.51.100.7:40001"); !ok {
		t.Fatal("выключенный лимитер обязан пропускать")
	}
	hits := 0
	h := remoteRateLimitMiddleware(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || hits != 1 {
		t.Fatalf("выключенный лимитер: code=%d hits=%d", rec.Code, hits)
	}
}

func testRemoteAddr(i int) string {
	return "203.0.113." + itoaSmall(i%256) + ":" + itoaSmall(40000+i%1000)
}

func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
