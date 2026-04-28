// Package realert sends a STILL-DOWN reminder for HARD incidents older than
// `RealertEvery` (per spec §5.3). Tick cadence is decoupled (typically 5 min);
// the actual realert interval is enforced via StaleHards SQL filter.
package realert

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/db"
)

type TGSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

type Config struct {
	ChatID       int64
	RealertEvery time.Duration // default 6h
	TickEvery    time.Duration // default 5min
}

type Poller struct {
	d   *db.DB
	tg  TGSender
	cfg Config
	wg  sync.WaitGroup
}

func NewPoller(d *db.DB, tg TGSender, cfg Config) *Poller {
	return &Poller{d: d, tg: tg, cfg: cfg}
}

func (p *Poller) Run(ctx context.Context) error {
	p.wg.Add(1)
	defer p.wg.Done()
	t := time.NewTicker(p.cfg.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) WaitForExit() { p.wg.Wait() }

func (p *Poller) tick(ctx context.Context) {
	cutoff := time.Now().Add(-p.cfg.RealertEvery)
	stale, err := p.d.State().StaleHards(cutoff)
	if err != nil {
		slog.Error("realert: StaleHards query failed", "err", err)
		return
	}
	for _, sh := range stale {
		u, err := p.d.Users().GetByID(sh.UserID)
		if err != nil {
			slog.Warn("realert: user lookup failed (orphan?)", "user_id", sh.UserID, "err", err)
			continue
		}
		st, err := p.d.State().Get(sh.UserID, sh.CheckName)
		if err != nil {
			slog.Error("realert: state get failed", "user_id", sh.UserID, "err", err)
			continue
		}
		if st.HardSince == nil {
			slog.Warn("realert: HardSince nil despite hard status", "user_id", sh.UserID)
			continue
		}
		count := int(time.Since(*st.HardSince) / p.cfg.RealertEvery)
		text := alerts.FormatRealert(alerts.RealertArgs{
			Nickname:     u.Nickname,
			CheckName:    sh.CheckName,
			HardSince:    *st.HardSince,
			RealertCount: count,
		})
		_, err = p.tg.SendMessage(ctx, p.cfg.ChatID, u.TelegramThreadID, text, "", nil)
		if err != nil {
			slog.Error("realert: tg send failed", "user_id", sh.UserID, "err", err)
			continue // do not advance LastAlertAt → retry next tick
		}
		// Realerts are sent as standalone messages (replyTo nil above);
		// LastAlertMsgID intentionally not updated so RECOVERY (dispatcher.go)
		// replies to the original HARD root, not the most recent reminder.
		// Use BumpLastAlertAt instead of Save to avoid race-overwriting an FSM
		// Recovery that occurred between StaleHards and this update.
		if err := p.d.State().BumpLastAlertAt(sh.UserID, sh.CheckName, time.Now()); err != nil {
			slog.Error("realert: bump LastAlertAt failed", "user_id", sh.UserID, "err", err)
		}
	}
}
