package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

type TGSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	SendMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup *tg.InlineKeyboardMarkup) (int64, error)
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
	CreateForumTopic(ctx context.Context, chatID int64, name string, iconColor int) (int64, error)
}

type Config struct {
	ChatID            int64
	FailThreshold     int
	RecoveryThreshold int
}

type Dispatcher struct {
	d   *db.DB
	tg  TGSender
	cfg Config
	mu  sync.Mutex
	// WelcomeKeyboard returns the reply-keyboard markup to attach to the
	// first message in a freshly-created per_router topic. Set by
	// cmd/backend/main.go after construction (the Dispatcher doesn't
	// import the callbacks UI snapshot to avoid a cycle). Nil disables
	// welcome — used only by tests or admin-disabled flows.
	WelcomeKeyboard func() any
}

func NewDispatcher(d *db.DB, tg TGSender, cfg Config) *Dispatcher {
	return &Dispatcher{d: d, tg: tg, cfg: cfg}
}

// Handle reacts to one FSM transition for one (user, check). The full
// wire.Check is passed (not just an extracted detail string) so the
// formatter can render rich per-category text from Details.
func (di *Dispatcher) Handle(ctx context.Context, userID int64, nickname, checkName string, tr state.Transition, check wire.Check) error {
	switch tr.Kind {
	case state.Noop, state.Soft:
		return di.d.State().Save(userID, checkName, tr.Next)
	case state.SoftFlap:
		today := time.Now().UTC().Format("2006-01-02")
		if err := di.d.State().IncSoftFlap(userID, checkName, today); err != nil {
			return err
		}
		return di.d.State().Save(userID, checkName, tr.Next)
	case state.Hard:
		// Save FSM transition FIRST, отправка в TG — после. Иначе при
		// 5xx/429 от TG возвращалась ошибка ДО Save, FSM оставался в
		// pre-Hard состоянии, следующий fail-репорт снова пересекал
		// порог → дубль алерта на каждый TG-глюк (BUG-02).
		// LastAlertMsgID/LastAlertAt пишем вторым save'ом только при
		// успехе TG-send. Если TG упал — FSM корректно в HARD, но
		// LastAlertAt=NULL значит realert poller его не подхватит до
		// следующего ручного refresh / OK-репорта.
		threadID, err := di.ensureTopic(ctx, userID, nickname)
		if err != nil {
			return fmt.Errorf("ensure topic for %s/%s: %w", nickname, checkName, err)
		}
		next := tr.Next
		if err := di.d.State().Save(userID, checkName, next); err != nil {
			return fmt.Errorf("save HARD state %s/%s: %w", nickname, checkName, err)
		}
		args := HardArgs{
			Nickname:    nickname,
			CheckName:   checkName,
			ConsecFails: tr.Next.ConsecutiveFails,
			HardSince:   *tr.Next.HardSince,
			Check:       check,
		}
		if u, err := di.d.Users().GetByID(userID); err == nil {
			args.IsMobile = u.IsMobile()
		} else {
			slog.Warn("dispatch HARD: user lookup failed; mobile-badge omitted", "user_id", userID, "err", err)
		}
		// Tunnel checks get neighbour context: list other tunnel_* siblings
		// so the operator can see at a glance whether this is one tunnel
		// flapping or the whole router being unreachable.
		if strings.HasPrefix(checkName, "tunnel_") {
			args.Neighbors = di.collectNeighbors(userID, checkName)
		}
		text := FormatHard(args)
		// Per-category command-channel buttons:
		// - tunnel_* checks → restart/diag/pingcheck (awg-manager actions on a tunnel)
		// - mobile-router heartbeat → force_recheck (poke a 4G router into a fresh report)
		var opts []tg.KeyboardOption
		if strings.HasPrefix(checkName, "tunnel_") {
			opts = append(opts, tg.WithTunnelActions())
		}
		if args.IsMobile && checkName == "agent_heartbeat" {
			opts = append(opts, tg.WithMobileActions())
		}
		kb := tg.HardAlertKeyboard(userID, checkName, opts...)
		sendHard := func(tid int64) (int64, error) {
			return di.tg.SendMessageWithKeyboard(ctx, di.cfg.ChatID, &tid, text, "", nil, &kb)
		}
		mid, err := sendHard(threadID)
		if err != nil {
			mid, err = di.retryOnStaleTopic(ctx, userID, nickname, err, sendHard)
		}
		if err != nil {
			return fmt.Errorf("HARD tg send %s/%s: %w", nickname, checkName, err)
		}
		next.LastAlertMsgID = &mid
		now := time.Now()
		next.LastAlertAt = &now
		return di.d.State().Save(userID, checkName, next)
	case state.Recovery:
		// Same ordering invariant как Hard: state-Save до TG-send. Если
		// TG отвалится, recovery фактически уже сохранён в FSM; следующий
		// OK-репорт превратится в Noop (state==ok), не в дубль Recovery.
		threadID, err := di.ensureTopic(ctx, userID, nickname)
		if err != nil {
			return fmt.Errorf("ensure topic for recovery %s/%s: %w", nickname, checkName, err)
		}
		prev, prevErr := di.d.State().Get(userID, checkName)
		if prevErr != nil {
			slog.Warn("recovery: state.Get failed; rendering without HardSince", "user_id", userID, "check", checkName, "err", prevErr)
		}
		var hardSince time.Time
		if prev.HardSince != nil {
			hardSince = *prev.HardSince
		}
		next := tr.Next
		next.LastAlertMsgID = nil
		next.LastAlertAt = nil
		next.Acked = false // defensive (FSM also sets this in Recovery transition)
		if err := di.d.State().Save(userID, checkName, next); err != nil {
			return fmt.Errorf("save Recovery state %s/%s: %w", nickname, checkName, err)
		}
		text := FormatRecovery(RecoveryArgs{
			Nickname:    nickname,
			CheckName:   checkName,
			HardSince:   hardSince,
			RecoveredAt: time.Now(),
		})
		sendRecovery := func(tid int64) (int64, error) {
			return di.tg.SendMessage(ctx, di.cfg.ChatID, &tid, text, "", prev.LastAlertMsgID)
		}
		if _, err := sendRecovery(threadID); err != nil {
			if _, err = di.retryOnStaleTopic(ctx, userID, nickname, err, sendRecovery); err != nil {
				return fmt.Errorf("recovery tg send %s/%s: %w", nickname, checkName, err)
			}
		}
		return nil
	}
	return nil
}

