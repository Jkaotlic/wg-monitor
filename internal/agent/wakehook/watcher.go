package wakehook

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const (
	defaultPoll   = 2 * time.Second
	defaultSettle = 5 * time.Second
)

// Watcher раз в Poll смотрит время изменения файла пробуждения. Новое
// касание открывает окно тишины Settle (пачка событий одной смены -- одно
// пробуждение), по его концу Throttle решает, будить ли отчёт.
type Watcher struct {
	Path     string
	Poll     time.Duration
	Settle   time.Duration
	Throttle *Throttle
	Wake     func()
	// Since -- с какого момента хук стоит (время блока фактов: хеш не прыгает).
	Since time.Time
	Stat  func(path string) (time.Time, error)
	Now   func() time.Time

	mu       sync.Mutex
	started  bool
	seen     time.Time
	pending  time.Time
	lastWake time.Time
}

func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now().UTC()
}

func (w *Watcher) stat(p string) (time.Time, error) {
	if w.Stat != nil {
		return w.Stat(p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

func (w *Watcher) throttle() *Throttle {
	if w.Throttle == nil {
		w.Throttle = &Throttle{}
	}
	return w.Throttle
}

// Tick -- один взгляд на файл. Первый взгляд -- точка отсчёта, не событие.
func (w *Watcher) Tick(now time.Time) {
	mtime, err := w.stat(w.Path)
	fire := false
	w.mu.Lock()
	switch {
	case !w.started:
		w.started = true
		if err == nil {
			w.seen = mtime
		}
	case err == nil && mtime.After(w.seen):
		w.seen = mtime
		if w.pending.IsZero() {
			w.pending = now
		}
	}
	settle := w.Settle
	if settle <= 0 {
		settle = defaultSettle
	}
	if !w.pending.IsZero() && now.Sub(w.pending) >= settle {
		w.pending = time.Time{}
		if w.throttle().Allow(now) {
			w.lastWake = now
			fire = true
		}
	}
	w.mu.Unlock()
	if fire && w.Wake != nil {
		w.Wake()
	}
}

func (w *Watcher) Run(ctx context.Context) {
	poll := w.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		w.Tick(w.now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Facts -- блок hooks для отчёта.
func (w *Watcher) Facts(state, errText string) *wire.HookFacts {
	w.mu.Lock()
	defer w.mu.Unlock()
	fired, suppressed := w.throttle().Stats(w.now())
	f := &wire.HookFacts{At: w.Since, State: state, Err: errText, Wakes1h: fired, Suppressed1h: suppressed}
	if !w.lastWake.IsZero() {
		lw := w.lastWake
		f.LastWakeAt = &lw
	}
	return f
}
