package alerts

import (
	"context"
	"encoding/json"
	"fmt"
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
		threadID, err := di.ensureTopic(ctx, userID, nickname)
		if err != nil {
			return fmt.Errorf("ensure topic: %w", err)
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
		}
		// Tunnel checks get neighbour context: list other tunnel_* siblings
		// so the operator can see at a glance whether this is one tunnel
		// flapping or the whole router being unreachable.
		if strings.HasPrefix(checkName, "tunnel_") {
			args.Neighbors = di.collectNeighbors(userID, checkName)
		}
		text := FormatHard(args)
		kb := tg.HardAlertKeyboard(userID, checkName)
		mid, err := di.tg.SendMessageWithKeyboard(ctx, di.cfg.ChatID, &threadID, text, "", nil, &kb)
		if err != nil {
			return err
		}
		next := tr.Next
		next.LastAlertMsgID = &mid
		now := time.Now()
		next.LastAlertAt = &now
		return di.d.State().Save(userID, checkName, next)
	case state.Recovery:
		threadID, err := di.ensureTopic(ctx, userID, nickname)
		if err != nil {
			return fmt.Errorf("ensure topic: %w", err)
		}
		prev, _ := di.d.State().Get(userID, checkName)
		var hardSince time.Time
		if prev.HardSince != nil {
			hardSince = *prev.HardSince
		}
		text := FormatRecovery(RecoveryArgs{
			Nickname:    nickname,
			CheckName:   checkName,
			HardSince:   hardSince,
			RecoveredAt: time.Now(),
		})
		_, err = di.tg.SendMessage(ctx, di.cfg.ChatID, &threadID, text, "", prev.LastAlertMsgID)
		if err != nil {
			return err
		}
		next := tr.Next
		next.LastAlertMsgID = nil
		next.LastAlertAt = nil
		next.Acked = false // defensive (FSM also sets this in Recovery transition)
		return di.d.State().Save(userID, checkName, next)
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
		return nil
	}
	var out []NeighborSummary
	for _, r := range rows {
		if r.CheckName == excludeCheck {
			continue
		}
		ns := NeighborSummary{CheckName: r.CheckName, Status: r.Status}
		var details map[string]any
		if r.DetailsJSON != "" && r.DetailsJSON != "null" {
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
	_, err = di.tg.SendMessage(ctx, di.cfg.ChatID, &threadID, FormatRouterOffline(nickname, since), "", nil)
	return err
}

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
	u, err = di.d.Users().GetByNickname(nickname)
	if err != nil {
		return 0, err
	}
	if u.TelegramThreadID != nil {
		return *u.TelegramThreadID, nil
	}
	tid, err := di.tg.CreateForumTopic(ctx, di.cfg.ChatID, "👤 "+nickname, 0xFF8C00)
	if err != nil {
		return 0, err
	}
	if err := di.d.Users().UpdateThreadID(u.ID, tid); err != nil {
		return 0, err
	}
	return tid, nil
}

