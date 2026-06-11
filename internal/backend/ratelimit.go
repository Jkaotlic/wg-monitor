package backend

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// userRateLimiter is a per-userID token-bucket. Refill rate and burst capacity
// are configured at construction. Buckets are created lazily and survive for
// the lifetime of the backend (small fleet — bounded N).
//
// API-06: a misbehaving agent (scheduler-firing-every-second bug, leaked
// token, malicious peer who guessed a token) can flood /v1/report and wedge
// the SQLite writer. Per-user limit caps damage at one user.
type userRateLimiter struct {
	mu      sync.Mutex
	buckets map[int64]*tokenBucket
	rate    float64 // tokens per second
	burst   float64 // bucket capacity
	now     func() time.Time
}

type tokenBucket struct {
	tokens   float64
	lastFill time.Time
}

const unauthenticatedRateLimitUserID int64 = -1

func newUserRateLimiter(perSec float64, burst int) *userRateLimiter {
	if perSec <= 0 || burst <= 0 {
		return nil
	}
	return &userRateLimiter{
		buckets: make(map[int64]*tokenBucket),
		rate:    perSec,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow consumes one token for userID. Returns true on allow, false on
// rate-limit. retryAfter approximates seconds-until-next-token when limited.
func (l *userRateLimiter) Allow(userID int64) (ok bool, retryAfter time.Duration) {
	if l == nil {
		return true, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, found := l.buckets[userID]
	if !found {
		l.buckets[userID] = &tokenBucket{tokens: l.burst - 1, lastFill: now}
		return true, 0
	}
	elapsed := now.Sub(b.lastFill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.lastFill = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	missing := 1 - b.tokens
	wait := time.Duration(missing/l.rate*float64(time.Second)) + time.Second
	return false, wait
}

// RateLimitMiddleware enforces per-userID limits on protected endpoints.
// Must run AFTER AuthMiddleware so UserIDFromContext is populated. nil
// limiter is a no-op pass-through.
func RateLimitMiddleware(l *userRateLimiter, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid := UserIDFromContext(r.Context())
			if uid == 0 {
				uid = unauthenticatedRateLimitUserID
			}
			ok, retry := l.Allow(uid)
			if !ok {
				secs := int(retry.Round(time.Second).Seconds())
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				incReportRateLimit()
				if logger != nil {
					logger.Warn("rate limit exceeded",
						"user_id", uid,
						"path", r.URL.Path,
						"retry_after_sec", secs,
					)
				}
				writeJSONError(w, http.StatusTooManyRequests, "rate_limited",
					"too many requests; retry after "+strconv.Itoa(secs)+"s")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
