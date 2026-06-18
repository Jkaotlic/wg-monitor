// Package digest sends a once-a-day "dead-man" heartbeat to the primary chat:
// a short "🟢 monitor alive — N/M routers online" message. Its purpose is the
// inverse of an alert — its ABSENCE is the signal. If the daily digest stops
// arriving, the operator knows the backend itself is down (a crashed/​hung
// backend can't alert about its own death; see docs/external-uptime-probe.md).
// This is a soft, in-process complement to a real external probe, not a
// replacement for it.
package digest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type Sender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

type Config struct {
	ChatID       int64         // primary chat; digest posts to General (threadID nil)
	HourMSK      int           // 0-23, hour of day in MSK to send (default 9)
	OnlineWindow time.Duration // a router is "online" if its latest event is fresher than this (default 10m)
	TickEvery    time.Duration // how often to check the clock (default 5m)
}

const (
	defaultHourMSK      = 9
	defaultOnlineWindow = 10 * time.Minute
	defaultTickEvery    = 5 * time.Minute
)

type Poller struct {
	d   *db.DB
	tg  Sender
	cfg Config
	now func() time.Time
	wg  sync.WaitGroup

	mu       sync.Mutex
	lastSent string // YYYY-MM-DD (MSK) of the last digest sent — daily dedupe
}

func NewPoller(d *db.DB, tg Sender, cfg Config) *Poller {
	if cfg.HourMSK < 0 || cfg.HourMSK > 23 {
		cfg.HourMSK = defaultHourMSK
	}
	if cfg.OnlineWindow <= 0 {
		cfg.OnlineWindow = defaultOnlineWindow
	}
	if cfg.TickEvery <= 0 {
		cfg.TickEvery = defaultTickEvery
	}
	return &Poller{d: d, tg: tg, cfg: cfg, now: time.Now}
}

// SetNow overrides the wall-clock seam for deterministic tests. Must be called
// before Run().
func (p *Poller) SetNow(fn func() time.Time) {
	if fn == nil {
		p.now = time.Now
		return
	}
	p.now = fn
}

func (p *Poller) Run(ctx context.Context) {
	p.wg.Add(1)
	defer p.wg.Done()
	t := time.NewTicker(p.cfg.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) WaitForExit() { p.wg.Wait() }

// TickForTest exposes tick() to tests.
func (p *Poller) TickForTest(ctx context.Context) { p.tick(ctx) }

func (p *Poller) tick(ctx context.Context) {
	now := p.now()
	msk := now.In(mscLoc())
	if msk.Hour() != p.cfg.HourMSK {
		return
	}
	today := msk.Format("2006-01-02")
	p.mu.Lock()
	already := p.lastSent == today
	p.mu.Unlock()
	if already {
		return
	}

	users, err := p.d.Users().GetAll()
	if err != nil {
		slog.Warn("digest: users.GetAll failed", "err", err)
		return
	}
	latest, err := p.d.Events().LatestPerUserAll()
	if err != nil {
		slog.Warn("digest: LatestPerUserAll failed; treating routers as offline", "err", err)
		latest = nil
	}

	online := 0
	var offline []string
	for _, u := range users {
		var ts time.Time
		if latest != nil {
			ts = latest[u.ID]
		}
		if !ts.IsZero() && now.Sub(ts) < p.cfg.OnlineWindow {
			online++
		} else {
			offline = append(offline, u.Nickname)
		}
	}

	text := RenderDigest(online, len(users), offline, msk)
	if _, err := p.tg.SendMessage(ctx, p.cfg.ChatID, nil, text, "", nil); err != nil {
		slog.Warn("digest: send failed; will retry within the hour", "err", err)
		return // do NOT mark sent → retry next tick while still in the hour
	}
	p.mu.Lock()
	p.lastSent = today
	p.mu.Unlock()
}

// RenderDigest builds the daily digest body. Plain text (no MarkdownV2 — nicknames
// are user-controlled and would need escaping).
func RenderDigest(online, total int, offline []string, mskNow time.Time) string {
	badge := "🟢"
	switch {
	case total > 0 && online == 0:
		badge = "🔴"
	case online < total:
		badge = "🟡"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s Монитор жив — ежедневная сводка (%s МСК)\n", badge, mskNow.Format("02.01 15:04"))
	fmt.Fprintf(&b, "Роутеры онлайн: %d из %d", online, total)
	if len(offline) > 0 {
		fmt.Fprintf(&b, "\nНе на связи: %s", strings.Join(offline, ", "))
	}
	b.WriteString("\n\nЭто сообщение приходит раз в сутки. Если оно пропало — проверь сам бэкенд: упавший бэкенд алертов не шлёт.")
	return b.String()
}

var (
	mscLocOnce sync.Once
	mscLocVal  *time.Location
)

func mscLoc() *time.Location {
	mscLocOnce.Do(func() {
		loc, err := time.LoadLocation("Europe/Moscow")
		if err != nil {
			mscLocVal = time.FixedZone("МСК", 3*3600)
			return
		}
		mscLocVal = loc
	})
	return mscLocVal
}
