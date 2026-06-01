package callbacks

import (
	"sync"
	"time"
)

type pendingConfirm struct {
	UserID    int64
	ActorTGID int64
	ThreadID  *int64
	Action    string
	Target    string
	Token     string
	ExpiresAt time.Time
}

type pendingConfirmStore struct {
	mu sync.Mutex
	m  map[string]*pendingConfirm
}

func newPendingConfirmStore() *pendingConfirmStore {
	return &pendingConfirmStore{m: make(map[string]*pendingConfirm)}
}

func (s *pendingConfirmStore) put(p *pendingConfirm) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.Token] = p
}

func (s *pendingConfirmStore) consume(userID, actorTGID int64, threadID *int64, action, target, token string) (*pendingConfirm, bool) {
	if s == nil || token == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[token]
	if !ok {
		return nil, false
	}
	if p.UserID != userID || p.Action != action || p.Target != target || !sameThread(p.ThreadID, threadID) {
		return nil, false
	}
	if p.ActorTGID != 0 && p.ActorTGID != actorTGID {
		return nil, false
	}
	if time.Now().After(p.ExpiresAt) {
		delete(s.m, token)
		return nil, false
	}
	delete(s.m, token)
	return p, true
}

