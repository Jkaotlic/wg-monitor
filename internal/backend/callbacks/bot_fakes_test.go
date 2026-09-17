package callbacks

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// fakeRouterTG -- бот без сети: запоминает ответы на нажатия, правки,
// отправки и удаления.
type fakeRouterTG struct {
	mu        sync.Mutex
	answers   []string
	edits     []string
	sentMsgs  []string
	sentModes []string
	rkSends   []rkSend
	deleted   []deleteCall
	deleteErr error
}

type rkSend struct {
	chatID int64
	text   string
	mode   string
	markup any
}

type deleteCall struct{ chatID, msgID int64 }

// fakeRouterTGFull -- прежнее имя фейка в тестах кабинетов и уведомлений.
type fakeRouterTGFull = fakeRouterTG

func (f *fakeRouterTG) SendMessage(_ context.Context, _ int64, _ *int64, text, parseMode string, _ *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentMsgs = append(f.sentMsgs, text)
	f.sentModes = append(f.sentModes, parseMode)
	return 1, nil
}

func (f *fakeRouterTG) SendMessageWithReplyKeyboard(_ context.Context, chatID int64, _ *int64, text, parseMode string, _ *int64, markup any) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rkSends = append(f.rkSends, rkSend{chatID: chatID, text: text, mode: parseMode, markup: markup})
	return 100, nil
}

func (f *fakeRouterTG) DeleteMessage(_ context.Context, chatID, msgID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, deleteCall{chatID, msgID})
	return f.deleteErr
}

func (f *fakeRouterTG) AnswerCallbackQuery(_ context.Context, _, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, text)
	return nil
}

func (f *fakeRouterTG) EditMessageText(_ context.Context, _, _ int64, text, _ string, _ *tg.InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, text)
	return nil
}

func (f *fakeRouterTG) GetUpdates(context.Context, int64, int) ([]tg.Update, error) { return nil, nil }

// silent -- бот не сделал ничего видимого.
func (f *fakeRouterTG) silent() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.answers)+len(f.edits)+len(f.sentMsgs)+len(f.rkSends)+len(f.deleted) == 0
}

func newTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	uid, err := d.Users().Insert("vasya", "rawtoken", "198.51.100.1", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	return d, uid
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }
