package callbacks

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// RouteAddDraft is the backend-local state for the add-route wizard.
type RouteAddDraft struct {
	UserID       int64
	ActorTGID    int64
	ThreadID     *int64
	RouterID     int64
	Kind         string
	Name         string
	TunnelID     string
	Targets      []string
	UseHRNeo     bool
	TemplateID   string
	Token        string
	ConfirmToken string
	PreviewHash  string
	ExpiresAt    time.Time
}

// RouteDeleteDraft is the backend-local state for one pending delete.
type RouteDeleteDraft struct {
	UserID       int64
	ActorTGID    int64
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

// RouteTemplateToken maps short Telegram callback_data tokens to full AWG
// Manager template IDs. Template IDs are kept out of callback_data because
// they can be long and are not under backend control.
type RouteTemplateToken struct {
	UserID       int64
	ActorTGID    int64
	ThreadID     *int64
	RouterID     int64
	DraftToken   string
	TemplateID   string
	TemplateName string
	Token        string
	ExpiresAt    time.Time
}

type RouteTemplateCatalog struct {
	UserID     int64
	ActorTGID  int64
	ThreadID   *int64
	RouterID   int64
	DraftToken string
	Templates  []wire.RouteTemplate
	ExpiresAt  time.Time
}

type RouteWizardStore struct {
	TTL       time.Duration
	Now       func() time.Time
	TokenFunc func() string

	mu               sync.Mutex
	addDrafts        map[string]RouteAddDraft
	delDrafts        map[string]RouteDeleteDraft
	routeTokens      map[string]RouteToken
	templateTokens   map[string]RouteTemplateToken
	templateCatalogs map[string]RouteTemplateCatalog
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

func (s *RouteWizardStore) GetAddDraftForActor(userID, actorTGID int64, threadID *int64, routerID int64, token string) (RouteAddDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.addDrafts[token]
	if !ok || !s.addScopeMatchesActorLocked(d.UserID, d.ActorTGID, d.ThreadID, d.RouterID, userID, actorTGID, threadID, routerID) || s.expiredLocked(d.ExpiresAt) {
		if ok && s.expiredLocked(d.ExpiresAt) {
			delete(s.addDrafts, token)
		}
		return RouteAddDraft{}, false
	}
	return d, true
}

func (s *RouteWizardStore) GetOpenAddDraft(userID int64, threadID *int64, routerID int64) (RouteAddDraft, bool) {
	return s.GetOpenAddDraftForActor(userID, 0, threadID, routerID)
}

func (s *RouteWizardStore) GetOpenAddDraftForActor(userID, actorTGID int64, threadID *int64, routerID int64) (RouteAddDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, d := range s.addDrafts {
		if !s.addScopeMatchesActorLocked(d.UserID, d.ActorTGID, d.ThreadID, d.RouterID, userID, actorTGID, threadID, routerID) {
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
	return s.CancelAddDraftForActor(userID, 0, threadID, routerID, token)
}

func (s *RouteWizardStore) CancelAddDraftForActor(userID, actorTGID int64, threadID *int64, routerID int64, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.addDrafts[token]
	if !ok || !s.addScopeMatchesActorLocked(d.UserID, d.ActorTGID, d.ThreadID, d.RouterID, userID, actorTGID, threadID, routerID) {
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
	return s.ConsumeAddConfirmForActor(userID, 0, threadID, routerID, token, confirmToken)
}

func (s *RouteWizardStore) ConsumeAddConfirmForActor(userID, actorTGID int64, threadID *int64, routerID int64, token, confirmToken string) (RouteAddDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.addDrafts[token]
	if !ok || d.ConfirmToken != confirmToken || !s.addScopeMatchesActorLocked(d.UserID, d.ActorTGID, d.ThreadID, d.RouterID, userID, actorTGID, threadID, routerID) || s.expiredLocked(d.ExpiresAt) {
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
	return s.CancelDeleteDraftForActor(userID, 0, threadID, routerID, token)
}

func (s *RouteWizardStore) CancelDeleteDraftForActor(userID, actorTGID int64, threadID *int64, routerID int64, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.delDrafts[token]
	if !ok || !s.addScopeMatchesActorLocked(d.UserID, d.ActorTGID, d.ThreadID, d.RouterID, userID, actorTGID, threadID, routerID) {
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
	return s.ConsumeDeleteConfirmForActor(userID, 0, threadID, routerID, token, confirmToken)
}

func (s *RouteWizardStore) ConsumeDeleteConfirmForActor(userID, actorTGID int64, threadID *int64, routerID int64, token, confirmToken string) (RouteDeleteDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.delDrafts[token]
	if !ok || d.ConfirmToken != confirmToken || !s.addScopeMatchesActorLocked(d.UserID, d.ActorTGID, d.ThreadID, d.RouterID, userID, actorTGID, threadID, routerID) || s.expiredLocked(d.ExpiresAt) {
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

func (s *RouteWizardStore) PutTemplateToken(tt RouteTemplateToken) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	if tt.Token == "" {
		tt.Token = s.tokenLocked()
	}
	tt.ExpiresAt = s.expiresAtLocked()
	s.templateTokens[tt.Token] = tt
	return tt.Token
}

func (s *RouteWizardStore) GetTemplateTokenForActor(userID, actorTGID int64, threadID *int64, routerID int64, draftToken, token string) (RouteTemplateToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tt, ok := s.templateTokens[token]
	if !ok || tt.DraftToken != draftToken || !s.addScopeMatchesActorLocked(tt.UserID, tt.ActorTGID, tt.ThreadID, tt.RouterID, userID, actorTGID, threadID, routerID) || s.expiredLocked(tt.ExpiresAt) {
		if ok && s.expiredLocked(tt.ExpiresAt) {
			delete(s.templateTokens, token)
		}
		return RouteTemplateToken{}, false
	}
	return tt, true
}

func (s *RouteWizardStore) PutTemplateCatalog(c RouteTemplateCatalog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	c.ExpiresAt = s.expiresAtLocked()
	c.Templates = append([]wire.RouteTemplate(nil), c.Templates...)
	s.templateCatalogs[c.DraftToken] = c
}

func (s *RouteWizardStore) GetTemplateCatalogForActor(userID, actorTGID int64, threadID *int64, routerID int64, draftToken string) (RouteTemplateCatalog, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.templateCatalogs[draftToken]
	if !ok || !s.addScopeMatchesActorLocked(c.UserID, c.ActorTGID, c.ThreadID, c.RouterID, userID, actorTGID, threadID, routerID) || s.expiredLocked(c.ExpiresAt) {
		if ok && s.expiredLocked(c.ExpiresAt) {
			delete(s.templateCatalogs, draftToken)
		}
		return RouteTemplateCatalog{}, false
	}
	c.Templates = append([]wire.RouteTemplate(nil), c.Templates...)
	return c, true
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
	if s.templateTokens == nil {
		s.templateTokens = make(map[string]RouteTemplateToken)
	}
	if s.templateCatalogs == nil {
		s.templateCatalogs = make(map[string]RouteTemplateCatalog)
	}
}

func (s *RouteWizardStore) addScopeMatchesLocked(storedUser int64, storedThread *int64, storedRouter int64, userID int64, threadID *int64, routerID int64) bool {
	return storedUser == userID && storedRouter == routerID && sameThread(storedThread, threadID)
}

func (s *RouteWizardStore) addScopeMatchesActorLocked(storedUser, storedActor int64, storedThread *int64, storedRouter, userID, actorTGID int64, threadID *int64, routerID int64) bool {
	if !s.addScopeMatchesLocked(storedUser, storedThread, storedRouter, userID, threadID, routerID) {
		return false
	}
	return storedActor == 0 || storedActor == actorTGID
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
