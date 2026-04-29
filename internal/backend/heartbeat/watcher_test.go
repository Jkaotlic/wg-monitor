// internal/backend/heartbeat/watcher_test.go
package heartbeat

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
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

func TestWatcherFiresOnceAfterStaleness(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	old := time.Now().Add(-10 * time.Minute).UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", old)

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: 25 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	w.WaitForExit()

	off.mu.Lock()
	defer off.mu.Unlock()
	if len(off.calls) == 0 {
		t.Fatal("expected at least one offline notice")
	}
	if off.calls[0].nick != "vasya" {
		t.Fatalf("nick: %s", off.calls[0].nick)
	}
}

func TestWatcherDoesNotFireWhenFresh(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", time.Now().UTC())

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: 25 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	w.WaitForExit()

	off.mu.Lock()
	defer off.mu.Unlock()
	if len(off.calls) != 0 {
		t.Fatalf("got %d calls for fresh user", len(off.calls))
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
	// Last event 30 minutes ago — over the static 5min threshold but well
	// inside the mobile 60min threshold. Watcher must NOT alert.
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", time.Now().Add(-30*time.Minute).UTC())

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{
		StaleAfterStatic: 5 * time.Minute,
		StaleAfterMobile: 60 * time.Minute,
		ScanEvery:        25 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	w.WaitForExit()

	off.mu.Lock()
	defer off.mu.Unlock()
	if len(off.calls) != 0 {
		t.Fatalf("got %d offline calls for mobile user inside grace window", len(off.calls))
	}
}

func TestWatcherMobileFiresAfterMobileThreshold(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55ff55"
	uid, _ := d.Users().InsertWithKind("carvan2", tok, "1.1.1.1", "nwg0", db.KindMobile)
	// 90min ago — past the mobile threshold. Watcher should fire.
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", time.Now().Add(-90*time.Minute).UTC())

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{
		StaleAfterStatic: 5 * time.Minute,
		StaleAfterMobile: 60 * time.Minute,
		ScanEvery:        25 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	w.WaitForExit()

	off.mu.Lock()
	defer off.mu.Unlock()
	if len(off.calls) == 0 {
		t.Fatal("expected mobile router to alert past its threshold")
	}
}

func TestWatcherSuppressesOfflineAfterResume(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1010101010101010101010101010101010101010101010101010101010101010"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", time.Now().Add(-10*time.Minute).UTC())

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{
		StaleAfterStatic: 5 * time.Minute,
		ResumeGrace:      200 * time.Millisecond,
		ScanEvery:        25 * time.Millisecond,
	})
	w.MarkResumed(uid) // simulate the agent's Resumed=true report just landing
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	time.Sleep(80 * time.Millisecond) // still inside grace
	off.mu.Lock()
	if len(off.calls) != 0 {
		off.mu.Unlock()
		cancel()
		w.WaitForExit()
		t.Fatalf("OFFLINE fired during resume grace: %d calls", len(off.calls))
	}
	off.mu.Unlock()

	// Wait past grace — now the watcher must alert.
	time.Sleep(250 * time.Millisecond)
	cancel()
	w.WaitForExit()

	off.mu.Lock()
	defer off.mu.Unlock()
	if len(off.calls) == 0 {
		t.Fatal("expected OFFLINE after resume grace expired")
	}
}

var _ atomic.Value // keep the import alive in case of future use
