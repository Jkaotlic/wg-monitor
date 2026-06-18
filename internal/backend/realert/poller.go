// Package realert sends a STILL-DOWN reminder for HARD incidents older than
// `RealertEvery` (per spec §5.3). Tick cadence is decoupled (typically 5 min);
// the actual realert interval is enforced via StaleHards SQL filter.
package realert

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type TGSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	SendMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup *tg.InlineKeyboardMarkup) (int64, error)
}

type Config struct {
	ChatID             int64
	RealertEvery       time.Duration // default 6h
	MobileRealertEvery time.Duration // default 6h
	TickEvery          time.Duration // default 5min
}

const (
	defaultRealertEvery = 6 * time.Hour
	defaultTickEvery    = 5 * time.Minute
)

type Poller struct {
	d              *db.DB
	tg             TGSender
	cfg            Config
	wg             sync.WaitGroup
	now            func() time.Time // injectable for tests; defaults to time.Now
	rateLimitMu    sync.Mutex
	rateLimitedTil time.Time
	// sendFailCount tracks consecutive TG-send failures per (user,check) so
	// we don't spam ERROR every tick when TG is sustained-down (OBS-11).
	// Reset on success.
	sendFailMu    sync.Mutex
	sendFailCount map[string]int
}

func NewPoller(d *db.DB, tg TGSender, cfg Config) *Poller {
	if cfg.RealertEvery <= 0 {
		cfg.RealertEvery = defaultRealertEvery
	}
	if cfg.MobileRealertEvery <= 0 {
		cfg.MobileRealertEvery = defaultRealertEvery
	}
	if cfg.TickEvery <= 0 {
		cfg.TickEvery = defaultTickEvery
	}
	return &Poller{
		d: d, tg: tg, cfg: cfg, now: time.Now,
		sendFailCount: make(map[string]int),
	}
}

// recordSendOutcome bumps/clears the per-(user,check) consecutive-failure
// counter and returns whether the current outcome should be logged. We log
// the 1st, 5th, and every 50th sustained failure so a chronic TG outage
// produces a small, useful trail instead of one ERROR every realert tick.
func (p *Poller) recordSendOutcome(userID int64, checkName string, ok bool) (logIt bool, count int) {
	key := strconv.FormatInt(userID, 10) + "/" + checkName
	p.sendFailMu.Lock()
	defer p.sendFailMu.Unlock()
	if ok {
		if _, had := p.sendFailCount[key]; had {
			delete(p.sendFailCount, key)
		}
		return false, 0
	}
	p.sendFailCount[key]++
	c := p.sendFailCount[key]
	switch {
	case c == 1, c == 5:
		return true, c
	case c%50 == 0:
		return true, c
	default:
		return false, c
	}
}

func (p *Poller) rateLimitActive(now time.Time) bool {
	p.rateLimitMu.Lock()
	defer p.rateLimitMu.Unlock()
	return !p.rateLimitedTil.IsZero() && now.Before(p.rateLimitedTil)
}

func (p *Poller) markRateLimited(now time.Time, delay time.Duration) {
	if delay <= 0 {
		return
	}
	p.rateLimitMu.Lock()
	defer p.rateLimitMu.Unlock()
	until := now.Add(delay)
	if until.After(p.rateLimitedTil) {
		p.rateLimitedTil = until
	}
}

// SetNow overrides the wall-clock seam for deterministic tests. Must be
// called before Run() starts; production callers must not use this.
func (p *Poller) SetNow(fn func() time.Time) {
	if fn == nil {
		p.now = time.Now
		return
	}
	p.now = fn
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
		if err := json.Unmarshal([]byte(row.DetailsJSON), &c.Details); err != nil {
			slog.Warn("realert: details_json unmarshal failed", "user_id", userID, "check", checkName, "err", err)
		}
	}
	return c
}

// neighborSummaries returns short status of OTHER tunnel_* checks for the same
// user — context shown next to a failing tunnel ("are siblings dead too?").
// Returns nil for non-tunnel checks. Now shares projection logic with the
// dispatcher via alerts.BuildNeighborSummaries (LOGIC-08).
func (p *Poller) neighborSummaries(userID int64, checkName string) []alerts.NeighborSummary {
	if !strings.HasPrefix(checkName, "tunnel_") && checkName != "dns" {
		return nil
	}
	rows, err := p.d.Events().LatestEventsByPrefixSince(userID, "tunnel_", p.now().Add(-alerts.NeighborFreshWindow))
	if err != nil {
		slog.Warn("realert: neighborSummaries events lookup failed", "user_id", userID, "err", err)
		return nil
	}
	return alerts.BuildNeighborSummaries(rows, checkName)
}

func (p *Poller) tick(ctx context.Context) {
	now := p.now()
	if p.rateLimitActive(now) {
		return
	}
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
		cadence := p.cfg.RealertEvery
		if u.IsMobile() {
			cadence = p.cfg.MobileRealertEvery
		}
		if st.LastAlertAt != nil && now.Sub(*st.LastAlertAt) < cadence {
			continue
		}
		count := int(now.Sub(*st.HardSince) / cadence)
		check := p.lastKnownCheck(sh.UserID, sh.CheckName)
		neighbors := p.neighborSummaries(sh.UserID, sh.CheckName)
		text := alerts.FormatRealert(alerts.RealertArgs{
			Nickname:     u.Nickname,
			CheckName:    sh.CheckName,
			HardSince:    *st.HardSince,
			RealertCount: count,
			IsMobile:     u.IsMobile(),
			Check:        check,
			Neighbors:    neighbors,
			RealertEvery: cadence,
		})
		// Mirror the original HARD alert's per-category action buttons so the
		// STILL-DOWN reminder is just as actionable. Without this the reminder —
		// which the operator is MORE likely to actually see than the original —
		// shipped only the silence/ack/mute/history base row, forcing a dig into
		// a panel to restart or diagnose. Keep in sync with alerts.Dispatcher.Handle.
		var opts []tg.KeyboardOption
		if strings.HasPrefix(sh.CheckName, "tunnel_") {
			opts = append(opts, tg.WithTunnelActions())
		}
		if sh.CheckName == "hydraroute" {
			opts = append(opts, tg.WithHydraRouteActions())
		}
		if u.IsMobile() && sh.CheckName == "agent_heartbeat" {
			opts = append(opts, tg.WithMobileActions())
		}
		kb := tg.HardAlertKeyboard(sh.UserID, sh.CheckName, opts...)
		chatID := u.EffectiveTelegramChatID(p.cfg.ChatID)
		_, err = p.tg.SendMessageWithKeyboard(ctx, chatID, u.TelegramThreadID, text, "", nil, &kb)
		if err != nil {
			if logIt, count := p.recordSendOutcome(sh.UserID, sh.CheckName, false); logIt {
				slog.Error("realert: tg send failed", "user_id", sh.UserID, "check", sh.CheckName, "consecutive_fails", count, "err", err)
			}
			if delay, ok := tg.RateLimitDelay(err); ok {
				p.markRateLimited(now, delay)
				return // stop this batch; retry later without advancing LastAlertAt
			}
			continue // do not advance LastAlertAt → retry next tick
		}
		p.recordSendOutcome(sh.UserID, sh.CheckName, true)
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
