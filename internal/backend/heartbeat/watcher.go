// internal/backend/heartbeat/watcher.go
package heartbeat

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type OfflineSender interface {
	SendOffline(ctx context.Context, userID int64, nickname string, since time.Duration) error
}

type Config struct {
	StaleAfter time.Duration
	ScanEvery  time.Duration
}

type Watcher struct {
	d        *db.DB
	off      OfflineSender
	cfg      Config
	notified map[int64]time.Time
	mu       sync.Mutex
	wg       sync.WaitGroup
}

func NewWatcher(d *db.DB, off OfflineSender, cfg Config) *Watcher {
	return &Watcher{d: d, off: off, cfg: cfg, notified: map[int64]time.Time{}}
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
		if stale < w.cfg.StaleAfter {
			w.mu.Lock()
			delete(w.notified, u.ID)
			w.mu.Unlock()
			continue
		}
		w.mu.Lock()
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
