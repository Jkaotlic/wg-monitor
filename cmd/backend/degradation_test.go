package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

type degradationRecorder struct {
	chats []int64
	texts []string
	modes []string
	err   error
}

func (r *degradationRecorder) SendMessage(_ context.Context, chatID int64, _ *int64, text, parseMode string, _ *int64) (int64, error) {
	r.chats = append(r.chats, chatID)
	r.texts = append(r.texts, text)
	r.modes = append(r.modes, parseMode)
	return 1, r.err
}

// Группы больше нет: об умершей горутине узнаёт админ в личке, и текст идёт
// без разметки -- ошибка Go со скобками и дефисами в MarkdownV2 порвала бы
// отправку, и сообщение о поломке потерялось бы из-за собственной разметки.
func TestNotifyDegradation_GoesToAdminDMAsPlainText(t *testing.T) {
	r := &degradationRecorder{}
	if err := notifyDegradation(r, 42, "callbacks router", errors.New("getUpdates failed (409): conflict-run")); err != nil {
		t.Fatal(err)
	}
	if len(r.chats) != 1 || r.chats[0] != 42 {
		t.Fatalf("адресаты %v, ждали личку админа 42", r.chats)
	}
	if r.modes[0] != "" {
		t.Fatalf("parse mode %q, ждали без разметки", r.modes[0])
	}
	text := r.texts[0]
	for _, want := range []string{"callbacks router", "conflict-run", "перезапуск"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Fatalf("текст %q без %q", text, want)
		}
	}
	if strings.ContainsAny(text, "*`_") {
		t.Fatalf("в тексте разметка: %q", text)
	}
}

// Админ не задан или ошибки нет -- писать некому и не о чем.
func TestNotifyDegradation_NoAdminNoSend(t *testing.T) {
	r := &degradationRecorder{}
	if err := notifyDegradation(r, 0, "realert poller", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if err := notifyDegradation(r, 42, "realert poller", nil); err != nil {
		t.Fatal(err)
	}
	if len(r.chats) != 0 {
		t.Fatalf("лишние отправки: %v", r.chats)
	}
}

// Отправка не удалась -- ошибка возвращается вызывающему для журнала.
func TestNotifyDegradation_SendErrorIsReturned(t *testing.T) {
	r := &degradationRecorder{err: &tg.APIError{Method: "sendMessage", Code: 403, Description: "Forbidden"}}
	if err := notifyDegradation(r, 42, "watcher", errors.New("boom")); err == nil {
		t.Fatal("ошибка отправки потерялась")
	}
}
