package callbacks

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// pendingMaint is one queued maintenance confirmation. Created when the user
// taps a destructive button (e.g. "🔁 Reboot router"); consumed when they tap
// "✅ Подтвердить" within the TTL. After consume the entry is removed from
// the store — replay is impossible.
type pendingMaint struct {
	UserID    int64
	ActorTGID int64
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

// MaintConfirmAction implements the Action interface for the maint_confirm /
// maint_fw_confirm callbacks. It atomically consumes the pending token,
// enqueues the appropriate wire.Command for the agent, and applies a
// per-user, per-action cooldown for destructive ops (router reboot, firmware
// install). hrneo / awgmgr restarts are cheap and do not trigger cooldown.
type MaintConfirmAction struct {
	sink  CommandEnqueuer
	store *pendingMaintStore
	cd    *cooldownStore
	idGen func() string
}

func NewMaintConfirmAction(sink CommandEnqueuer, store *pendingMaintStore, cd *cooldownStore, idGen func() string) *MaintConfirmAction {
	return &MaintConfirmAction{sink: sink, store: store, cd: cd, idGen: idGen}
}

func (a *MaintConfirmAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	cooldownAction := ""
	maintName := ""
	pm, ok, err := a.store.applyForActor(args.UserID, q.From.ID, args.MaintToken, func(pm *pendingMaint) error {
		cmd := wire.Command{ID: a.idGen(), IssuedAt: time.Now().UTC()}
		switch pm.Name {
		case "hrneo", "hrneo_start", "hrneo_stop", "awgmgr":
			cmd.Action = "service_restart"
			cmd.Args = map[string]any{"name": pm.Name}
		case "opkg_upgrade":
			cmd.Action = "opkg_upgrade"
		case "router":
			cmd.Action = "service_restart"
			cmd.Args = map[string]any{"name": "router"}
			cooldownAction = "router_reboot"
		case "firmware":
			cmd.Action = "firmware_install"
			cooldownAction = "firmware_install"
		default:
			return fmt.Errorf("unknown maint name: %q", pm.Name)
		}
		maintName = pm.Name
		// EnqueueWithRef (а не голый Enqueue) — иначе ConsumeOriginRef в
		// handler.go::cmdResultHandler возвращает false, MaintNotifier
		// .NotifyCommandResult НЕ вызывается, и maint-панель оператора никогда
		// не обновится после исполнения. Симметричный RebindConfirmAction
		// делает это правильно — у нас был чистый asymmetry-bug (LOGIC-01).
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
	if cooldownAction != "" {
		a.cd.set(args.UserID, cooldownAction, 5*time.Minute)
	}
	return fmt.Sprintf("✅ запрос отправлен: %s", maintName), nil
}

// ensure MaintConfirmAction satisfies Action at compile time.
var _ Action = (*MaintConfirmAction)(nil)

// suppress unused-import lint until callers wire cmdpkg.MessageRef.
var _ cmdpkg.MessageRef
