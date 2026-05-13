package callbacks

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// diagCache stores the raw JSON body of a diag_now success result so
// the "📄 Полный отчёт" inline button can fetch it without re-running
// the diagnostic. Tokens are 8 hex chars (4 random bytes). TTL is set
// per Put; expired entries are evicted lazily on Get.
type diagCache struct {
	mu sync.Mutex
	m  map[string]diagCacheEntry
}

type diagCacheEntry struct {
	body      string
	expiresAt time.Time
}

func newDiagCache() *diagCache {
	return &diagCache{m: make(map[string]diagCacheEntry)}
}

// Put stores body under a fresh 8-hex token and returns the token.
func (c *diagCache) Put(body string, ttl time.Duration) string {
	tok := newDiagToken()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[tok] = diagCacheEntry{body: body, expiresAt: time.Now().Add(ttl)}
	return tok
}

// Get returns the body for token if present and not expired.
func (c *diagCache) Get(token string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[token]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiresAt) {
		delete(c.m, token)
		return "", false
	}
	return e.body, true
}

func newDiagToken() string {
	var b [4]byte
	_, _ = cryptoRand.Read(b[:])
	return hex.EncodeToString(b[:])
}
