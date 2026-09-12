package backend

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// floodAddr -- адреса для залива карты ведёрок. IPv6 из документационного
// диапазона 2001:db8::/32: их бесплатно бесконечно много, и именно так
// выглядит настоящий залив -- у владельца одного /64 адресов больше, чем у
// нас памяти. Прежний помощник давал всего 256 разных хостов, поэтому карта
// в тесте не переполнялась вовсе и вытеснение не проверялось ни разу.
func floodAddr(i int) string {
	return fmt.Sprintf("[2001:db8::%x]:40001", i)
}

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

// Обход вытеснением: подбирающий заливает карту чужими адресами, чтобы его
// собственное пустое ведёрко выкинули и счёт попыток начался заново.
//
// Залив стоит ему ничего, поэтому жертву вытеснение обязано выбирать не
// произвольно: уходить должны ПОЛНЫЕ ведёрки, которым лимит и так ничего не
// помнит, а наказанное -- оставаться до последнего.
func TestRemoteRateLimiterKeepsPenaltyThroughFlood(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 2) // один токен раз в сто секунд
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	const hammer = "198.51.100.7:40001"

	l.Allow(hammer)
	l.Allow(hammer)
	if ok, _ := l.Allow(hammer); ok {
		t.Fatal("запас попыток должен был кончиться")
	}

	// Залив: вдвое больше адресов, чем вмещает карта -- вытеснение случится
	// заведомо, и не один раз.
	for i := 0; i < remoteRateLimiterMaxBuckets*2; i++ {
		l.Allow(floodAddr(i))
	}

	if ok, _ := l.Allow(hammer); ok {
		t.Fatal("залив чужих адресов сбросил собственный счёт -- лимит обходится вытеснением")
	}
	if got := l.size(); got > remoteRateLimiterMaxBuckets {
		t.Fatalf("ведёрок в памяти = %d, want не больше %d", got, remoteRateLimiterMaxBuckets)
	}
}

// Обратная сторона того же правила: полные ведёрки забываются, и памяти
// хватает. Новый человек при переполненной карте обязан войти -- иначе
// защита от подбора сама стала бы способом запереть вход всем.
func TestRemoteRateLimiterForgetsFullBucketsAndStillLetsNewcomersIn(t *testing.T) {
	l := newRemoteRateLimiter(1, 5)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	for i := 0; i < remoteRateLimiterMaxBuckets+100; i++ {
		l.Allow(floodAddr(i))
	}
	// Час спустя все эти ведёрки полны -- помнить о них нечего.
	now = now.Add(time.Hour)
	if ok, _ := l.Allow("198.51.100.7:40001"); !ok {
		t.Fatal("новый адрес не пустили при переполненной карте")
	}
	if got := l.size(); got > remoteRateLimiterMaxBuckets {
		t.Fatalf("ведёрок в памяти = %d, want не больше %d", got, remoteRateLimiterMaxBuckets)
	}
}

// Что лимитер видит за релеем -- зафиксировано намеренно, а не забыто.
//
// X-Forwarded-For не разбирается нигде: заголовок подделывается одной
// строкой, и доверие к нему означало бы обход лимита вообще без залива.
// Цена выбора названа вслух: за релеем KeenDNS RemoteAddr может оказаться
// адресом релея для всех внешних клиентов сразу, и тогда ведёрко одно на
// всех -- подбор не изолируется, а десять чужих неудач способны запереть
// вход законному админу. Проверяется это на раскатке по журналу
// «вход: слишком много попыток»: если remote у разных людей один и тот же,
// ключ надо переводить на доверенный XFF от известного прокси.
func TestRemoteRateLimiterIgnoresForwardedForBehindRelay(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	h := remoteRateLimitMiddleware(l, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	hit := func(forwarded string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", nil)
		req.RemoteAddr = "198.51.100.1:40001" // один и тот же релей
		req.Header.Set("X-Forwarded-For", forwarded)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := hit("203.0.113.5"); code != http.StatusNoContent {
		t.Fatalf("первая попытка = %d, want 204", code)
	}
	// Другой XFF, тот же RemoteAddr -- то же ведёрко: подделываемый
	// заголовок ключом не становится.
	if code := hit("203.0.113.6"); code != http.StatusTooManyRequests {
		t.Fatalf("смена X-Forwarded-For обошла лимит: код %d, want 429", code)
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
