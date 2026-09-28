package wakehook

import (
	"errors"
	"testing"
	"time"
)

type fakeFile struct {
	mtime  time.Time
	exists bool
}

func (f *fakeFile) stat(string) (time.Time, error) {
	if !f.exists {
		return time.Time{}, errors.New("no such file")
	}
	return f.mtime, nil
}

func newWatcher(f *fakeFile, wakes *int) *Watcher {
	return &Watcher{Path: "/opt/var/run/wg-monitor.wake", Stat: f.stat, Throttle: &Throttle{},
		Wake: func() { *wakes++ }, Since: t0}
}

func TestWatcherIgnoresBaselineAtStart(t *testing.T) {
	f := &fakeFile{mtime: t0.Add(-time.Hour), exists: true}
	wakes := 0
	w := newWatcher(f, &wakes)
	for i := 0; i < 10; i++ {
		w.Tick(t0.Add(time.Duration(i) * 2 * time.Second))
	}
	if wakes != 0 {
		t.Fatalf("старый файл при старте -- не событие: %d", wakes)
	}
}

// Флаппинг: десять касаний за пять секунд -- одно пробуждение.
func TestWatcherStormGivesOneWake(t *testing.T) {
	f := &fakeFile{mtime: t0.Add(-time.Hour), exists: true}
	wakes := 0
	w := newWatcher(f, &wakes)
	w.Tick(t0)
	for i := 1; i <= 10; i++ {
		now := t0.Add(time.Duration(i) * 500 * time.Millisecond)
		f.mtime = now
		w.Tick(now)
	}
	for i := 6; i <= 20; i++ {
		w.Tick(t0.Add(time.Duration(i) * time.Second))
	}
	if wakes != 1 {
		t.Fatalf("пробуждений %d, хотим 1", wakes)
	}
}

func TestWatcherFileAppearsLater(t *testing.T) {
	f := &fakeFile{}
	wakes := 0
	w := newWatcher(f, &wakes)
	w.Tick(t0)
	f.exists, f.mtime = true, t0.Add(time.Second)
	w.Tick(t0.Add(2 * time.Second))
	w.Tick(t0.Add(8 * time.Second))
	if wakes != 1 {
		t.Fatalf("первое касание хука после старта обязано будить: %d", wakes)
	}
}

func TestWatcherFacts(t *testing.T) {
	f := &fakeFile{mtime: t0.Add(-time.Hour), exists: true}
	wakes := 0
	w := newWatcher(f, &wakes)
	w.Now = func() time.Time { return t0.Add(time.Minute) }
	w.Tick(t0)
	f.mtime = t0.Add(time.Second)
	w.Tick(t0.Add(time.Second))
	w.Tick(t0.Add(7 * time.Second))
	got := w.Facts(StateInstalled, "")
	if got.State != StateInstalled || got.Wakes1h != 1 || got.LastWakeAt == nil || !got.At.Equal(t0) {
		t.Fatalf("facts = %+v", got)
	}
}
