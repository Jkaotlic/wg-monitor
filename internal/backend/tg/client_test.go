// internal/backend/tg/client_test.go
package tg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendMessageInThread(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	mid, err := c.SendMessage(context.Background(), -100123, intPtr(7), "hi", "MarkdownV2", intPtr(99))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if mid != 42 {
		t.Fatalf("msg id: %d", mid)
	}
	if got["chat_id"].(float64) != -100123 {
		t.Fatalf("chat: %v", got["chat_id"])
	}
	if got["message_thread_id"].(float64) != 7 {
		t.Fatalf("thread: %v", got["message_thread_id"])
	}
	if got["reply_to_message_id"].(float64) != 99 {
		t.Fatalf("reply: %v", got["reply_to_message_id"])
	}
}

func TestCreateForumTopic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/createForumTopic") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true,"result":{"message_thread_id":555,"name":"vasya"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	tid, err := c.CreateForumTopic(context.Background(), -100123, "👤 vasya", 0xFF8C00)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tid != 555 {
		t.Fatalf("tid: %d", tid)
	}
}

func TestApiErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"ok":false,"error_code":403,"description":"bot was kicked"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	_, err := c.SendMessage(context.Background(), -100, nil, "x", "", nil)
	if err == nil || !strings.Contains(err.Error(), "bot was kicked") {
		t.Fatalf("err: %v", err)
	}
}

func intPtr(i int64) *int64 { return &i }

func TestSendMessageWithKeyboard(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 777}})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	kb := HardAlertKeyboard(1, "x")
	mid, err := c.SendMessageWithKeyboard(context.Background(), 100, nil, "hi", "", nil, &kb)
	if err != nil { t.Fatal(err) }
	if mid != 777 { t.Errorf("got mid=%d, want 777", mid) }
	if captured["reply_markup"] == nil {
		t.Errorf("expected reply_markup in body, got %+v", captured)
	}
}

func TestAnswerCallbackQuery(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			t.Errorf("expected answerCallbackQuery, got %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	if err := c.AnswerCallbackQuery(context.Background(), "cbk-1", "Silenced"); err != nil {
		t.Fatal(err)
	}
	if captured["callback_query_id"] != "cbk-1" {
		t.Errorf("expected callback_query_id=cbk-1, got %v", captured["callback_query_id"])
	}
	if captured["text"] != "Silenced" {
		t.Errorf("expected text=Silenced, got %v", captured["text"])
	}
}

func TestEditMessageTextWithEmptyMarkupRemovesKeyboard(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 5}})
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	empty := InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{}}
	err := c.EditMessageText(context.Background(), 100, 5, "new text", "", &empty)
	if err != nil { t.Fatal(err) }
	rm, ok := captured["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("expected reply_markup map, got %T", captured["reply_markup"])
	}
	arr, _ := rm["inline_keyboard"].([]any)
	if len(arr) != 0 {
		t.Errorf("expected empty inline_keyboard, got %v", arr)
	}
}

func TestEditMessageTextWithNilMarkupOmitsField(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 5}})
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	err := c.EditMessageText(context.Background(), 100, 5, "new", "", nil)
	if err != nil { t.Fatal(err) }
	if _, present := captured["reply_markup"]; present {
		t.Errorf("nil markup should omit reply_markup field, got %v", captured)
	}
}

func TestSendMessageWithReplyKeyboard_AcceptsReplyMarkup(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}

	rk := &ReplyKeyboardMarkup{
		Keyboard:       [][]ReplyKeyboardButton{{{Text: "📊 Что происходит?"}}},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
	mid, err := c.SendMessageWithReplyKeyboard(context.Background(), -100, intPtr(11), "hi", "", nil, rk)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if mid != 7 {
		t.Fatalf("mid=%d", mid)
	}
	rm, ok := got["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup missing or wrong type: %T", got["reply_markup"])
	}
	if rm["is_persistent"] != true {
		t.Errorf("is_persistent missing: %+v", rm)
	}
}

func TestSendMessageWithReplyKeyboard_AcceptsInlineMarkup(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Write([]byte(`{"ok":true,"result":{"message_id":8}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	ik := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "X", CallbackData: "y"}}}}
	_, err := c.SendMessageWithReplyKeyboard(context.Background(), -100, nil, "hi", "", nil, ik)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	rm, ok := got["reply_markup"].(map[string]any)
	if !ok || rm["inline_keyboard"] == nil {
		t.Errorf("inline_keyboard not propagated: %+v", got)
	}
}

func TestDeleteMessage(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	if err := c.DeleteMessage(context.Background(), -100, 42); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got["chat_id"].(float64) != -100 || got["message_id"].(float64) != 42 {
		t.Errorf("payload: %+v", got)
	}
}
