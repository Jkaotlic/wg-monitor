package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeTG -- поддельный Telegram Bot API: край системы. Тревога настоящая до
// HTTP-запроса; здесь она только записывается.
type fakeTG struct {
	srv *httptest.Server

	mu       sync.Mutex
	nextID   int64
	messages []*tgMessage
	log      []string
}

// tgMessage -- сообщение глазами человека: правки меняют текст на месте.
type tgMessage struct {
	Chat int64
	ID   int64
	Text string
}

func newFakeTG(t *testing.T) *fakeTG {
	t.Helper()
	f := &fakeTG{nextID: 1000}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTG) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ChatID    int64  `json:"chat_id"`
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
	}
	_ = json.Unmarshal(body, &req)
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "sendMessage":
		f.nextID++
		f.messages = append(f.messages, &tgMessage{Chat: req.ChatID, ID: f.nextID, Text: req.Text})
		f.log = append(f.log, req.Text)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": f.nextID}})
	case "editMessageText":
		for _, m := range f.messages {
			if m.Chat == req.ChatID && m.ID == req.MessageID {
				m.Text = req.Text
			}
		}
		f.log = append(f.log, req.Text)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}
}

// Texts -- текущие тексты сообщений по порядку отправки: что видит человек.
func (f *fakeTG) Texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.messages))
	for _, m := range f.messages {
		out = append(out, m.Text)
	}
	return out
}

// Log -- все отправки и правки по порядку.
func (f *fakeTG) Log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}
