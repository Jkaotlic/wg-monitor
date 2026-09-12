package backend

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Лимит попыток на входах в систему -- том самом месте, где короткое
// случайное значение защищено только своей длиной.
//
// У лимитера отчётов (ratelimit.go) ключ -- токен агента, и на входе он
// бесполезен: тот, кто подбирает ссылку, токена не предъявляет вовсе.
// Поэтому здесь ключ -- адрес, с которого пришли.
const (
	// Три входа: /v1/dashboard/login, /v1/dashboard/web-link/redeem и
	// /v1/miniapp/session. Запас в 10 попыток -- чтобы человек, у которого
	// сорвалась связь на релее, мог перезайти несколько раз подряд; дальше
	// одна попытка в пять секунд.
	entranceRatePerSec = 0.2
	entranceBurst      = 10
	// Потолок числа адресов в памяти. Ключ приходит снаружи, значит карта
	// ведёрок -- это то, что перебором можно раздуть: без потолка защита от
	// подбора сама становится способом съесть память бэкенда.
	remoteRateLimiterMaxBuckets = 4096
)

// remoteRateLimiter -- token bucket на адрес обращения.
type remoteRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // токенов в секунду
	burst   float64 // ёмкость ведёрка
	now     func() time.Time
}

func newRemoteRateLimiter(perSec float64, burst int) *remoteRateLimiter {
	if perSec <= 0 || burst <= 0 {
		return nil
	}
	return &remoteRateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    perSec,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow тратит один токен адреса. nil-лимитер пропускает всё: это
// «ограничение выключено», а не паника на каждом запросе.
func (l *remoteRateLimiter) Allow(remote string) (ok bool, retryAfter time.Duration) {
	if l == nil {
		return true, 0
	}
	key := remoteRateKey(remote)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= remoteRateLimiterMaxBuckets {
			l.evictLocked(now)
		}
		l.buckets[key] = &tokenBucket{tokens: l.burst - 1, lastFill: now}
		return true, 0
	}
	b.tokens = l.tokensAt(b, now)
	b.lastFill = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	missing := 1 - b.tokens
	wait := time.Duration(missing/l.rate*float64(time.Second)) + time.Second
	return false, wait
}

func (l *remoteRateLimiter) tokensAt(b *tokenBucket, now time.Time) float64 {
	tokens := b.tokens
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		tokens += elapsed * l.rate
	}
	if tokens > l.burst {
		tokens = l.burst
	}
	return tokens
}

// evictLocked освобождает место под новый адрес. Сначала уходят полные
// ведёрки: у такого адреса лимит всё равно ничего не помнит. Если и после
// этого места нет -- значит идёт распределённый перебор, и часть адресов
// теряет свой счёт досрочно. Это честный размен: память бэкенда ограничена,
// а безлимитная карта ведёрок уронила бы его целиком.
func (l *remoteRateLimiter) evictLocked(now time.Time) {
	for key, b := range l.buckets {
		if l.tokensAt(b, now) >= l.burst {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) < remoteRateLimiterMaxBuckets {
		return
	}
	target := remoteRateLimiterMaxBuckets * 3 / 4
	for key := range l.buckets {
		if len(l.buckets) <= target {
			return
		}
		delete(l.buckets, key)
	}
}

func (l *remoteRateLimiter) size() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// remoteRateKey -- адрес без порта. Порт у подбирающего меняется на каждом
// запросе, и ключ по нему не ограничил бы ничего вовсе.
func remoteRateKey(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil && host != "" {
		return host
	}
	return remote
}

// remoteRateLimitMiddleware ставит лимит попыток перед входом. Отказ говорит
// словами и называет срок: человеку, у которого сорвалась связь, надо знать,
// что он не заблокирован навсегда.
func remoteRateLimitMiddleware(l *remoteRateLimiter, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retry := l.Allow(r.RemoteAddr)
			if !ok {
				secs := int(retry.Round(time.Second).Seconds())
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				if logger != nil {
					logger.Warn("вход: слишком много попыток",
						"remote", remoteRateKey(r.RemoteAddr),
						"path", r.URL.Path,
						"retry_after_sec", secs,
					)
				}
				writeJSONError(w, http.StatusTooManyRequests, "rate_limited",
					"Слишком много попыток. Попробуйте через "+strconv.Itoa(secs)+" с.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
