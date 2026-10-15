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
// Публичный RemoteAddr: X-Forwarded-For подделывается одной строкой, ему
// не верим (BOT-04: верим только за частным/loopback прокси).
func TestRemoteRateLimiterIgnoresForwardedForFromPublicRemote(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	h := remoteRateLimitMiddleware(l, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))

	hit := func(forwarded string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", nil)
		req.RemoteAddr = "198.51.100.1:40001" // один и тот же релей
		req.Header.Set("X-Forwarded-For", forwarded)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := hit("203.0.113.5"); code != http.StatusUnauthorized {
		t.Fatalf("первая попытка = %d, want 401", code)
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
		w.WriteHeader(http.StatusUnauthorized)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", nil)
	req.RemoteAddr = "198.51.100.7:40001"
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("первый запрос = %d, want 401", first.Code)
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

func limitedHandler(l *remoteRateLimiter, status int) http.Handler {
	return remoteRateLimitMiddleware(l, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
}

func hitFrom(h http.Handler, remote string, hdr map[string]string) int {
	req := httptest.NewRequest(http.MethodPost, "/v1/miniapp/session", nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// BOT-04: за доверенным прокси (частный RemoteAddr) ключ -- клиент из XFF.
func TestRemoteRateLimitKeysByForwardedClientBehindPrivateProxy(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	l.now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	h := limitedHandler(l, http.StatusUnauthorized)
	proxy := "192.168.1.1:40001"
	if c := hitFrom(h, proxy, map[string]string{"X-Forwarded-For": "203.0.113.5"}); c != http.StatusUnauthorized {
		t.Fatalf("первый = %d", c)
	}
	// Другой клиент за тем же прокси лимит не делит.
	if c := hitFrom(h, proxy, map[string]string{"X-Forwarded-For": "203.0.113.6"}); c != http.StatusUnauthorized {
		t.Fatalf("чужой клиент делит лимит: %d", c)
	}
	// Тот же клиент -- исчерпал.
	if c := hitFrom(h, proxy, map[string]string{"X-Forwarded-For": "203.0.113.5"}); c != http.StatusTooManyRequests {
		t.Fatalf("повтор клиента = %d, want 429", c)
	}
}

func TestRemoteRateLimitIgnoresForwardedFromPublicRemote(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	l.now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	h := limitedHandler(l, http.StatusUnauthorized)
	hitFrom(h, "198.51.100.9:1", map[string]string{"X-Forwarded-For": "203.0.113.1", "X-Real-IP": "203.0.113.2"})
	if c := hitFrom(h, "198.51.100.9:2", map[string]string{"X-Forwarded-For": "203.0.113.3"}); c != http.StatusTooManyRequests {
		t.Fatalf("подделка XFF обошла лимит: %d", c)
	}
}

// Клиент дописывает свой XFF слева: берём крайний правый недоверенный.
func TestRemoteRateLimitTakesRightmostUntrustedForwarded(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "127.0.0.1:5"
	r.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.4, 10.0.0.2")
	if got := clientRateKey(r); got != "198.51.100.4" {
		t.Fatalf("key = %q, want 198.51.100.4", got)
	}
}

// X-Real-IP целиком в руках клиента при проброске заголовков: не читается.
func TestRemoteRateLimitRotatingRealIPHasNoEffect(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	l.now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	h := limitedHandler(l, http.StatusUnauthorized)
	hitFrom(h, "198.51.100.9:1", map[string]string{"X-Real-IP": "203.0.113.1"})
	if c := hitFrom(h, "198.51.100.9:2", map[string]string{"X-Real-IP": "203.0.113.2"}); c != http.StatusTooManyRequests {
		t.Fatalf("X-Real-IP обошёл лимит (публичный): %d", c)
	}
	proxy := "192.168.1.1:5"
	n429 := 0
	for i := 0; i < 40; i++ {
		if hitFrom(h, proxy, map[string]string{"X-Real-IP": fmt.Sprintf("203.0.113.%d", i+1)}) == http.StatusTooManyRequests {
			n429++
		}
	}
	if n429 == 0 {
		t.Fatal("перебор X-Real-IP за прокси не остановлен")
	}
}

// Прокси пропускает XFF клиента как есть: перебор заголовка упирается в потолок по RemoteAddr.
func TestRemoteRateLimitBackstopStopsRotatingForwardedFor(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1) // потолок: burst 10
	l.now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	h := limitedHandler(l, http.StatusUnauthorized)
	pass := 0
	for i := 0; i < 50; i++ {
		if hitFrom(h, "192.168.1.1:5", map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.113.%d", i+1)}) == http.StatusUnauthorized {
			pass++
		}
	}
	if pass != 10 {
		t.Fatalf("пропущено %d запросов, want 10 (потолок по RemoteAddr)", pass)
	}
}

// Потолок считает и удачные входы.
func TestRemoteRateLimitBackstopCountsSuccesses(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	l.now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	h := limitedHandler(l, http.StatusOK)
	got429 := false
	for i := 0; i < 30; i++ {
		if hitFrom(h, "198.51.100.1:1", nil) == http.StatusTooManyRequests {
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("залив удачными входами не упёрся в потолок")
	}
}

// Удачные входы токен не тратят; неудачные -- тратят.
func TestRemoteRateLimitSuccessfulEntriesDoNotSpendTokens(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 2)
	l.now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	ok := limitedHandler(l, http.StatusOK)
	for i := 0; i < 20; i++ {
		if c := hitFrom(ok, "198.51.100.1:1", nil); c != http.StatusOK {
			t.Fatalf("удачный вход %d отвергнут: %d", i, c)
		}
	}
	bad := limitedHandler(l, http.StatusUnauthorized)
	hitFrom(bad, "198.51.100.1:1", nil)
	hitFrom(bad, "198.51.100.1:1", nil)
	if c := hitFrom(bad, "198.51.100.1:1", nil); c != http.StatusTooManyRequests {
		t.Fatalf("неудачи не тратят токены: %d", c)
	}
}

// Отказанный собственным ведёрком запрос общий потолок не расходует.
func TestRemoteRateLimitRefusedClientDoesNotDrainBackstop(t *testing.T) {
	l := newRemoteRateLimiter(0.01, 1)
	l.now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	h := limitedHandler(l, http.StatusUnauthorized)
	proxy := "192.168.1.1:5"
	for i := 0; i < 100; i++ {
		hitFrom(h, proxy, map[string]string{"X-Forwarded-For": "203.0.113.5"})
	}
	if c := hitFrom(h, proxy, map[string]string{"X-Forwarded-For": "203.0.113.6"}); c != http.StatusUnauthorized {
		t.Fatalf("второй клиент заперт чужим натиском: %d", c)
	}
}

func TestClientRateKeyIgnoresRealIPEntirely(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "127.0.0.1:4242"
	r.Header.Set("X-Real-IP", "203.0.113.7")
	if got := clientRateKey(r); got != "127.0.0.1" {
		t.Fatalf("key = %q, want 127.0.0.1", got)
	}
}
