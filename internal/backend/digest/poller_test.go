package digest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type fakeTG struct {
	mu    sync.Mutex
	sent  []string
	chats []int64
}

func (f *fakeTG) SendMessage(_ context.Context, chatID int64, _ *int64, text, _ string, _ *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	f.chats = append(f.chats, chatID)
	return 1, nil
}

func (f *fakeTG) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/d.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// fixedNow is 09:00 MSK (06:00 UTC) on 2026-06-18.
var fixedNow = time.Date(2026, 6, 18, 6, 0, 0, 0, time.UTC)

func TestDigest_SendsOncePerDayWithOnlineCount(t *testing.T) {
	d := openDB(t)
	v, err := d.Users().Insert("vasya", "rawtoken-vasya", "1.1.1.1", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("petya", "rawtoken-petya", "2.2.2.2", "nwg1"); err != nil {
		t.Fatal(err)
	}
	// vasya reported 1 min ago → online; petya never reported → offline.
	if err := d.Events().Insert(v, "dns", "ok", "{}", fixedNow.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, HourMSK: 9, OnlineWindow: 10 * time.Minute})
	p.SetNow(func() time.Time { return fixedNow })

	p.TickForTest(context.Background())
	if f.count() != 1 {
		t.Fatalf("expected exactly 1 digest at the configured hour, got %d", f.count())
	}
	if f.chats[0] != -100 {
		t.Fatalf("digest должен идти в primary chat, got chat %d", f.chats[0])
	}
	body := f.sent[0]
	for _, want := range []string{"Монитор жив", "1 из 2", "petya"} {
		if !strings.Contains(body, want) {
			t.Fatalf("digest body missing %q:\n%s", want, body)
		}
	}

	// Second tick same day → deduped, no extra send.
	p.TickForTest(context.Background())
	if f.count() != 1 {
		t.Fatalf("digest must be once-per-day; got %d sends", f.count())
	}
}

func TestDigest_SilentOutsideConfiguredHour(t *testing.T) {
	d := openDB(t)
	if _, err := d.Users().Insert("vasya", "rawtoken-vasya", "1.1.1.1", "nwg0"); err != nil {
		t.Fatal(err)
	}
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, HourMSK: 9, OnlineWindow: 10 * time.Minute})
	// 10:00 MSK ≠ configured 09:00 → no send.
	p.SetNow(func() time.Time { return fixedNow.Add(time.Hour) })

	p.TickForTest(context.Background())
	if f.count() != 0 {
		t.Fatalf("digest must stay silent outside the configured hour, got %d sends", f.count())
	}
}