// collectNeighbors returns short summaries of the user's other tunnel checks.
// On any DB error we silently return nil — neighbour context is decoration,
// not the load-bearing payload, and we'd rather send a slightly thinner
// alert than no alert at all.
func (di *Dispatcher) collectNeighbors(userID int64, excludeCheck string) []NeighborSummary {
	rows, err := di.d.Events().LatestEventsByPrefix(userID, "tunnel_")
	if err != nil {
		slog.Warn("collectNeighbors: events lookup failed", "user_id", userID, "err", err)
		return nil
	}
	return BuildNeighborSummaries(rows, excludeCheck)
}

// BuildNeighborSummaries projects events.LatestEventsByPrefix rows into
// renderable []NeighborSummary, applying the ping_check_status override and
// JSON detail extraction that both dispatcher and realert.poller need
// identically (LOGIC-08). Single source of truth for "neighbour view".
func BuildNeighborSummaries(rows []db.EventRow, excludeCheck string) []NeighborSummary {
	var out []NeighborSummary
	for _, r := range rows {
		if r.CheckName == excludeCheck {
			continue
		}
		ns := NeighborSummary{CheckName: r.CheckName, Status: r.Status}
		if r.DetailsJSON != "" && r.DetailsJSON != "null" {
			var details map[string]any
			if err := json.Unmarshal([]byte(r.DetailsJSON), &details); err == nil {
				ns.TunnelName, _ = details["tunnel_name"].(string)
				ns.Interface, _ = details["interface"].(string)
				if pcStatus, ok := details["ping_check_status"].(string); ok && pcStatus != "" {
					ns.Status = pcStatus
				}
				if v, ok := details["handshake_age_sec"].(float64); ok {
					ns.HandshakeAge = int(v)
				}
			}
		}
		out = append(out, ns)
	}
	return out
}

