package callbacks

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// RouteAddDraft is the backend-local state for the add-route wizard.
type RouteAddDraft struct {
	UserID       int64
	ThreadID     *int64
	RouterID     int64
	Kind         string
	Name         string
	TunnelID     string
	Targets      []string
	UseHRNeo     bool
	Token        string
	ConfirmToken string
	PreviewHash  string
	ExpiresAt    time.Time
}

// RouteDeleteDraft is the backend-local state for one pending delete.
type RouteDeleteDraft struct {
	UserID       int64
	ThreadID     *int64
	RouterID     int64
	Kind         string
	RouteID      string
	PreviewHash  string
	Token        string
	ConfirmToken string
	ExpiresAt    time.Time
}

// RouteToken maps short Telegram callback_data tokens to full route IDs.
type RouteToken struct {
	UserID      int64
	ThreadID    *int64
	RouterID    int64
	Kind        string
	RouteID     string
	PreviewHash string
	Token       string
	ExpiresAt   time.Time
}

type RouteWizardStore struct {
	TTL       time.Duration
	Now       func() time.Time
	TokenFunc func() string

	mu          sync.Mutex
	addDrafts   map[string]RouteAddDraft
	delDrafts   map[string]RouteDeleteDraft
	routeTokens map[string]RouteToken
}

func NewRouteWizardStore(ttl time.Duration) *RouteWizardStore {
	return &RouteWizardStore{TTL: ttl}
}

func (s *RouteWizardStore) PutAddDraft(d RouteAddDraft) RouteAddDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	if d.Token == "" {
		d.Token = s.tokenLocked()
	}
	d.ExpiresAt = s.expiresAtLocked()
	s.addDrafts[d.Token] = d
	return d
}

func (s *RouteWizardStore) GetAddDraft(userID int64, threadID *int64, routerID int64, token string) (RouteAddDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.addDrafts[token]
	if !ok || !s.addScopeMatchesLocked(d.UserID, d.ThreadID, d.RouterID, userID, threadID, routerID) || s.expiredLocked(d.ExpiresAt) {
		if ok && s.expiredLocked(d.ExpiresAt) {
			delete(s.addDrafts, token)
		}
		return RouteAddDraft{}, false
	}
	return d, true
}

func (s *RouteWizardStore) GetOpenAddDraft(userID int64, threadID *int64, routerID int64) (RouteAddDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, d := range s.addDrafts {
		if !s.addScopeMatchesLocked(d.UserID, d.ThreadID, d.RouterID, userID, threadID, routerID) {
			continue
		}
		if s.expiredLocked(d.ExpiresAt) {
			delete(s.addDrafts, token)
			continue
		}
		if d.Name == "" || len(d.Targets) == 0 {
			return d, true
		}
	}
	return RouteAddDraft{}, false
}

func (s *RouteWizardStore) CancelAddDraft(userID int64, threadID *int64, routerID int64, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.addDrafts[token]
	if !ok || !s.addScopeMatchesLocked(d.UserID, d.ThreadID, d.RouterID, userID, threadID, routerID) {
		return false
	}
	delete(s.addDrafts, token)
	return true
}

func (s *RouteWizardStore) SetAddConfirm(userID int64, threadID *int64, routerID int64, token, previewHash string) (RouteAddDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.addDrafts[token]
	if !ok || !s.addScopeMatchesLocked(d.UserID, d.ThreadID, d.RouterID, userID, threadID, routerID) || s.expiredLocked(d.ExpiresAt) {
		return RouteAddDraft{}, false
	}
	d.PreviewHash = previewHash
	d.ConfirmToken = s.tokenLocked()
	s.addDrafts[token] = d
	return d, true
}

func (s *RouteWizardStore) ConsumeAddConfirm(userID int64, threadID *int64, routerID int64, token, confirmToken string) (RouteAddDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.addDrafts[token]
	if !ok || d.ConfirmToken != confirmToken || !s.addScopeMatchesLocked(d.UserID, d.ThreadID, d.RouterID, userID, threadID, routerID) || s.expiredLocked(d.ExpiresAt) {
		return RouteAddDraft{}, false
	}
	delete(s.addDrafts, token)
	return d, true
}

