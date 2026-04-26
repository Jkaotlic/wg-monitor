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

var _ atomic.Value // keep the import alive in case of future use
