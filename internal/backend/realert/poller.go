// Package realert sends a STILL-DOWN reminder for HARD incidents older than
// `RealertEvery` (per spec §5.3). Tick cadence is decoupled (typically 5 min);
// the actual realert interval is enforced via StaleHards SQL filter.
package realert

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/pkg/wire"
)

type TGSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

type Config struct {
	ChatID       int64
	RealertEvery time.Duration // default 6h
	TickEvery    time.Duration // default 5min
}

const (
	defaultRealertEvery = 6 * time.Hour
	defaultTickEvery    = 5 * time.Minute
)

type Poller struct {
	d   *db.DB
	tg  TGSender
	cfg Config
	wg  sync.WaitGroup
	now func() time.Time // injectable for tests
}

func NewPoller(d *db.DB, tg TGSender, cfg Config) *Poller {
	if cfg.RealertEvery <= 0 {
		cfg.RealertEvery = defaultRealertEvery
	}
	if cfg.TickEvery <= 0 {
		cfg.TickEvery = defaultTickEvery
	}
	return &Poller{d: d, tg: tg, cfg: cfg, now: time.Now}
}

// SetNow overrides the clock used by tick(). Test-only.
func (p *Poller) SetNow(now func() time.Time) {
	if now != nil {
		p.now = now
	}
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

// lastKnownCheck loads the most recent event row for (userID, checkName) and
// reconstructs a wire.Check from its details_json. Returns zero Check if no
// event exists or unmarshal fails — formatter degrades gracefully.
func (p *Poller) lastKnownCheck(userID int64, checkName string) wire.Check {
	row, ok, err := p.d.Events().LatestEvent(userID, checkName)
	if err != nil || !ok {
		return wire.Check{}
	}
	c := wire.Check{Name: row.CheckName, Status: row.Status}
	if row.DetailsJSON != "" {
		_ = json.Unmarshal([]byte(row.DetailsJSON), &c.Details)
	}
	return c
}

// neighborSummaries returns short status of OTHER tunnel_* checks for the same
// user — context shown next to a failing tunnel ("are siblings dead too?").
// Returns nil for non-tunnel checks. Now shares projection logic with the
// dispatcher via alerts.BuildNeighborSummaries (LOGIC-08).
func (p *Poller) neighborSummaries(userID int64, checkName string) []alerts.NeighborSummary {
	if !strings.HasPrefix(checkName, "tunnel_") {
		return nil
	}
	rows, err := p.d.Events().LatestEventsByPrefix(userID, "tunnel_")
	if err != nil {
		slog.Warn("realert: neighborSummaries events lookup failed", "user_id", userID, "err", err)
		return nil
	}
	return alerts.BuildNeighborSummaries(rows, checkName)
}

func (p *Poller) tick(ctx context.Context) {
	now := p.now()
	cutoff := now.Add(-p.cfg.RealertEvery)
	stale, err := p.d.State().StaleHards(cutoff)
	if err != nil {
		slog.Error("realert: StaleHards query failed", "err", err)
		return
	}
	// Pre-fetch users once to avoid the per-incident GetByID lookup (DB-10).
	users, err := p.d.Users().GetAll()
	if err != nil {
		slog.Warn("realert: users.GetAll failed; falling back to per-incident lookup", "err", err)
	}
	usersByID := make(map[int64]db.User, len(users))
	for _, u := range users {
		usersByID[u.ID] = u
	}
	for _, sh := range stale {
		u, ok := usersByID[sh.UserID]
		if !ok {
			one, err := p.d.Users().GetByID(sh.UserID)
			if err != nil {
				slog.Warn("realert: user lookup failed (orphan?)", "user_id", sh.UserID, "err", err)
				continue
			}
			u = *one
		}
		st, err := p.d.State().Get(sh.UserID, sh.CheckName)
		if err != nil {
			slog.Error("realert: state get failed", "user_id", sh.UserID, "check", sh.CheckName, "err", err)
			continue
		}
		if st.HardSince == nil {
			slog.Warn("realert: HardSince nil despite hard status", "user_id", sh.UserID, "check", sh.CheckName)
			continue
		}
		count := int(now.Sub(*st.HardSince) / p.cfg.RealertEvery)
		check := p.lastKnownCheck(sh.UserID, sh.CheckName)
		neighbors := p.neighborSummaries(sh.UserID, sh.CheckName)
		text := alerts.FormatRealert(alerts.RealertArgs{
			Nickname:      u.Nickname,
			CheckName:     sh.CheckName,
			HardSince:     *st.HardSince,
			RealertCount:  count,
			IsMobile:      u.IsMobile(),
			Check:         check,
			Neighbors:     neighbors,
			RealertEvery:  p.cfg.RealertEvery,
		})
		_, err = p.tg.SendMessage(ctx, p.cfg.ChatID, u.TelegramThreadID, text, "", nil)
		if err != nil {
			slog.Error("realert: tg send failed", "user_id", sh.UserID, "check", sh.CheckName, "err", err)
			continue // do not advance LastAlertAt → retry next tick
		}
		// Realerts are sent as standalone messages (replyTo nil above);
		// LastAlertMsgID intentionally not updated so RECOVERY (dispatcher.go)
		// replies to the original HARD root, not the most recent reminder.
		// Use BumpLastAlertAt instead of Save to avoid race-overwriting an FSM
		// Recovery that occurred between StaleHards and this update.
		if err := p.d.State().BumpLastAlertAt(sh.UserID, sh.CheckName, now); err != nil {
			slog.Error("realert: bump LastAlertAt failed", "user_id", sh.UserID, "check", sh.CheckName, "err", err)
		}
	}
}
