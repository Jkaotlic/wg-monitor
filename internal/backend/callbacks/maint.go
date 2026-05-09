package callbacks

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// pendingMaint is one queued maintenance confirmation. Created when the user
// taps a destructive button (e.g. "🔁 Reboot router"); consumed when they tap
// "✅ Подтвердить" within the TTL. After consume the entry is removed from
// the store — replay is impossible.
type pendingMaint struct {
	UserID    int64
	Name      string // "hrneo" | "awgmgr" | "router" | "firmware"
	Token     string
	ExpiresAt time.Time
}

// pendingMaintStore is a goroutine-safe map of token → pendingMaint with
// atomic consume semantics (delete-and-return). Lost on backend restart —
// acceptable since tokens are short-lived (5 min).
type pendingMaintStore struct {
	mu sync.Mutex
	m  map[string]*pendingMaint
}

func newPendingMaintStore() *pendingMaintStore {
	return &pendingMaintStore{m: make(map[string]*pendingMaint)}
}

func (s *pendingMaintStore) put(p *pendingMaint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.Token] = p
}

// consume atomically removes the pendingMaint and returns it iff it matches
// userID and is unexpired. Returns ok=false on any mismatch — and in the
// expired case, also evicts the stale entry so a re-issued token under the
// same key can succeed.
func (s *pendingMaintStore) consume(userID int64, token string) (*pendingMaint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[token]
	if !ok {
		return nil, false
	}
	delete(s.m, token)
	if p.UserID != userID || time.Now().After(p.ExpiresAt) {
		return nil, false
	}
	return p, true
}

// makeMaintToken returns 8 lowercase hex characters seeded from crypto/rand.
// Same shape as the existing routes-rebind tokens.
func makeMaintToken() string {
	var b [4]byte
	_, _ = cryptoRand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// cooldownStore tracks per-user, per-action cooldown windows for destructive
// ops (router reboot, firmware install). All in-memory; lost on backend
// restart — acceptable since the cooldown window is at most 5 minutes and
// the underlying side effect (reboot) takes about that long anyway.
type cooldownStore struct {
	mu sync.Mutex
	m  map[cooldownKey]time.Time // value = expires-at
}

type cooldownKey struct {
	UserID int64
	Action string // "router_reboot" | "firmware_install"
}

func newCooldownStore() *cooldownStore {
	return &cooldownStore{m: make(map[cooldownKey]time.Time)}
}

func (s *cooldownStore) set(userID int64, action string, dur time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[cooldownKey{userID, action}] = time.Now().Add(dur)
}

// remaining returns the time left in the cooldown window, or 0 if there's
// no active cooldown for this (userID, action). Expired entries are evicted
// as a side effect.
func (s *cooldownStore) remaining(userID int64, action string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := cooldownKey{userID, action}
	until, ok := s.m[k]
	if !ok {
		return 0
	}
	rem := time.Until(until)
	if rem <= 0 {
		delete(s.m, k)
		return 0
	}
	return rem
}
