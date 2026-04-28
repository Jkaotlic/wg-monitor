// Package realert sends a STILL-DOWN reminder for HARD incidents older than
// `RealertEvery` (per spec §5.3). Tick cadence is decoupled (typically 5 min);
// the actual realert interval is enforced via StaleHards SQL filter.
package realert

import (
	"context"
	"log/slog"
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
}

func NewPoller(d *db.DB, tg TGSender, cfg Config) *Poller {
	return &Poller{d: d, tg: tg, cfg: cfg}
}

func (p *Poller) Run(ctx context.Context) error {
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
		now := time.Now()
		st.LastAlertAt = &now
		// LastAlertMsgID retained — points to original HARD msg for RECOVERY reply.
		if err := p.d.State().Save(sh.UserID, sh.CheckName, st); err != nil {
			slog.Error("realert: state save failed", "user_id", sh.UserID, "err", err)
		}
	}
}
