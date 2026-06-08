package alerts

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeSendTG struct {
	mu       sync.Mutex
	chatID   int64
	threadID *int64
	text     string
}

func (f *fakeSendTG) SendMessage(_ context.Context, chatID int64, threadID *int64, text, _ string, _ *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatID = chatID
	f.threadID = threadID
	f.text = text
	return 100, nil
}

func TestWakeNotifier_SendsToRouterTopic(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1100110011001100110011001100110011001100110011001100110011001100"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().UpdateThreadID(uid, 555); err != nil {
		t.Fatal(err)
	}

	tg := &fakeSendTG{}
	wn := NewWakeNotifier(d, tg, -100)
	checks := []wire.Check{{Name: "tunnels", Status: "ok"}}
	if err := wn.SendWake(context.Background(), uid, "carvan", checks); err != nil {
		t.Fatal(err)
	}
	if tg.chatID != -100 {
		t.Errorf("chatID: want -100, got %d", tg.chatID)
	}
	if tg.threadID == nil || *tg.threadID != 555 {
		t.Errorf("threadID: want 555, got %v", tg.threadID)
	}
	if !strings.Contains(tg.text, "🚗") || !strings.Contains(tg.text, "carvan") {
		t.Errorf("text missing wake markers: %q", tg.text)
	}
}

func TestWakeNotifier_UsesRouterTelegramChatID(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	uid, _ := d.Users().InsertWithKind("tenantcar", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().UpdateTelegramTopic(uid, -200, 555); err != nil {
		t.Fatal(err)
	}

	tg := &fakeSendTG{}
	wn := NewWakeNotifier(d, tg, -100)
	if err := wn.SendWake(context.Background(), uid, "tenantcar", []wire.Check{{Name: "tunnels", Status: "ok"}}); err != nil {
		t.Fatal(err)
	}
	if tg.chatID != -200 {
		t.Errorf("chatID: want -200, got %d", tg.chatID)
	}
	if tg.threadID == nil || *tg.threadID != 555 {
		t.Errorf("threadID: want 555, got %v", tg.threadID)
	}
}

func TestSleepNotifier_SendsToRouterTopic(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "2200220022002200220022002200220022002200220022002200220022002200"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	d.Users().UpdateThreadID(uid, 777)

	tg := &fakeSendTG{}
	sn := NewSleepNotifier(d, tg, -200)
	when := time.Date(2026, 5, 15, 14, 32, 0, 0, time.Local)
	if err := sn.SendSleeping(context.Background(), uid, "carvan", when); err != nil {
		t.Fatal(err)
	}
	if tg.chatID != -200 {
		t.Errorf("chatID: want -200, got %d", tg.chatID)
	}
	if tg.threadID == nil || *tg.threadID != 777 {
		t.Errorf("threadID: want 777, got %v", tg.threadID)
	}
	if !strings.Contains(tg.text, "🌙") || !strings.Contains(tg.text, "carvan") {
		t.Errorf("text missing sleep markers: %q", tg.text)
	}
}

func TestWakeNotifier_NoThreadID_SkipsSend(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "3300330033003300330033003300330033003300330033003300330033003300"
	uid, _ := d.Users().InsertWithKind("orphan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	// no UpdateThreadID — TelegramThreadID stays NULL

	tg := &fakeSendTG{}
	wn := NewWakeNotifier(d, tg, -100)
	if err := wn.SendWake(context.Background(), uid, "orphan", nil); err != nil {
		t.Fatal(err)
	}
	if tg.text != "" {
		t.Errorf("send must be skipped when topic missing; sent %q", tg.text)
	}
}
