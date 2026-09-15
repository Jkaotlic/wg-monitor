package callbacks

import (
	"context"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// chatsRouterTG -- фейк бота, который помнит адресатов личных сообщений.
type chatsRouterTG struct {
	fakeRouterTGFull
	cmu   sync.Mutex
	chats []int64
}

func (f *chatsRouterTG) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	f.cmu.Lock()
	f.chats = append(f.chats, chatID)
	f.cmu.Unlock()
	return f.fakeRouterTGFull.SendMessage(ctx, chatID, threadID, text, parseMode, replyTo)
}

func (f *chatsRouterTG) SendMessageWithKeyboard(_ context.Context, chatID int64, _ *int64, _, _ string, _ *int64, _ *tg.InlineKeyboardMarkup) (int64, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	f.chats = append(f.chats, chatID)
	return 1, nil
}

// Отчёты починки линии и замены конфига идут через NotifyRouterTopic -- и они
// тоже «всё подряд» для админа.
func TestNotifyRouterTopicReachesAdmin(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().SetTelegramUserID(uid, 100); err != nil {
		t.Fatal(err)
	}
	f := &chatsRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})

	if err := r.NotifyRouterTopic(context.Background(), uid, "Линия починена"); err != nil {
		t.Fatal(err)
	}
	seen := map[int64]int{}
	for _, c := range f.chats {
		seen[c]++
	}
	if seen[100] != 1 || seen[42] != 1 || len(f.chats) != 2 {
		t.Fatalf("адресаты %v, ждали владельца 100 и админа 42 по одному разу", f.chats)
	}
}