func (s *RouteWizardStore) PutDeleteDraft(d RouteDeleteDraft) RouteDeleteDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	if d.Token == "" {
		d.Token = s.tokenLocked()
	}
	d.ExpiresAt = s.expiresAtLocked()
	s.delDrafts[d.Token] = d
	return d
}

func (s *RouteWizardStore) GetDeleteDraft(userID int64, threadID *int64, routerID int64, token string) (RouteDeleteDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.delDrafts[token]
	if !ok || !s.addScopeMatchesLocked(d.UserID, d.ThreadID, d.RouterID, userID, threadID, routerID) || s.expiredLocked(d.ExpiresAt) {
		if ok && s.expiredLocked(d.ExpiresAt) {
			delete(s.delDrafts, token)
		}
		return RouteDeleteDraft{}, false
	}
	return d, true
}

func (s *RouteWizardStore) CancelDeleteDraft(userID int64, threadID *int64, routerID int64, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.delDrafts[token]
	if !ok || !s.addScopeMatchesLocked(d.UserID, d.ThreadID, d.RouterID, userID, threadID, routerID) {
		return false
	}
	delete(s.delDrafts, token)
	return true
}

func (s *RouteWizardStore) SetDeleteConfirm(userID int64, threadID *int64, routerID int64, token, previewHash string) (RouteDeleteDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.delDrafts[token]
	if !ok || !s.addScopeMatchesLocked(d.UserID, d.ThreadID, d.RouterID, userID, threadID, routerID) || s.expiredLocked(d.ExpiresAt) {
		return RouteDeleteDraft{}, false
	}
	d.PreviewHash = previewHash
	d.ConfirmToken = s.tokenLocked()
	s.delDrafts[token] = d
	return d, true
}

func (s *RouteWizardStore) ConsumeDeleteConfirm(userID int64, threadID *int64, routerID int64, token, confirmToken string) (RouteDeleteDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.delDrafts[token]
	if !ok || d.ConfirmToken != confirmToken || !s.addScopeMatchesLocked(d.UserID, d.ThreadID, d.RouterID, userID, threadID, routerID) || s.expiredLocked(d.ExpiresAt) {
		return RouteDeleteDraft{}, false
	}
	delete(s.delDrafts, token)
	return d, true
}

func (s *RouteWizardStore) PutRouteToken(rt RouteToken) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	if rt.Token == "" {
		rt.Token = s.tokenLocked()
	}
	rt.ExpiresAt = s.expiresAtLocked()
	s.routeTokens[rt.Token] = rt
	return rt.Token
}

func (s *RouteWizardStore) GetRouteToken(userID int64, threadID *int64, routerID int64, token string) (RouteToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.routeTokens[token]
	if !ok || !s.addScopeMatchesLocked(rt.UserID, rt.ThreadID, rt.RouterID, userID, threadID, routerID) || s.expiredLocked(rt.ExpiresAt) {
		if ok && s.expiredLocked(rt.ExpiresAt) {
			delete(s.routeTokens, token)
		}
		return RouteToken{}, false
	}
	return rt, true
}

func (s *RouteWizardStore) initLocked() {
	if s.addDrafts == nil {
		s.addDrafts = make(map[string]RouteAddDraft)
	}
	if s.delDrafts == nil {
		s.delDrafts = make(map[string]RouteDeleteDraft)
	}
	if s.routeTokens == nil {
		s.routeTokens = make(map[string]RouteToken)
	}
}

func (s *RouteWizardStore) addScopeMatchesLocked(storedUser int64, storedThread *int64, storedRouter int64, userID int64, threadID *int64, routerID int64) bool {
	return storedUser == userID && storedRouter == routerID && sameThread(storedThread, threadID)
}

func (s *RouteWizardStore) expiredLocked(expiresAt time.Time) bool {
	return !expiresAt.IsZero() && !s.nowLocked().Before(expiresAt)
}

func (s *RouteWizardStore) expiresAtLocked() time.Time {
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return s.nowLocked().Add(ttl)
}

func (s *RouteWizardStore) nowLocked() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *RouteWizardStore) tokenLocked() string {
	if s.TokenFunc != nil {
		return s.TokenFunc()
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func sameThread(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
