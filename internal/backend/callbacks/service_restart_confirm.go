package callbacks

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// pendingMaint is one queued service-restart confirmation (hrneo / awgmgr).
type pendingMaint struct {
	UserID    int64
	ActorTGID int64
	Name      string // одно из botServiceRestartNames
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
// userID and is unexpired. Returns ok=false on any mismatch.
//
// Важно: при mismatch UserID мы НЕ удаляем pending — иначе любой member
// чата может тапнуть кнопку чужого подтверждения и DoS'нуть owner'у его
// maintenance-операцию (BUG-04). Удаляем только при success или истечении
// expiry.
func (s *pendingMaintStore) consume(userID int64, token string) (*pendingMaint, bool) {
	return s.consumeForActor(userID, 0, token)
}

func (s *pendingMaintStore) consumeForActor(userID, actorTGID int64, token string) (*pendingMaint, bool) {
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
	if p.ActorTGID != 0 && p.ActorTGID != actorTGID {
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

func (s *pendingMaintStore) applyForActor(userID, actorTGID int64, token string, apply func(*pendingMaint) error) (*pendingMaint, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[token]
	if !ok {
		return nil, false, nil
	}
	if p.UserID != userID {
		return nil, false, nil
	}
	if p.ActorTGID != 0 && p.ActorTGID != actorTGID {
		return nil, false, nil
	}
	if time.Now().After(p.ExpiresAt) {
		delete(s.m, token)
		return nil, false, nil
	}
	if err := apply(p); err != nil {
		return p, true, err
	}
	delete(s.m, token)
	return p, true, nil
}

// makeMaintToken returns 8 lowercase hex characters seeded from crypto/rand.
// Same shape as the existing routes-rebind tokens.
func makeMaintToken() string {
	var b [4]byte
	_, _ = cryptoRand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// botServiceRestartNames -- что бот ещё перезапускает сам: HydraRoute Neo
// (кнопки панели маршрутов, цикл 4 программы) и awg-manager (старая кнопка
// restart_tunnel под тревогой). Перезагрузка роутера, прошивка и пакеты
// Entware переехали в мини-апп (цикл 1), и живой токен из старого сообщения
// их в очередь не ставит.
var botServiceRestartNames = map[string]bool{
	"hrneo":       true,
	"hrneo_start": true,
	"hrneo_stop":  true,
	"awgmgr":      true,
}

// MaintConfirmAction implements the Action interface for the maint_confirm
// callback. It atomically consumes the pending token and enqueues
// service_restart for one of botServiceRestartNames.
type MaintConfirmAction struct {
	sink  CommandEnqueuer
	store *pendingMaintStore
	idGen func() string
}

func NewMaintConfirmAction(sink CommandEnqueuer, store *pendingMaintStore, idGen func() string) *MaintConfirmAction {
	return &MaintConfirmAction{sink: sink, store: store, idGen: idGen}
}

func (a *MaintConfirmAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	maintName := ""
	pm, ok, err := a.store.applyForActor(args.UserID, q.From.ID, args.MaintToken, func(pm *pendingMaint) error {
		if !botServiceRestartNames[pm.Name] {
			return fmt.Errorf("unknown maint name: %q", pm.Name)
		}
		cmd := wire.Command{
			ID:       a.idGen(),
			Action:   "service_restart",
			Args:     map[string]any{"name": pm.Name},
			IssuedAt: time.Now().UTC(),
		}
		maintName = pm.Name
		// EnqueueWithRef (а не голый Enqueue) — иначе итог не вернётся в чат.
		ref := cmdpkg.MessageRef{
			ChatID:    q.Message.Chat.ID,
			MessageID: q.Message.MessageID,
			ThreadID:  q.Message.MessageThreadID,
		}
		if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
			return fmt.Errorf("не удалось поставить команду в очередь: %w", err)
		}
		return nil
	})
	if !ok {
		return "", fmt.Errorf("token expired or unknown")
	}
	if err != nil {
		return "", err
	}
	if maintName == "" && pm != nil {
		maintName = pm.Name
	}
	return fmt.Sprintf("✅ запрос отправлен: %s", maintName), nil
}

// ensure MaintConfirmAction satisfies Action at compile time.
var _ Action = (*MaintConfirmAction)(nil)
