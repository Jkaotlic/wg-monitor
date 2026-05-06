// internal/backend/heartbeat/watcher.go
package heartbeat

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type OfflineSender interface {
	SendOffline(ctx context.Context, userID int64, nickname string, since time.Duration) error
}

// Config controls per-user heartbeat staleness detection.
//
// StaleAfterStatic / StaleAfterMobile pick the threshold based on user.Kind.
// Mobile routers (4G in-vehicle) are expected to drop into tunnels/garages
// for tens of minutes at a time without it meaning anything is wrong.
//
// ResumeGrace suppresses OFFLINE alerts for a brief window after the agent
// has explicitly told us it just resumed (Report.Resumed=true). The agent
// gathers a fresh diagnostic round before sending that report, so we want
// the FSM to ingest its checks and emit accurate signals — not race a
// premature OFFLINE.
//
// StaleAfter is the legacy single-threshold knob. If non-zero and the new
// kind-aware fields are zero, it's used as both static and mobile threshold
// (for backward compatibility with the Stage 1 config layout).
type Config struct {
	StaleAfter       time.Duration // deprecated, see StaleAfter{Static,Mobile}
	StaleAfterStatic time.Duration
	StaleAfterMobile time.Duration
	ResumeGrace      time.Duration
	ScanEvery        time.Duration
}

const (
	defaultStaleAfterStatic = 5 * time.Minute
	defaultStaleAfterMobile = 60 * time.Minute
	defaultResumeGrace      = 90 * time.Second
)

func (c Config) staleFor(u db.User) time.Duration {
	if u.IsMobile() {
		switch {
		case c.StaleAfterMobile > 0:
			return c.StaleAfterMobile
		case c.StaleAfter > 0:
			return c.StaleAfter
		default:
			return defaultStaleAfterMobile
		}
	}
	switch {
	case c.StaleAfterStatic > 0:
		return c.StaleAfterStatic
	case c.StaleAfter > 0:
		return c.StaleAfter
	default:
		return defaultStaleAfterStatic
	}
}

type Watcher struct {
	d        *db.DB
	off      OfflineSender
	cfg      Config
	notified map[int64]time.Time
	resumed  map[int64]time.Time
	mu       sync.Mutex
	wg       sync.WaitGroup
}

func NewWatcher(d *db.DB, off OfflineSender, cfg Config) *Watcher {
	if cfg.ResumeGrace <= 0 {
		cfg.ResumeGrace = defaultResumeGrace
	}
	return &Watcher{
		d: d, off: off, cfg: cfg,
		notified: map[int64]time.Time{},
		resumed:  map[int64]time.Time{},
	}
}

// MarkResumed records that the user's agent reported a Resumed=true tick.
// While the resume mark is younger than cfg.ResumeGrace, the watcher will
// skip OFFLINE notices for that user.
func (w *Watcher) MarkResumed(userID int64) {
	w.mu.Lock()
	w.resumed[userID] = time.Now()
	w.mu.Unlock()
}

func (w *Watcher) Run(ctx context.Context) {
	w.wg.Add(1)
	defer w.wg.Done()
	w.scan(ctx)
	t := time.NewTicker(w.cfg.ScanEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.scan(ctx)
		}
	}
}

func (w *Watcher) WaitForExit() { w.wg.Wait() }

func (w *Watcher) scan(ctx context.Context) {
	users, err := w.d.Users().GetAll()
	if err != nil {
		slog.Warn("heartbeat scan: list users", "err", err)
		return
	}
	now := time.Now()
	for _, u := range users {
		latest, err := w.d.Events().LatestPerUser(u.ID)
		if err != nil {
			continue
		}
		if latest.IsZero() {
			continue
		}
		stale := now.Sub(latest)
		threshold := w.cfg.staleFor(u)
		if stale < threshold {
			w.mu.Lock()
			delete(w.notified, u.ID)
			w.mu.Unlock()
			continue
		}
		w.mu.Lock()
		// Resumed-suppression: skip if we just received a resumed report.
		if rt, ok := w.resumed[u.ID]; ok {
			if now.Sub(rt) < w.cfg.ResumeGrace {
				w.mu.Unlock()
				continue
			}
			// Mark expired — drop it so the map doesn't grow unbounded.
			delete(w.resumed, u.ID)
		}
		last, sent := w.notified[u.ID]
		notify := !sent || now.Sub(last) > 6*time.Hour
		if notify {
			w.notified[u.ID] = now
		}
		w.mu.Unlock()
		if !notify {
			continue
		}
		if err := w.off.SendOffline(ctx, u.ID, u.Nickname, stale); err != nil {
			slog.Warn("heartbeat: send offline failed", "nickname", u.Nickname, "err", err)
		}
	}
}
