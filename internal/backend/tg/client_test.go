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