// SendOffline sends a ROUTER OFFLINE notice (used by the heartbeat watcher).
func (di *Dispatcher) SendOffline(ctx context.Context, userID int64, nickname string, since time.Duration) error {
	threadID, err := di.ensureTopic(ctx, userID, nickname)
	if err != nil {
		return err
	}
	text := FormatRouterOffline(nickname, since)
	sendOffline := func(tid int64) (int64, error) {
		return di.tg.SendMessage(ctx, di.cfg.ChatID, &tid, text, "", nil)
	}
	if _, err = sendOffline(threadID); err != nil {
		_, err = di.retryOnStaleTopic(ctx, userID, nickname, err, sendOffline)
	}
	return err
}

// retryOnStaleTopic handles the case where SendMessage* failed because TG
// reports the cached topic_id no longer exists (e.g. the operator deleted
// the forum topic manually). When tg.IsTopicNotFound(initialErr) holds we
// clear users.telegram_thread_id, recreate the topic via ensureTopic, and
// invoke send once more with the fresh id. Other errors are returned
// untouched. Only a single retry is attempted — if the recreate-then-send
// also fails, the original error is wrapped so the caller sees both.
func (di *Dispatcher) retryOnStaleTopic(
	ctx context.Context,
	userID int64,
	nickname string,
	initialErr error,
	send func(threadID int64) (int64, error),
) (int64, error) {
	if !tg.IsTopicNotFound(initialErr) {
		return 0, initialErr
	}
	slog.Warn("dispatch: stale forum topic; clearing and recreating",
		"user_id", userID, "nickname", nickname, "err", initialErr)
	if err := di.d.Users().ClearThreadID(userID); err != nil {
		return 0, fmt.Errorf("self-heal clear thread id: %w (orig: %v)", err, initialErr)
	}
	newID, err := di.ensureTopic(ctx, userID, nickname)
	if err != nil {
		return 0, fmt.Errorf("self-heal recreate topic: %w (orig: %v)", err, initialErr)
	}
	return send(newID)
}

// ensureTopic returns a forum_topic id for `nickname`, creating one if
// missing. Fast path is lock-free; on cache miss it takes the dispatcher
// mutex and double-checks before delegating to EnsureTopicForUser so
// concurrent alerts for the same user don't race to create duplicate
// topics in TG.
func (di *Dispatcher) ensureTopic(ctx context.Context, userID int64, nickname string) (int64, error) {
	u, err := di.d.Users().GetByNickname(nickname)
	if err != nil {
		return 0, err
	}
	if u.TelegramThreadID != nil {
		return *u.TelegramThreadID, nil
	}
	di.mu.Lock()
	defer di.mu.Unlock()
	// Double-check under lock — another goroutine may have created the
	// topic while we waited.
	u2, err := di.d.Users().GetByID(u.ID)
	if err == nil && u2 != nil && u2.TelegramThreadID != nil {
		return *u2.TelegramThreadID, nil
	}
	tid, err := EnsureTopicForUser(ctx, di.tg, di.d, di.cfg.ChatID, u.ID, false)
	if err != nil {
		return 0, err
	}
	// Fresh create — send welcome so reply-keyboard attaches to the topic.
	// Non-fatal: log and continue if welcome fails. The topic is usable
	// without it (it appears on first alert).
	if di.WelcomeKeyboard != nil {
		if werr := SendWelcome(ctx, di.tg, di.cfg.ChatID, tid, nickname, di.WelcomeKeyboard()); werr != nil {
			slog.Warn("welcome send failed (non-fatal)", "user", nickname, "err", werr)
		}
	}
	return tid, nil
}

