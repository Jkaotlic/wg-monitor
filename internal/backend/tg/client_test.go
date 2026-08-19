// internal/backend/tg/client_test.go
package tg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestApiErrorCarriesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 17","parameters":{"retry_after":17}}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	_, err := c.SendMessage(context.Background(), -100, nil, "x", "", nil)
	delay, ok := RateLimitDelay(err)
	if !ok {
		t.Fatalf("expected rate-limit APIError, got %v", err)
	}
	if delay != 17*time.Second {
		t.Fatalf("retry_after delay = %s, want 17s", delay)
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
	if err != nil {
		t.Fatal(err)
	}
	if mid != 777 {
		t.Errorf("got mid=%d, want 777", mid)
	}
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
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if _, present := captured["reply_markup"]; present {
		t.Errorf("nil markup should omit reply_markup field, got %v", captured)
	}
}

func TestEditMessageTextIgnoresMessageNotModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/editMessageText") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	if err := c.EditMessageText(context.Background(), 100, 5, "same", "", nil); err != nil {
		t.Fatalf("message-not-modified edit should be a no-op, got %v", err)
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

func TestSendDocumentUploadsMultipart(t *testing.T) {
	var gotPath string
	var gotFields = map[string]string{}
	var gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				gotFields[k] = v[0]
			}
		}
		files := r.MultipartForm.File["document"]
		if len(files) != 1 {
			t.Fatalf("document files = %d", len(files))
		}
		gotFile = files[0].Filename
		w.Write([]byte(`{"ok":true,"result":{"message_id":9}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	threadID := int64(11)
	mid, err := c.SendDocument(context.Background(), -100, &threadID, "x.conf", []byte("[Interface]\n"), "caption")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if mid != 9 {
		t.Fatalf("mid=%d", mid)
	}
	if gotPath != "/bottok/sendDocument" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotFields["chat_id"] != "-100" || gotFields["message_thread_id"] != "11" || gotFields["caption"] != "caption" {
		t.Fatalf("fields = %+v", gotFields)
	}
	if gotFile != "x.conf" {
		t.Fatalf("file = %q", gotFile)
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

func TestGetFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getFile") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		if req["file_id"] != "file123" {
			t.Errorf("file_id: %v", req["file_id"])
		}
		w.Write([]byte(`{"ok":true,"result":{"file_id":"file123","file_path":"documents/file123.conf"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	fp, err := c.GetFile(context.Background(), "file123")
	if err != nil {
		t.Fatal(err)
	}
	if fp != "documents/file123.conf" {
		t.Errorf("file_path: %q", fp)
	}
}

func TestDownloadFile(t *testing.T) {
	content := []byte("[Interface]\nPrivateKey = abc\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/file/bottok/documents/file123.conf") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Write(content)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	data, err := c.DownloadFile(context.Background(), "documents/file123.conf")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Errorf("data: %q", data)
	}
}

func TestDownloadFileRejectsOversizeResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/file/bottok/documents/file123.conf") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = io.Copy(w, io.LimitReader(zeroReader{}, maxDownloadFileBytes+1))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	_, err := c.DownloadFile(context.Background(), "documents/file123.conf")
	if err == nil {
		t.Fatal("expected oversize error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// stubTransportErrRoundTripper forces http.Client.Do to fail with a
// transport-level error without any real network I/O. Go's http.Client
// wraps whatever the RoundTripper returns in a *url.Error whose Error()
// embeds the full request URL — exactly the shape that leaked the bot-token
// before DownloadFile redacted it.
type stubTransportErrRoundTripper struct{ err error }

func (rt stubTransportErrRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, rt.err
}

// TestDownloadFileRedactsBotTokenOnTransportError forces a *url.Error via a
// stub RoundTripper (simulating a dial/timeout failure while downloading an
// uploaded .conf) and asserts the token never survives into the returned
// error string. This error is relayed verbatim into Telegram chat text by
// callbacks.handleDocumentUpload, so a leak here means the live bot-token
// gets posted into the topic.
func TestDownloadFileRedactsBotTokenOnTransportError(t *testing.T) {
	const token = "123456:SECRETTOKEN"
	c := &Client{
		BaseURL: DefaultBaseURL,
		Token:   token,
		HTTP:    &http.Client{Transport: stubTransportErrRoundTripper{err: errors.New("simulated dial failure")}},
	}
	_, err := c.DownloadFile(context.Background(), "documents/file123.conf")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("DownloadFile error leaks bot token: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("expected redaction marker (***) in error, got %v", err)
	}
}

// TestDownloadFileRedactsBotTokenOnBuildRequestError covers the OTHER
// *url.Error source in DownloadFile: a raw control character in filePath
// makes net/url.Parse (invoked by http.NewRequestWithContext) fail before
// any request is sent, and that error's Error() also embeds the full URL
// (token included) — the "not just one [error path]" case from the audit.
func TestDownloadFileRedactsBotTokenOnBuildRequestError(t *testing.T) {
	const token = "123456:SECRETTOKEN"
	c := &Client{
		BaseURL: DefaultBaseURL,
		Token:   token,
		HTTP:    http.DefaultClient, // unreachable: request build fails first
	}
	_, err := c.DownloadFile(context.Background(), "documents/\n.conf")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("DownloadFile error leaks bot token: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("expected redaction marker (***) in error, got %v", err)
	}
}

func TestSetMyCommands_PostsExpectedPayload(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()

	c := &Client{
		BaseURL: ts.URL + "/bot",
		Token:   "T",
		HTTP:    ts.Client(),
	}
	cmds := []BotCommand{
		{Command: "panel", Description: "Открыть панель управления"},
		{Command: "topic_help", Description: "Шпаргалка"},
	}
	if err := c.SetMyCommands(context.Background(), cmds); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
	var got struct {
		Commands []BotCommand `json:"commands"`
	}
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("unmarshal body: %v\nbody: %s", err, gotBody)
	}
	if len(got.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d: %+v", len(got.Commands), got.Commands)
	}
	if got.Commands[0].Command != "panel" {
		t.Errorf("commands[0].command: got %q, want %q", got.Commands[0].Command, "panel")
	}
	if got.Commands[0].Description != "Открыть панель управления" {
		t.Errorf("commands[0].description: got %q, want %q", got.Commands[0].Description, "Открыть панель управления")
	}
	if got.Commands[1].Command != "topic_help" {
		t.Errorf("commands[1].command: got %q, want %q", got.Commands[1].Command, "topic_help")
	}
	if got.Commands[1].Description != "Шпаргалка" {
		t.Errorf("commands[1].description: got %q, want %q", got.Commands[1].Description, "Шпаргалка")
	}
}

func TestSetMyCommandsWithScope_PostsScopePayload(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()

	c := &Client{
		BaseURL: ts.URL + "/bot",
		Token:   "T",
		HTTP:    ts.Client(),
	}
	cmds := []BotCommand{{Command: "panel", Description: "admin panel"}}
	scope := BotCommandScope{Type: "chat_member", ChatID: -100, UserID: 12345}
	if err := c.SetMyCommandsWithScope(context.Background(), cmds, scope); err != nil {
		t.Fatalf("SetMyCommandsWithScope: %v", err)
	}
	var got struct {
		Commands []BotCommand    `json:"commands"`
		Scope    BotCommandScope `json:"scope"`
	}
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("unmarshal body: %v\nbody: %s", err, gotBody)
	}
	if len(got.Commands) != 1 || got.Commands[0].Command != "panel" {
		t.Fatalf("commands = %+v", got.Commands)
	}
	if got.Scope.Type != "chat_member" || got.Scope.ChatID != -100 || got.Scope.UserID != 12345 {
		t.Fatalf("scope = %+v", got.Scope)
	}
}

func TestSetCommandsMenuButton_PostsCommandsButton(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setChatMenuButton") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()

	c := &Client{BaseURL: ts.URL + "/bot", Token: "T", HTTP: ts.Client()}
	if err := c.SetCommandsMenuButton(context.Background()); err != nil {
		t.Fatalf("SetCommandsMenuButton: %v", err)
	}
	var got struct {
		MenuButton struct {
			Type string `json:"type"`
		} `json:"menu_button"`
	}
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("unmarshal body: %v\nbody: %s", err, gotBody)
	}
	if got.MenuButton.Type != "commands" {
		t.Fatalf("menu_button.type = %q, want commands", got.MenuButton.Type)
	}
}

func TestGetUpdates_ParseDocument(t *testing.T) {
	resp := `{"ok":true,"result":[{"update_id":1,"message":{"message_id":10,"from":{"id":99},"chat":{"id":-100},"message_thread_id":5,"document":{"file_id":"fid1","file_name":"awg11.conf","file_size":512}}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(resp))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client(), LongPollHTTP: srv.Client()}
	updates, err := c.GetUpdates(context.Background(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("len: %d", len(updates))
	}
	doc := updates[0].Message.Document
	if doc == nil {
		t.Fatal("Document is nil")
	}
	if doc.FileID != "fid1" || doc.FileName != "awg11.conf" {
		t.Errorf("doc: %+v", doc)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestEditMessageReplyMarkup(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 555}})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	err := c.EditMessageReplyMarkup(context.Background(), 100, 555, &InlineKeyboardMarkup{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := captured["reply_markup"]; !ok {
		t.Errorf("expected reply_markup in body, got %+v", captured)
	}
	if captured["text"] != nil {
		t.Errorf("editMessageReplyMarkup must not send a text field, got %+v", captured["text"])
	}
	if fmt.Sprintf("%v", captured["message_id"]) != "555" {
		t.Errorf("message_id = %v, want 555", captured["message_id"])
	}
}

// Мини-апп стал основной поверхностью, и кнопка меню в приватном чате должна
// открывать его, а не список слэш-команд. Проверяем ровно форму запроса:
// TG принимает web_app-кнопку только с непустым text и вложенным web_app.url.
func TestSetWebAppMenuButton_PostsWebAppButton(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setChatMenuButton") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()

	c := &Client{BaseURL: ts.URL + "/bot", Token: "T", HTTP: ts.Client()}
	if err := c.SetWebAppMenuButton(context.Background(), "Открыть приложение", "https://wg.example.test/miniapp/"); err != nil {
		t.Fatalf("SetWebAppMenuButton: %v", err)
	}
	var got struct {
		MenuButton struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			WebApp struct {
				URL string `json:"url"`
			} `json:"web_app"`
		} `json:"menu_button"`
	}
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("unmarshal body: %v\nbody: %s", err, gotBody)
	}
	if got.MenuButton.Type != "web_app" {
		t.Fatalf("menu_button.type = %q, want web_app", got.MenuButton.Type)
	}
	if got.MenuButton.Text != "Открыть приложение" {
		t.Fatalf("menu_button.text = %q", got.MenuButton.Text)
	}
	if got.MenuButton.WebApp.URL != "https://wg.example.test/miniapp/" {
		t.Fatalf("menu_button.web_app.url = %q", got.MenuButton.WebApp.URL)
	}
}

// Кнопка команд не должна утащить с собой пустые text/web_app: TG на
// type=commands с лишними полями отвечает ошибкой, а не игнорирует их.
func TestSetCommandsMenuButton_OmitsWebAppFields(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()

	c := &Client{BaseURL: ts.URL + "/bot", Token: "T", HTTP: ts.Client()}
	if err := c.SetCommandsMenuButton(context.Background()); err != nil {
		t.Fatalf("SetCommandsMenuButton: %v", err)
	}
	if strings.Contains(string(gotBody), "web_app") || strings.Contains(string(gotBody), `"text"`) {
		t.Fatalf("commands button carries web_app/text: %s", gotBody)
	}
}
