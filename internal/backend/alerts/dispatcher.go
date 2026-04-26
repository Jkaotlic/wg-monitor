package alerts

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
)

type TGSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
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

func (di *Dispatcher) Handle(ctx context.Context, userID int64, nickname, checkName string, tr state.Transition, detail string) error {
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
		text := FormatHard(HardArgs{
			Nickname:    nickname,
			CheckName:   checkName,
			ConsecFails: tr.Next.ConsecutiveFails,
			HardSince:   *tr.Next.HardSince,
			Detail:      detail,
		})
		mid, err := di.tg.SendMessage(ctx, di.cfg.ChatID, &threadID, text, "", nil)
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
		return di.d.State().Save(userID, checkName, next)
	}
	return nil
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
