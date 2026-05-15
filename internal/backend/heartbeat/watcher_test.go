// internal/backend/heartbeat/watcher_test.go
package heartbeat

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
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
	uid, err := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
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
		MobileLifecycle:  false, // explicitly legacy: this test asserts the StaleAfterMobile path
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
		MobileLifecycle:  false, // explicitly legacy: this test asserts the StaleAfterMobile path
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

// Regression: full lifecycle — never-reported user → events arrive → events
// stop again → second OFFLINE must fire. Earlier the cooldown/dedup logic
// was hand-traced for each branch in isolation; this exercises the full
// state-machine across the IsZero fallback path.
func TestWatcherLifecycle_NeverReported_ThenReports_ThenSilent(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "5050505050505050505050505050505050505050505050505050505050505050"
	uid, _ := d.Users().Insert("loop", tok, "1.1.1.1", "awg0")
	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{
		StaleAfterStatic: 5 * time.Minute,
		RenotifyEvery:    6 * time.Hour,
		ScanEvery:        time.Hour,
	})
	t0 := time.Now().UTC()

	// Phase 1: 30 min past CreatedAt, no events → first OFFLINE fires.
	driveScan(w, t0.Add(30*time.Minute))
	if got := len(off.snapshot()); got != 1 {
		t.Fatalf("phase 1 expected 1 OFFLINE (never reported), got %d", got)
	}

	// Phase 2: agent finally reports. notified[] cleared, no further alerts.
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", t0.Add(35*time.Minute))
	driveScan(w, t0.Add(36*time.Minute))
	if got := len(off.snapshot()); got != 1 {
		t.Fatalf("phase 2 events arrived, expected no new alerts, got %d", got)
	}

	// Phase 3: agent goes silent for >threshold; second OFFLINE must fire.
	driveScan(w, t0.Add(45*time.Minute))
	if got := len(off.snapshot()); got != 2 {
		t.Fatalf("phase 3 expected 2nd OFFLINE after silence, got %d", got)
	}
}

// Regression: a user added but whose agent never sends a heartbeat must not
// stay invisible forever. The watcher should treat User.CreatedAt as the
// "earliest possible heartbeat" so the same stale threshold gives a grace
// window for fresh deploys, then surface OFFLINE if still nothing arrives.
func TestWatcherFires_NeverReported_PastGrace(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "3030303030303030303030303030303030303030303030303030303030303030"
	_, _ = d.Users().Insert("ghost", tok, "1.1.1.1", "awg0")
	// No events inserted — agent never started.
	now := time.Now().UTC()
	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfterStatic: 5 * time.Minute, ScanEvery: time.Hour})
	// Pin the clock 30 minutes after Insert — past the 5-min static threshold.
	driveScan(w, now.Add(30*time.Minute))
	if got := len(off.snapshot()); got != 1 {
		t.Fatalf("expected OFFLINE for never-reported user past grace, got %d calls", got)
	}
}

func TestWatcherSilent_NeverReported_InsideGrace(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "4040404040404040404040404040404040404040404040404040404040404040"
	_, _ = d.Users().Insert("fresh", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfterStatic: 5 * time.Minute, ScanEvery: time.Hour})
	// Scan at +2 min — still inside onboarding grace window.
	driveScan(w, now.Add(2*time.Minute))
	if got := len(off.snapshot()); got != 0 {
		t.Fatalf("freshly-added user inside grace must not alert, got %d", got)
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

type fakeSleep struct {
	mu    sync.Mutex
	calls []sleepRec
}

type sleepRec struct {
	userID   int64
	nick     string
	lastSeen time.Time
}

func (f *fakeSleep) SendSleeping(_ context.Context, uid int64, nick string, ls time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sleepRec{uid, nick, ls})
	return nil
}

func (f *fakeSleep) snapshot() []sleepRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sleepRec, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestWatcherMobileLifecycle_SleepInfoAfter5Min(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-6*time.Minute))

	off := &fakeOffline{}
	sleep := &fakeSleep{}
	w := NewWatcher(d, off, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now)

	if got := len(sleep.snapshot()); got != 1 {
		t.Fatalf("sleep notifier: want 1 call, got %d", got)
	}
	if got := len(off.snapshot()); got != 0 {
		t.Fatalf("offline notifier: want 0 calls, got %d", got)
	}
}

func TestWatcherMobileLifecycle_NoRenotifyOnRepeatScan(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-6*time.Minute))

	sleep := &fakeSleep{}
	w := NewWatcher(d, &fakeOffline{}, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now)
	driveScan(w, now.Add(30*time.Second))
	driveScan(w, now.Add(2*time.Minute))

	if got := len(sleep.snapshot()); got != 1 {
		t.Fatalf("repeat scans must not refire sleep notification; got %d", got)
	}
}

func TestWatcherMobileLifecycle_ResumeClearsSleepFlag(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-6*time.Minute))

	sleep := &fakeSleep{}
	w := NewWatcher(d, &fakeOffline{}, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now) // fires sleep #1
	// agent comes back: fresh heartbeat
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(time.Minute))
	driveScan(w, now.Add(time.Minute+1*time.Second)) // fresh, clears sleepNotified
	// goes silent again
	driveScan(w, now.Add(7*time.Minute)) // should fire sleep #2

	if got := len(sleep.snapshot()); got != 2 {
		t.Fatalf("expected sleep to refire after a fresh report round; got %d", got)
	}
}

func TestWatcherMobileLifecycle_FreshUserNoSleepInfo(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	now := time.Now().UTC()
	// Fresh user inserted 2min ago, no events: latest will fall back to CreatedAt.
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-2*time.Minute))

	sleep := &fakeSleep{}
	w := NewWatcher(d, &fakeOffline{}, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now)

	if got := len(sleep.snapshot()); got != 0 {
		t.Fatalf("fresh mobile user must not fire sleep info; got %d", got)
	}
}

func TestWatcherStaticUnaffectedByMobileLifecycle(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00"
	uid, _ := d.Users().Insert("homerouter", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-10*time.Minute))

	off := &fakeOffline{}
	sleep := &fakeSleep{}
	w := NewWatcher(d, off, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		StaleAfterStatic: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now)

	if got := len(off.snapshot()); got != 1 {
		t.Fatalf("static must hit SendOffline once; got %d", got)
	}
	if got := len(sleep.snapshot()); got != 0 {
		t.Fatalf("static must NOT hit SendSleeping; got %d", got)
	}
}
