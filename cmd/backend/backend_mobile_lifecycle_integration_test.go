package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type captureTG struct {
	mu    sync.Mutex
	sends []capSend
}

type capSend struct {
	chatID   int64
	threadID *int64
	text     string
}

func (c *captureTG) SendMessage(_ context.Context, chatID int64, threadID *int64, text, _ string, _ *int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends = append(c.sends, capSend{chatID, threadID, text})
	return int64(len(c.sends)), nil
}

func (c *captureTG) snapshot() []capSend {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capSend, len(c.sends))
	copy(out, c.sends)
	return out
}

func TestIntegration_MobileLifecycle_WakeAndSleep(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().UpdateThreadID(uid, 555); err != nil {
		t.Fatal(err)
	}

	capt := &captureTG{}
	const chatID = -100

	// Wire the wake notifier exactly as cmd/backend/main.go would.
	wake := alerts.NewWakeNotifier(d, capt, chatID)

	// Simulate the handler's hook: Resumed=true on a mobile user → SendWake.
	checks := []wire.Check{{Name: "tunnels", Status: "ok"}, {Name: "dns_via_tunnel", Status: "ok"}}
	if err := wake.SendWake(context.Background(), uid, "client-h", checks); err != nil {
		t.Fatal(err)
	}

	// Wire watcher + sleep notifier; trigger a stale scan.
	sleep := alerts.NewSleepNotifier(d, capt, chatID)
	w := heartbeat.NewWatcher(d, &noopOffline{}, heartbeat.Config{
		MobileLifecycle:  true,
		MobileSleepAfter: time.Second,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	// Last heartbeat 2s ago — should trip MobileSleepAfter (1s).
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-2*time.Second))
	w.SetNow(func() time.Time { return now })
	w.ScanForTest(context.Background())

	sends := capt.snapshot()
	if len(sends) != 2 {
		t.Fatalf("want 2 sends (wake + sleep), got %d", len(sends))
	}
	if !strings.Contains(sends[0].text, "🚗") || !strings.Contains(sends[0].text, "всё ок") {
		t.Errorf("first send must be wake-card all-ok, got %q", sends[0].text)
	}
	if !strings.Contains(sends[1].text, "🌙") || !strings.Contains(sends[1].text, "client-h") {
		t.Errorf("second send must be sleep-info, got %q", sends[1].text)
	}
	for _, s := range sends {
		if s.threadID == nil || *s.threadID != 555 {
			t.Errorf("send threadID: want 555, got %v", s.threadID)
		}
		if s.chatID != chatID {
			t.Errorf("send chatID: want %d, got %d", chatID, s.chatID)
		}
	}
}

type noopOffline struct{}

func (noopOffline) SendOffline(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}
