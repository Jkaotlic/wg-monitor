package callbacks

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// pendingOpkgRepair is one queued repair request. Created when the backend
// renders a 🔧 button under an opkg_upgrade message; consumed when the user
// taps within the TTL. Single use — replay impossible.
type pendingOpkgRepair struct {
	UserID    int64
	URL       string // already normalised (no /Packages.gz suffix)
	Token     string // 8 hex chars
	ExpiresAt time.Time
}

// pendingOpkgRepairStore is a goroutine-safe map of token → pendingOpkgRepair.
// Lifetimes and consume semantics mirror pendingMaintStore (see maint.go).
// Lost on backend restart — acceptable since tokens are short-lived (5 min).
type pendingOpkgRepairStore struct {
	mu sync.Mutex
	m  map[string]*pendingOpkgRepair
}

func newPendingOpkgRepairStore() *pendingOpkgRepairStore {
	return &pendingOpkgRepairStore{m: make(map[string]*pendingOpkgRepair)}
}

func (s *pendingOpkgRepairStore) put(p *pendingOpkgRepair) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.Token] = p
}

// consume atomically removes-and-returns the entry iff userID matches and the
// entry is unexpired. On UserID mismatch the entry stays — protects against
// a chat member tapping someone else's button.
//
// Важно: при mismatch UserID мы НЕ удаляем pending — иначе любой member
// чата может тапнуть кнопку чужого подтверждения и DoS'нуть owner'у его
// opkg-операцию. Удаляем только при success или истечении expiry.
func (s *pendingOpkgRepairStore) consume(userID int64, token string) (*pendingOpkgRepair, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[token]
	if !ok {
		return nil, false
	}
	if p.UserID != userID {
		// чужой member тапнул кнопку — игнорируем, токен оставляем для owner'а.
		return nil, false
	}
	if time.Now().After(p.ExpiresAt) {
		// expired — эвиктим, чтобы новый token мог занять место.
		delete(s.m, token)
		return nil, false
	}
	delete(s.m, token)
	return p, true
}

// makeOpkgRepairToken returns 8 lowercase hex characters seeded from crypto/rand.
// Same shape as makeMaintToken and makeRebindToken.
func makeOpkgRepairToken() string {
	var b [4]byte
	_, _ = cryptoRand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// OpkgRepairAction implements the Action interface for opkg_disable callbacks.
// It consumes the pendingOpkgRepair by token and enqueues an
// opkg_feed_disable wire.Command for the agent.
type OpkgRepairAction struct {
	sink  CommandEnqueuer
	store *pendingOpkgRepairStore
	idGen func() string
}

func NewOpkgRepairAction(sink CommandEnqueuer, store *pendingOpkgRepairStore, idGen func() string) *OpkgRepairAction {
	if idGen == nil {
		idGen = defaultCmdID
	}
	return &OpkgRepairAction{sink: sink, store: store, idGen: idGen}
}

func (a *OpkgRepairAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	if a.sink == nil {
		return "", errors.New("command channel disabled (no sink configured)")
	}
	p, ok := a.store.consume(args.UserID, args.OpkgRepairToken)
	if !ok {
		return "", errors.New("сессия истекла или не найдена; запусти проверку обновлений заново")
	}
	cmd := wire.Command{
		ID:       a.idGen(),
		Action:   "opkg_feed_disable",
		Args:     map[string]any{"url": p.URL},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
	}
	if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
		return "", fmt.Errorf("enqueue opkg_feed_disable: %w", err)
	}
	return "🔧 отключаем фид…", nil
}

// ensure OpkgRepairAction satisfies Action at compile time.
var _ Action = (*OpkgRepairAction)(nil)
