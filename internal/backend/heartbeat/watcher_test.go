// internal/backend/heartbeat/watcher_test.go
package heartbeat

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type fakeOffline struct {
	mu    sync.Mutex
	calls []callRec
}

type callRec struct {
	userID int64
	nick   string
	since  time.Duration
}

func (f *fakeOffline) SendOffline(_ context.Context, uid int64, nick string, since time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, callRec{uid, nick, since})
	return nil
}

func (f *fakeOffline) snapshot() []callRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]callRec, len(f.calls))
	copy(out, f.calls)
	return out
}

// driveScan invokes scan synchronously with a fixed wall-clock. Avoids the
// flaky time.Sleep dance against Run's goroutine ticker.
func driveScan(w *Watcher, fixed time.Time) {
	w.SetNow(func() time.Time { return fixed })
	w.scan(context.Background())
}

func TestWatcherFiresOnceAfterStaleness(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	old := now.Add(-10 * time.Minute)
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", old)

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: time.Hour})

	driveScan(w, now)

	calls := off.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 offline notice, got %d", len(calls))
	}
	if calls[0].nick != "vasya" {
		t.Fatalf("nick: %s", calls[0].nick)
	}

	// Second scan at the same instant must not refire (cooldown / dedup logic).
	driveScan(w, now)
	if got := len(off.snapshot()); got != 1 {
		t.Fatalf("expected dedup to keep 1 call, got %d", got)
	}
}

func TestWatcherDoesNotFireWhenFresh(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now)

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: time.Hour})

	driveScan(w, now)

	if got := len(off.snapshot()); got != 0 {
		t.Fatalf("got %d calls for fresh user", got)
	}
}

func TestWatcherMobileUsesLongerGrace(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "ee44ee44ee44ee44ee44ee44ee44ee44ee44ee44ee44ee44ee44ee44ee44ee44"
	uid, err := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Last event 30 minutes ago — over the static 5min threshold but well
	// inside the mobile 60min threshold. Watcher must NOT alert.
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-30*time.Minute))

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{
		StaleAfterStatic: 5 * time.Minute,
		StaleAfterMobile: 60 * time.Minute,
		ScanEvery:        time.Hour,
	})

	driveScan(w, now)

	if got := len(off.snapshot()); got != 0 {
		t.Fatalf("got %d offline calls for mobile user inside grace window", got)
	}
}

func TestWatcherMobileFiresAfterMobileThreshold(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55"
	uid, _ := d.Users().InsertWithKind("carvan2", tok, "1.1.1.1", "nwg0", db.KindMobile)
	now := time.Now().UTC()
	// 90min ago — past the mobile threshold. Watcher should fire.
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-90*time.Minute))

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{
		StaleAfterStatic: 5 * time.Minute,
		StaleAfterMobile: 60 * time.Minute,
		ScanEvery:        time.Hour,
	})

	driveScan(w, now)

	if got := len(off.snapshot()); got == 0 {
		t.Fatal("expected mobile router to alert past its threshold")
	}
}

func TestWatcherSuppressesOfflineAfterResume(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1010101010101010101010101010101010101010101010101010101010101010"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	t0 := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", t0.Add(-10*time.Minute))

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{
		StaleAfterStatic: 5 * time.Minute,
		ResumeGrace:      200 * time.Millisecond,
		ScanEvery:        time.Hour,
	})

	// Pin clock to t0 for MarkResumed and the in-grace scan.
	w.SetNow(func() time.Time { return t0 })
	w.MarkResumed(uid)
	w.scan(context.Background())

	if got := len(off.snapshot()); got != 0 {
		t.Fatalf("OFFLINE fired during resume grace: %d calls", got)
	}

	// Advance past grace — watcher must alert.
	t1 := t0.Add(500 * time.Millisecond)
	w.SetNow(func() time.Time { return t1 })
	w.scan(context.Background())

	if got := len(off.snapshot()); got == 0 {
		t.Fatal("expected OFFLINE after resume grace expired")
	}
}

func TestWatcherRefiresAfterCooldown(t *testing.T) {
	// Covers the 6h re-notify branch deterministically without sleeping for hours.
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "2020202020202020202020202020202020202020202020202020202020202020"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	t0 := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", t0.Add(-10*time.Minute))

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: time.Hour})

	driveScan(w, t0)
	if got := len(off.snapshot()); got != 1 {
		t.Fatalf("first scan: expected 1 call, got %d", got)
	}
	// Within the cooldown window — must NOT refire.
	driveScan(w, t0.Add(time.Hour))
	if got := len(off.snapshot()); got != 1 {
		t.Fatalf("scan inside cooldown: expected 1 call, got %d", got)
	}
	// Past 6h — must refire.
	driveScan(w, t0.Add(7*time.Hour))
	if got := len(off.snapshot()); got != 2 {
		t.Fatalf("scan past cooldown: expected 2 calls, got %d", got)
	}
}
