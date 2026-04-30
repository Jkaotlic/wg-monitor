package tg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGetUpdatesParsesCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		au, _ := req["allowed_updates"].([]any)
		hasCallback := false
		for _, v := range au {
			if v == "callback_query" {
				hasCallback = true
			}
		}
		if !hasCallback {
			t.Errorf("expected allowed_updates to include callback_query, got %v", au)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": []map[string]any{
				{
					"update_id": 100,
					"callback_query": map[string]any{
						"id":   "cbk-1",
						"from": map[string]any{"id": 12345},
						"message": map[string]any{
							"message_id": 7,
							"chat":       map[string]any{"id": -100},
							"text":       "🔴 [vasya] AWG handshake — DOWN",
						},
						"data": "silence:42:awg_handshake:4h",
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	ups, err := c.GetUpdates(context.Background(), 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 1 {
		t.Fatalf("expected 1 update, got %d", len(ups))
	}
	u := ups[0]
	if u.UpdateID != 100 {
		t.Errorf("update_id: %d", u.UpdateID)
	}
	if u.CallbackQuery == nil {
		t.Fatal("CallbackQuery nil")
	}
	if u.CallbackQuery.Data != "silence:42:awg_handshake:4h" {
		t.Errorf("data: %q", u.CallbackQuery.Data)
	}
	if u.CallbackQuery.From.ID != 12345 {
		t.Errorf("from.id: %d", u.CallbackQuery.From.ID)
	}
	if u.CallbackQuery.Message.MessageID != 7 {
		t.Errorf("message_id: %d", u.CallbackQuery.Message.MessageID)
	}
}

func TestGetUpdatesEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	ups, err := c.GetUpdates(context.Background(), 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 0 {
		t.Errorf("expected 0 updates, got %d", len(ups))
	}
}

func TestGetUpdatesPassesOffset(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	_, _ = c.GetUpdates(context.Background(), 555, 30)
	if v, _ := captured["offset"].(float64); v != 555 {
		t.Errorf("expected offset=555, got %v", captured["offset"])
	}
	if v, _ := captured["timeout"].(float64); v != 30 {
		t.Errorf("expected timeout=30, got %v", captured["timeout"])
	}
	_ = url.Parse
	_ = strings.Contains
}

func TestGetUpdatesIncludesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		// Verify the new message type is in the allowed_updates list
		au, _ := req["allowed_updates"].([]any)
		hasMessage := false
		for _, v := range au {
			if v == "message" {
				hasMessage = true
			}
		}
		if !hasMessage {
			t.Errorf("allowed_updates missing 'message': %v", au)
		}
		w.Write([]byte(`{"ok":true,"result":[
			{"update_id":1,"message":{"message_id":42,"chat":{"id":-100},"from":{"id":555},"text":"📊 Что происходит?","message_thread_id":11}}
		]}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client(), LongPollHTTP: srv.Client()}
	ups, err := c.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 1 || ups[0].Message == nil {
		t.Fatalf("expected 1 update with non-nil Message, got %+v", ups)
	}
	m := ups[0].Message
	if m.Text != "📊 Что происходит?" {
		t.Errorf("text: %q", m.Text)
	}
	if m.MessageID != 42 {
		t.Errorf("message_id: %d", m.MessageID)
	}
	if m.MessageThreadID == nil || *m.MessageThreadID != 11 {
		t.Errorf("thread id: %v", m.MessageThreadID)
	}
	if m.From.ID != 555 {
		t.Errorf("from.id: %d", m.From.ID)
	}
	if m.Chat.ID != -100 {
		t.Errorf("chat.id: %d", m.Chat.ID)
	}
}
