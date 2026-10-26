package tg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// FRESH-01: ответ на тревогу, которую владелец удалил у себя, Telegram
// отклоняет 400 «message to be replied not found», если в запросе нет
// allow_sending_without_reply. Каждый путь отправки с привязкой обязан нести
// флаг; без привязки -- поля нет, запрос прежний.
func TestReplySendsAllowWithoutReply(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = nil
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "x", CallbackData: "y"}}}}

	senders := map[string]func(replyTo *int64) error{
		"SendMessage": func(r *int64) error {
			_, err := c.SendMessage(context.Background(), 5, nil, "t", "", r)
			return err
		},
		"SendMessageWithKeyboard": func(r *int64) error {
			_, err := c.SendMessageWithKeyboard(context.Background(), 5, nil, "t", "", r, kb)
			return err
		},
		"SendSilentMessageWithKeyboard": func(r *int64) error {
			_, err := c.SendSilentMessageWithKeyboard(context.Background(), 5, nil, "t", "", r, kb)
			return err
		},
		"SendMessageWithReplyKeyboard": func(r *int64) error {
			_, err := c.SendMessageWithReplyKeyboard(context.Background(), 5, nil, "t", "", r, kb)
			return err
		},
	}
	for name, send := range senders {
		if err := send(intPtr(77)); err != nil {
			t.Fatalf("%s reply: %v", name, err)
		}
		if got["allow_sending_without_reply"] != true {
			t.Errorf("%s с replyTo: allow_sending_without_reply = %v, want true (тело %v)", name, got["allow_sending_without_reply"], got)
		}
		if err := send(nil); err != nil {
			t.Fatalf("%s top-level: %v", name, err)
		}
		if _, ok := got["allow_sending_without_reply"]; ok {
			t.Errorf("%s без replyTo: поле allow_sending_without_reply не должно уходить (тело %v)", name, got)
		}
	}
}
