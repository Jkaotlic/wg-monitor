package alerts

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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

func TestWakeNotifier_SendsToOwnerDM(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1100110011001100110011001100110011001100110011001100110011001100"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().SetTelegramUserID(uid, 7001); err != nil {
		t.Fatal(err)
	}

	tg := &fakeSendTG{}
	wn := NewWakeNotifier(d, tg, -100)
	checks := []wire.Check{{Name: "tunnels", Status: "ok"}}
	if err := wn.SendWake(context.Background(), uid, "client-h", checks); err != nil {
		t.Fatal(err)
	}
	if tg.chatID != 7001 {
		t.Errorf("адресат: ждали личку владельца 7001, получили %d", tg.chatID)
	}
	if tg.threadID != nil {
		t.Errorf("в личку пишут без темы, получили %v", *tg.threadID)
	}
	if !strings.Contains(tg.text, "🚗") || !strings.Contains(tg.text, "client-h") {
		t.Errorf("text missing wake markers: %q", tg.text)
	}
}

func TestWakeNotifier_SkipsMutedRecipient(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	uid, _ := d.Users().InsertWithKind("tenantcar", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().SetTelegramUserID(uid, 7002); err != nil {
		t.Fatal(err)
	}
	// Владелец выключил уведомления по этому роутеру -- значит и отчёт о
	// пробуждении ему не приходит.
	if err := d.NotifyMutes().SetMuted(7002, uid, true); err != nil {
		t.Fatal(err)
	}

	tg := &fakeSendTG{}
	wn := NewWakeNotifier(d, tg, -100)
	if err := wn.SendWake(context.Background(), uid, "tenantcar", []wire.Check{{Name: "tunnels", Status: "ok"}}); err != nil {
		t.Fatal(err)
	}
	if tg.chatID != 0 {
		t.Errorf("заглушившему ушло сообщение в чат %d", tg.chatID)
	}
}

func TestSleepNotifier_SendsToOwnerDM(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "2200220022002200220022002200220022002200220022002200220022002200"
	uid, _ := d.Users().InsertWithKind("sleeper", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().SetTelegramUserID(uid, 7003); err != nil {
		t.Fatal(err)
	}

	tg := &fakeSendTG{}
	sn := NewSleepNotifier(d, tg, -100)
	if err := sn.SendSleeping(context.Background(), uid, "sleeper", time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if tg.chatID != 7003 {
		t.Errorf("адресат: ждали личку владельца 7003, получили %d", tg.chatID)
	}
	if tg.threadID != nil {
		t.Errorf("в личку пишут без темы, получили %v", *tg.threadID)
	}
	if !strings.Contains(tg.text, "sleeper") {
		t.Errorf("в тексте нет имени роутера: %q", tg.text)
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

// Кнопки под отчётом о пробуждении владелец видит в личке. «🛣 Маршруты»
// открывала панель бота, «HR-Neo проверка» -- инженерный осмотр, «Диагностика»
// и «Повторить проверку» вели в панель бота командами. Теперь под отчётом
// ровно одна кнопка -- та же, что под тревогами: приложение. Без настроенной
// базы кнопок нет вовсе (nil), а не пустая клавиатура.
func TestMobileWakeKeyboard_LeadsOwnerToApp(t *testing.T) {
	kbWithBase := mobileWakeKeyboard(42, "https://example.com/")
	b, err := json.Marshal(kbWithBase)
	if err != nil {
		t.Fatal(err)
	}
	kb := string(b)
	for _, bad := range []string{"panel:", "routes_hrneo_doctor", "HR-Neo", "diag_now", "force_recheck", "callback_data"} {
		if strings.Contains(kb, bad) {
			t.Errorf("под отчётом осталась старая командная кнопка %q: %s", bad, kb)
		}
	}
	if !strings.Contains(kb, `"url":"https://example.com/miniapp/?router=42"`) {
		t.Errorf("нет кнопки приложения с адресом роутера: %s", kb)
	}
	markup, ok := kbWithBase.(tg.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("expected tg.InlineKeyboardMarkup, got %T", kbWithBase)
	}
	total := 0
	for _, row := range markup.InlineKeyboard {
		total += len(row)
	}
	if total != 1 {
		t.Fatalf("expected exactly 1 button (app), got %d: %+v", total, markup)
	}

	if kbNoBase := mobileWakeKeyboard(42, ""); kbNoBase != nil {
		t.Errorf("без адреса приложения клавиатуры быть не должно, получили %+v", kbNoBase)
	}
}
