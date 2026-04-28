package callbacks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

type fakeRouterTG struct {
	mu        sync.Mutex
	answers   []string
	edits     []string
	sentMsgs  []string
	sendErr   error
	answerErr error
	editErr   error
}

func (f *fakeRouterTG) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	f.sentMsgs = append(f.sentMsgs, text)
	return 1, f.sendErr
}
func (f *fakeRouterTG) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	f.mu.Lock(); defer f.mu.Unlock()
	f.answers = append(f.answers, text)
	return f.answerErr
}
func (f *fakeRouterTG) EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error {
	f.mu.Lock(); defer f.mu.Unlock()
	f.edits = append(f.edits, text)
	return f.editErr
}
func (f *fakeRouterTG) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tg.Update, error) {
	return nil, nil
}

func TestRouterDispatchesSilence(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-1",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "🔴 alert text"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 { t.Errorf("expected 1 answer, got %d", len(f.answers)) }
	if len(f.edits) != 1 {
		t.Errorf("expected 1 edit, got %d", len(f.edits))
	} else if !strings.Contains(f.edits[0], "Silenced") {
		t.Errorf("edit text missing 'Silenced': %q", f.edits[0])
	}
}

func TestRouterRejectsNonAdmin(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-2",
		From:    tg.User{ID: 99999},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 { t.Errorf("expected 1 answer (rejection), got %d", len(f.answers)) }
	if !strings.Contains(f.answers[0], "not authorized") {
		t.Errorf("expected 'not authorized', got %q", f.answers[0])
	}
	if len(f.edits) != 0 {
		t.Errorf("expected NO edits for non-admin, got %d", len(f.edits))
	}
}

func TestRouterUnknownAction(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:   "cbk-3",
		From: tg.User{ID: 12345},
		Data: "frobnicate:1:x",
	}
	r.HandleCallback(context.Background(), q)
	if len(f.answers) != 1 { t.Fatal("expected answerCallback") }
	if !strings.Contains(strings.ToLower(f.answers[0]), "unknown") {
		t.Errorf("expected 'unknown' in answer, got %q", f.answers[0])
	}
}

func TestRouterHistorySkipsEdit(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-h",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "alert"},
		Data:    "history:" + itoa(uid) + ":awg_handshake",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.sentMsgs) != 1 {
		t.Errorf("expected 1 history message sent, got %d", len(f.sentMsgs))
	}
	if len(f.edits) != 0 {
		t.Errorf("history should not edit original, got %d edits", len(f.edits))
	}
}

func TestRouterActionErrorReportedAsToast(t *testing.T) {
	d, uid := newTestDB(t)
	d.Close()
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
	q := &tg.CallbackQuery{
		ID:      "cbk-err",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "ack:" + itoa(uid) + ":awg_handshake",
	}
	r.HandleCallback(context.Background(), q)
	if len(f.answers) != 1 { t.Fatal("expected answer") }
	if !strings.Contains(f.answers[0], "error") {
		t.Errorf("expected 'error' in answer, got %q", f.answers[0])
	}
	if len(f.edits) != 0 {
		t.Error("on error, should NOT edit")
	}
	_ = errors.New
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }
