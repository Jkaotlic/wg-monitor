package callbacks

import (
	"sync"
	"time"
)

// pendingAddOperator is one in-progress FSM: the admin tapped "Добавить
// оператора" and we're waiting for them to either forward a message from
// the new operator or type a numeric TG ID. Single FSM per admin at a
// time (keyed by admin user id); a fresh put replaces any prior entry.
type pendingAddOperator struct {
	AdminUserID int64
	RouterID    int64
	ExpiresAt   time.Time
}

// pendingAddOperatorStore is a goroutine-safe map keyed by admin's TG
// user id. Lifetime mirrors pendingMaintStore — short TTL (5 min), in-
// memory only, evicted on expired-get.
type pendingAddOperatorStore struct {
	mu sync.Mutex
	m  map[int64]*pendingAddOperator
}

func newPendingAddOperatorStore() *pendingAddOperatorStore {
	return &pendingAddOperatorStore{m: make(map[int64]*pendingAddOperator)}
}

// put stores an FSM for `adminID`, replacing any prior pending entry for
// the same admin. ttl is added to time.Now() as the expiry.
func (s *pendingAddOperatorStore) put(adminID, routerID int64, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[adminID] = &pendingAddOperator{
		AdminUserID: adminID,
		RouterID:    routerID,
		ExpiresAt:   time.Now().Add(ttl),
	}
}

// get returns the unexpired FSM for `adminID` or (nil, false). Expired
// entries are evicted as a side effect.
func (s *pendingAddOperatorStore) get(adminID int64) (*pendingAddOperator, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[adminID]
	if !ok {
		return nil, false
	}
	if time.Now().After(p.ExpiresAt) {
		delete(s.m, adminID)
		return nil, false
	}
	return p, true
}

// clear removes the FSM for `adminID` (no-op if absent).
func (s *pendingAddOperatorStore) clear(adminID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, adminID)
}
