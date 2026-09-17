package callbacks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type fakeRouterTG struct {
	mu          sync.Mutex
	answers     []string
	edits       []string
	editMarkups []*tg.InlineKeyboardMarkup
	sentMsgs    []string
	sentMarkups []any
	topicCalls  []fakeTopicCallRouter
	nextTopicID int64
	topicErr    error
	sendErr     error
	answerErr   error
	editErr     error
	filePath    string
	fileData    []byte
}

type fakeTopicCallRouter struct {
	ChatID int64
	Name   string
}

func (f *fakeRouterTG) CreateForumTopic(ctx context.Context, chatID int64, name string, _ int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.topicCalls = append(f.topicCalls, fakeTopicCallRouter{ChatID: chatID, Name: name})
	if f.topicErr != nil {
		return 0, f.topicErr
	}
	if f.nextTopicID == 0 {
		f.nextTopicID = 6000
	}
	f.nextTopicID++
	return f.nextTopicID, nil
}

func (f *fakeRouterTG) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentMsgs = append(f.sentMsgs, text)
	return 1, f.sendErr
}
func (f *fakeRouterTG) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, text)
	return f.answerErr
}
func (f *fakeRouterTG) EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, text)
	f.editMarkups = append(f.editMarkups, markup)
	return f.editErr
}
func (f *fakeRouterTG) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tg.Update, error) {
	return nil, nil
}
func (f *fakeRouterTG) SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentMsgs = append(f.sentMsgs, text)
	f.sentMarkups = append(f.sentMarkups, markup)
	return 1, nil
}
func (f *fakeRouterTG) DeleteMessage(ctx context.Context, chatID, messageID int64) error { return nil }
func (f *fakeRouterTG) GetFile(_ context.Context, _ string) (string, error)              { return f.filePath, nil }
func (f *fakeRouterTG) DownloadFile(_ context.Context, _ string) ([]byte, error) {
	return f.fileData, nil
}

func ptrInt64(v int64) *int64 { return &v }

func TestHandleDocumentUploadRejectsOversizeDownloadedBody(t *testing.T) {
	f := &fakeRouterTG{
		filePath: "documents/huge.conf",
		fileData: []byte(strings.Repeat("A", (50*1024)+1)),
	}
	r := &Router{
		tg:      f,
		pending: make(map[int64]*pendingUpload),
	}
	threadID := int64(11)
	user := &db.User{ID: 7, Nickname: "testkeen"}
	msg := &tg.Message{
		Chat:            tg.Chat{ID: -100},
		MessageThreadID: &threadID,
		Document: &tg.Document{
			FileID:   "file-1",
			FileName: "huge.conf",
			FileSize: 1,
		},
	}

	r.handleDocumentUpload(context.Background(), msg, "per_router", user)

	if len(r.pending) != 0 {
		t.Fatalf("oversize downloaded body stored as pending upload: %+v", r.pending)
	}
	if len(f.sentMsgs) == 0 || !strings.Contains(f.sentMsgs[len(f.sentMsgs)-1], "50") || !strings.Contains(f.sentMsgs[len(f.sentMsgs)-1], ".conf") {
		t.Fatalf("expected oversize warning, got messages=%q", f.sentMsgs)
	}
}

// routeAwareRoundTripper stands in for the whole TG Bot API + file-CDN over
// a single http.Client without any real network I/O: getFile succeeds, the
// file download fails at the transport level (simulating a transient
// network blip while downloading an uploaded .conf — the exact scenario
// that used to leak the bot-token, see client.go DownloadFile), and
// sendMessage succeeds while recording the outgoing request body so the
// test can inspect exactly what text would have reached the chat.
type routeAwareRoundTripper struct {
	mu           sync.Mutex
	sendMessages [][]byte
}

func (rt *routeAwareRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	switch {
	case strings.HasSuffix(path, "/getFile"):
		return stubJSONResponse(`{"ok":true,"result":{"file_path":"documents/x.conf"}}`), nil
	case strings.Contains(path, "/file/bot"):
		return nil, errors.New("simulated dial failure")
	case strings.HasSuffix(path, "/sendMessage"):
		body, _ := io.ReadAll(req.Body)
		rt.mu.Lock()
		rt.sendMessages = append(rt.sendMessages, body)
		rt.mu.Unlock()
		return stubJSONResponse(`{"ok":true,"result":{"message_id":1}}`), nil
	default:
		return nil, fmt.Errorf("routeAwareRoundTripper: unexpected request path %s", path)
	}
}

func stubJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// TestHandleDocumentUploadDownloadFailureDoesNotLeakBotTokenToChat is the
// end-to-end regression for the DownloadFile bot-token redaction fix: it
// wires the REAL tg.Client (not a fake) into the router so the request URLs
// are built exactly as production does, forces a transport failure on the
// file-CDN download, and asserts the resulting "не удалось скачать файл: "
// chat message — captured from the literal outgoing sendMessage HTTP body —
// never contains the bot token.
func TestHandleDocumentUploadDownloadFailureDoesNotLeakBotTokenToChat(t *testing.T) {
	const token = "123456:SECRETTOKEN"
	rt := &routeAwareRoundTripper{}
	client := &tg.Client{
		BaseURL: tg.DefaultBaseURL,
		Token:   token,
		HTTP:    &http.Client{Transport: rt},
	}
	r := &Router{
		tg:      client,
		pending: make(map[int64]*pendingUpload),
	}
	threadID := int64(11)
	user := &db.User{ID: 7, Nickname: "testkeen"}
	msg := &tg.Message{
		Chat:            tg.Chat{ID: -100},
		MessageThreadID: &threadID,
		Document: &tg.Document{
			FileID:   "file-1",
			FileName: "awg11.conf",
			FileSize: 100,
		},
	}

	r.handleDocumentUpload(context.Background(), msg, "per_router", user)

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.sendMessages) == 0 {
		t.Fatal("expected a sendMessage call reporting the download failure")
	}
	last := string(rt.sendMessages[len(rt.sendMessages)-1])
	if strings.Contains(last, token) {
		t.Fatalf("bot token leaked into chat-bound sendMessage body: %s", last)
	}
	if !strings.Contains(last, "не удалось скачать файл") {
		t.Fatalf("expected download-failure text, got: %s", last)
	}
}

func markupHasCallback(kb *tg.InlineKeyboardMarkup, want string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == want {
				return true
			}
		}
	}
	return false
}

func markupHasCallbackPrefix(kb *tg.InlineKeyboardMarkup, wantPrefix string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, wantPrefix) {
				return true
			}
		}
	}
	return false
}

func firstCallbackWithPrefix(t *testing.T, kb *tg.InlineKeyboardMarkup, wantPrefix string) string {
	t.Helper()
	if kb == nil {
		t.Fatalf("nil keyboard, want callback prefix %q", wantPrefix)
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, wantPrefix) {
				return btn.CallbackData
			}
		}
	}
	t.Fatalf("keyboard missing callback prefix %q: %+v", wantPrefix, kb.InlineKeyboard)
	return ""
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

	if len(f.answers) != 1 {
		t.Errorf("expected 1 answer, got %d", len(f.answers))
	}
	if len(f.edits) != 1 {
		t.Errorf("expected 1 edit, got %d", len(f.edits))
	} else if !strings.Contains(f.edits[0], "Уведомления скрыты") {
		t.Errorf("edit text missing silence status: %q", f.edits[0])
	}
}

// Allowlist policy changed 2026-04-30: admin-only restriction lifted, chat-id
// is now the gate. Anyone in the configured group chat can tap; callbacks from
// other chats are rejected.
func TestRouterRejectsWrongChat(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-2",
		From:    tg.User{ID: 99999},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -999 /* not -100 */}},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 {
		t.Errorf("expected 1 answer (rejection), got %d", len(f.answers))
	}
	if !strings.Contains(f.answers[0], "wrong chat") {
		t.Errorf("expected 'wrong chat', got %q", f.answers[0])
	}
	if len(f.edits) != 0 {
		t.Errorf("expected NO edits for wrong-chat, got %d", len(f.edits))
	}
}

// Non-admin user from the right chat is now allowed to tap (per 2026-04-30
// product change). Verifies the policy reversal explicitly.
func TestRouterAllowsNonAdminInRightChat(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 77); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := int64(77)
	q := &tg.CallbackQuery{
		ID:      "cbk-anon",
		From:    tg.User{ID: 99999}, // not admin
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Errorf("expected 1 edit (action applied), got %d", len(f.edits))
	}
}

func TestRouterRejectsSameThreadFromWrongChat(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateTelegramTopic(uid, -200, 77); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, ExtraChatIDs: []int64{-200}, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := int64(77)
	q := &tg.CallbackQuery{
		ID:      "cbk-wrong-chat",
		From:    tg.User{ID: 99999},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid, Text: "panel"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 0 {
		t.Fatalf("wrong chat must not apply router action, edits=%v", f.edits)
	}
	if len(f.answers) != 1 {
		t.Fatalf("expected rejection answer, got %v", f.answers)
	}
}

func TestRouterAllowsSecondaryConfiguredGroupTopic(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateTelegramTopic(uid, -200, 77); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, ExtraChatIDs: []int64{-200}, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := int64(77)
	q := &tg.CallbackQuery{
		ID:      "cbk-secondary-chat",
		From:    tg.User{ID: 99999},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -200}, MessageThreadID: &tid, Text: "panel"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Fatalf("secondary group action should apply once, got edits=%v answers=%v", f.edits, f.answers)
	}
}

func TestRouterRejectsUnconfiguredGroup(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateTelegramTopic(uid, -200, 77); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, ExtraChatIDs: []int64{-200}, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := int64(77)
	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:      "cbk-unconfigured-chat",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -300}, MessageThreadID: &tid, Text: "panel"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	})

	if len(f.edits) != 0 || len(f.answers) != 1 {
		t.Fatalf("unconfigured group should be ignored, edits=%v answers=%v", f.edits, f.answers)
	}
}

func TestRouterHandleCallback_MissingRouterDoesNotEnqueueCommand(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:      "cbk-missing-router",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
		Data:    "diag_now:999999:_menu",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 0 {
		t.Fatalf("missing router callback must not enqueue command, got %+v", sink.calls)
	}
	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "роутер") {
		t.Fatalf("missing router should be answered to operator, got answers=%v", f.answers)
	}
}

func TestACL_RejectsOwnerOperatorCommandCallbacksBeforeRouterTopic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		actor int64
		setup func(t *testing.T, d *db.DB, uid int64)
	}{
		{
			name:  "owner",
			actor: 555,
			setup: func(t *testing.T, d *db.DB, uid int64) {
				t.Helper()
				if err := d.Users().SetTelegramUserID(uid, 555); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "operator",
			actor: 777,
			setup: func(t *testing.T, d *db.DB, uid int64) {
				t.Helper()
				if err := d.Users().SetTelegramUserID(uid, 555); err != nil {
					t.Fatal(err)
				}
				if err := d.RouterOperators().Add(uid, 777, 12345); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, uid := newTestDB(t)
			tc.setup(t, d, uid)
			f := &fakeRouterTG{}
			sink := &fakeEnqueuer{}
			r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

			r.HandleCallback(context.Background(), &tg.CallbackQuery{
				ID:      "diag-before-topic",
				From:    tg.User{ID: tc.actor},
				Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
				Data:    "diag_now:" + itoa(uid) + ":_menu",
			})

			if len(sink.calls) != 0 {
				t.Fatalf("non-admin %s must not enqueue command before router topic exists: %+v", tc.name, sink.calls)
			}
			if len(f.answers) != 1 || !strings.Contains(f.answers[0], "топик") {
				t.Fatalf("expected topic rejection toast, got answers=%v", f.answers)
			}
		})
	}
}

func TestACL_AllowsAdminCommandCallbackBeforeRouterTopic(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:      "diag-admin-before-topic",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "diag_now:" + itoa(uid) + ":_menu",
	})

	if len(sink.calls) != 1 || sink.calls[0].action != "diag_now" {
		t.Fatalf("admin bootstrap callback should still enqueue command, got %+v", sink.calls)
	}
}

func TestRouterRejectsLegacyRoutesCloseFromNonOwnerInRouterTopic(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 77); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().SetTelegramUserID(uid, 111); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	q := &tg.CallbackQuery{
		ID:      "routes-close-legacy",
		From:    tg.User{ID: 999},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: ptrInt64(77), Text: "routes panel"},
		Data:    "routes_close:0:_panel_",
	}

	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 0 {
		t.Fatalf("legacy routes_close:0 must not let non-owner close router panel, edits=%v", f.edits)
	}
	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "роутер") {
		t.Fatalf("expected router ACL rejection toast, answers=%v", f.answers)
	}
}

func keyboardContainsCallback(kb *tg.InlineKeyboardMarkup, callback string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == callback {
				return true
			}
		}
	}
	return false
}

func assertNoEnglishUserFacingCopy(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{
		"router not found",
		"user not found",
		"router is not bound",
		"command sink",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("user-facing copy still contains English %q in:\n%s", forbidden, text)
		}
	}
}

func TestRouterUnknownAction(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-3",
		From:    tg.User{ID: 12345},
		Message: tg.Message{Chat: tg.Chat{ID: -100}}, // right chat, so we get past gate
		Data:    "frobnicate:1:x",
	}
	r.HandleCallback(context.Background(), q)
	if len(f.answers) != 1 {
		t.Fatal("expected answerCallback")
	}
	if !strings.Contains(f.answers[0], "неизвестная") {
		t.Errorf("expected unknown-button toast, got %q", f.answers[0])
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

func TestRouterDispatchesCommandAction(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-diag",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "🔴 alert"},
		Data:    "diag_now:" + itoa(uid) + ":tunnel_amnezia_for_awg2",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.calls))
	}
	if sink.calls[0].action != "diag_now" || sink.calls[0].userID != uid {
		t.Errorf("got %+v", sink.calls[0])
	}
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "Отправлено роутеру") {
		t.Errorf("expected edit containing 'Отправлено роутеру', got %v", f.edits)
	}
}

func TestRouterRestartTunnelRequiresAwgManagerConfirm(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-restart",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "🔴 alert"},
		Data:    "restart_tunnel:" + itoa(uid) + ":tunnel_amnezia_for_awg2",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 0 {
		t.Fatalf("restart_tunnel callback must require confirmation before enqueue, got %+v", sink.calls)
	}
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "awg-manager") {
		t.Fatalf("expected awg-manager confirmation edit, got %v", f.edits)
	}
	if len(f.editMarkups) != 1 || !markupHasCallbackPrefix(f.editMarkups[0], fmt.Sprintf("maint_confirm:%d:awgmgr:", uid)) {
		t.Fatalf("confirm markup missing awgmgr maint_confirm callback: %+v", f.editMarkups)
	}
}

func TestRouterDispatchesInlineCheckViaTunnel(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-check-via-tunnel",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
		Data:    "check_via_tunnel:" + itoa(uid) + ":_panel_",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.calls))
	}
	if sink.calls[0].action != "check_via_tunnel" || sink.calls[0].userID != uid {
		t.Fatalf("got %+v", sink.calls[0])
	}
}

func TestRouterPanelCommandDoesNotEditStaleSnapshot(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-toggle",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
		Data:    "tunnel_disable:" + itoa(uid) + ":tunnel_awg13:Wireguard3",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.calls))
	}
	if sink.calls[0].action != "tunnel_disable" {
		t.Fatalf("action = %q, want tunnel_disable", sink.calls[0].action)
	}
	if len(f.answers) != 1 {
		t.Fatalf("expected answer toast, got %d", len(f.answers))
	}
	if len(f.edits) != 0 {
		t.Fatalf("panel callback should not edit stale DB snapshot, got edits=%v", f.edits)
	}
}

func TestRouterPanelTunnelRestartEnqueuesWithNDMS(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-restart-tunnel",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
		Data:    "tunnel_restart:" + itoa(uid) + ":tunnel_awg13:Wireguard3",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.calls))
	}
	if sink.calls[0].action != "tunnel_restart" || sink.calls[0].ndms != "Wireguard3" {
		t.Fatalf("call = %+v, want action=tunnel_restart ndms=Wireguard3", sink.calls[0])
	}
	if len(f.answers) != 1 {
		t.Fatalf("expected answer toast, got %d", len(f.answers))
	}
	if len(f.edits) != 0 {
		t.Fatalf("panel callback should not edit stale DB snapshot, got edits=%v", f.edits)
	}
}

func TestRouterTunnelToggleStaleButtonRefreshesInsteadOfCommand(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
	cache := &RoutesCache{TTL: time.Minute}
	cache.Put(uid, wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{
		ID: "awg10", Name: "live", Iface: "nwg0", NDMSName: "Wireguard0", Enabled: true,
	}}})
	r.SetRoutesCache(cache)

	q := &tg.CallbackQuery{
		ID:      "cbk-toggle-stale",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
		Data:    "tunnel_disable:" + itoa(uid) + ":tunnel_awg13:Wireguard3",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 || sink.calls[0].action != "tunnels_status" {
		t.Fatalf("stale toggle should enqueue only live refresh, got %+v", sink.calls)
	}
	if len(f.edits) != 1 || strings.Contains(f.edits[0], "очередь") {
		t.Fatalf("stale toggle should refresh panel, edits=%v", f.edits)
	}
}

func TestRouterTunnelPanelActionStaleNDMSRefreshesInsteadOfCommand(t *testing.T) {
	for _, action := range []string{"tunnel_disable", "tunnel_restart", "tunnel_delete"} {
		t.Run(action, func(t *testing.T) {
			d, uid := newTestDB(t)
			f := &fakeRouterTG{}
			sink := &fakeEnqueuer{}
			r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
			cache := &RoutesCache{TTL: time.Minute}
			cache.Put(uid, wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{
				ID: "awg13", Name: "de", Iface: "nwg9", NDMSName: "Wireguard9", Enabled: true,
			}}})
			r.SetRoutesCache(cache)

			q := &tg.CallbackQuery{
				ID:      "cbk-stale-ndms",
				From:    tg.User{ID: 12345},
				Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
				Data:    action + ":" + itoa(uid) + ":tunnel_awg13:Wireguard3",
			}
			r.HandleCallback(context.Background(), q)

			if len(sink.calls) != 1 || sink.calls[0].action != "tunnels_status" {
				t.Fatalf("stale %s should enqueue only live refresh, got %+v", action, sink.calls)
			}
			if len(f.edits) != 1 || strings.Contains(f.edits[0], "очередь") {
				t.Fatalf("stale %s should refresh panel, edits=%v", action, f.edits)
			}
		})
	}
}

func TestRouterTunnelToggleStaleEnabledStateRefreshesInsteadOfCommand(t *testing.T) {
	for _, tc := range []struct {
		name           string
		action         string
		currentEnabled bool
	}{
		{name: "disable already disabled", action: "tunnel_disable", currentEnabled: false},
		{name: "enable already enabled", action: "tunnel_enable", currentEnabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, uid := newTestDB(t)
			f := &fakeRouterTG{}
			sink := &fakeEnqueuer{}
			r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
			cache := &RoutesCache{TTL: time.Minute}
			cache.Put(uid, wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{
				ID: "awg13", Name: "de", Iface: "nwg3", NDMSName: "Wireguard3", Enabled: tc.currentEnabled,
			}}})
			r.SetRoutesCache(cache)

			q := &tg.CallbackQuery{
				ID:      "cbk-stale-enabled",
				From:    tg.User{ID: 12345},
				Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
				Data:    tc.action + ":" + itoa(uid) + ":tunnel_awg13:Wireguard3",
			}
			r.HandleCallback(context.Background(), q)

			if len(sink.calls) != 1 || sink.calls[0].action != "tunnels_status" {
				t.Fatalf("stale %s should enqueue only live refresh, got %+v", tc.action, sink.calls)
			}
			if len(f.edits) != 1 || strings.Contains(f.edits[0], "очередь") {
				t.Fatalf("stale %s should refresh panel, edits=%v", tc.action, f.edits)
			}
		})
	}
}

func TestRouterTunnelDeleteAskRendersConfirm(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
	cache := &RoutesCache{TTL: time.Minute}
	cache.Put(uid, wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{
		ID: "awg13", Name: "de", Iface: "nwg3", NDMSName: "Wireguard3", Enabled: true,
	}}})
	r.SetRoutesCache(cache)

	q := &tg.CallbackQuery{
		ID:      "cbk-delete-ask",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
		Data:    "tunnel_delete_ask:" + itoa(uid) + ":tunnel_awg13:Wireguard3",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "Удалить туннель") {
		t.Fatalf("expected delete confirmation edit, got %v", f.edits)
	}
	if len(f.editMarkups) != 1 || !markupHasCallback(f.editMarkups[0], "tunnel_delete:"+itoa(uid)+":tunnel_awg13:Wireguard3:awg13") {
		t.Fatalf("confirm markup missing tunnel_delete callback: %+v", f.editMarkups)
	}
}

func TestRouterTunnelDeleteAskRendersConfirmWithoutNDMSName(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
	cache := &RoutesCache{TTL: time.Minute}
	cache.Put(uid, wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{
		ID: "kernel-real-id", Name: "kernel", Iface: "opkgtun10", Enabled: true,
	}}})
	r.SetRoutesCache(cache)

	q := &tg.CallbackQuery{
		ID:      "cbk-delete-ask-kernel",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
		Data:    "tunnel_delete_ask:" + itoa(uid) + ":tunnel_kernel-real-id::kernel-real-id",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "kernel") {
		t.Fatalf("expected delete confirmation edit, got %v", f.edits)
	}
	if len(f.editMarkups) != 1 || !markupHasCallback(f.editMarkups[0], "tunnel_delete:"+itoa(uid)+":tunnel_kernel-real-id::kernel-real-id") {
		t.Fatalf("confirm markup missing explicit tunnel_id callback: %+v", f.editMarkups)
	}
}

func TestRouterTunnelDeleteAskStaleButtonRefreshesInsteadOfConfirm(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
	cache := &RoutesCache{TTL: time.Minute}
	cache.Put(uid, wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{
		ID: "awg10", Name: "live", Iface: "nwg0", NDMSName: "Wireguard0", Enabled: true,
	}}})
	r.SetRoutesCache(cache)

	q := &tg.CallbackQuery{
		ID:      "cbk-delete-stale",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
		Data:    "tunnel_delete_ask:" + itoa(uid) + ":tunnel_awg13:Wireguard3",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 || strings.Contains(f.edits[0], "Удалить туннель") {
		t.Fatalf("stale delete button must refresh panel instead of confirm, edits=%v", f.edits)
	}
	if len(sink.calls) != 1 || sink.calls[0].action != "tunnels_status" {
		t.Fatalf("stale delete button should enqueue live refresh, got %+v", sink.calls)
	}
}

func TestRouterCommandActionWithoutSinkRejects(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	// NewRouter (no sink) — command actions must reject
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
	q := &tg.CallbackQuery{
		ID:      "cbk-no-sink",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "diag_now:" + itoa(uid) + ":tunnel_x",
	}
	r.HandleCallback(context.Background(), q)
	if len(f.answers) != 1 {
		t.Fatal("expected answerCallback")
	}
	if !strings.Contains(f.answers[0], "Ошибка") {
		t.Errorf("expected error toast when sink is nil, got %q", f.answers[0])
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
	if len(f.answers) != 1 {
		t.Fatal("expected answer")
	}
	if !strings.Contains(f.answers[0], "Ошибка") {
		t.Errorf("expected error in answer, got %q", f.answers[0])
	}
	if len(f.edits) != 0 {
		t.Error("on error, should NOT edit")
	}
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }

// fakeRouterTGFull adds capture of SendMessageWithReplyKeyboard + DeleteMessage
type fakeRouterTGFull struct {
	fakeRouterTG
	rkSends   []rkSend
	deleted   []deleteCall
	deleteErr error
}
type rkSend struct {
	chatID  int64
	thread  *int64
	text    string
	markup  any
	replyTo *int64
}
type deleteCall struct{ chatID, msgID int64 }

func (f *fakeRouterTGFull) SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rkSends = append(f.rkSends, rkSend{chatID, threadID, text, markup, replyTo})
	return 100, nil
}
func (f *fakeRouterTGFull) DeleteMessage(ctx context.Context, chatID, msgID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, deleteCall{chatID, msgID})
	return f.deleteErr
}

func TestRouterHandleMessage_RoutesPerRouter(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 11); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9, UI: UIConfigSnapshot{DeleteUserCommandMessages: true}})

	tid := int64(11)
	msg := &tg.Message{
		MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "📊 Что происходит?",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.rkSends) != 1 {
		t.Fatalf("want 1 smart-reply send, got %d", len(f.rkSends))
	}
	if !strings.Contains(f.rkSends[0].text, "vasya") {
		t.Errorf("smart reply missing nickname: %s", f.rkSends[0].text)
	}
	if len(f.deleted) != 1 || f.deleted[0].msgID != 42 {
		t.Errorf("expected DeleteMessage(_, 42), got %+v", f.deleted)
	}
}

func TestRouterHandleMessage_RejectsWrongChat(t *testing.T) {
	d, uid := newTestDB(t)
	_ = uid
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100})
	msg := &tg.Message{Chat: tg.Chat{ID: -999}, From: tg.User{ID: 12345}, Text: "📊 Что происходит?"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 0 || len(f.deleted) != 0 {
		t.Errorf("must no-op on wrong chat: %+v %+v", f.rkSends, f.deleted)
	}
}

func TestRouterHandleMessage_NonAdminIgnored(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 99999}, Text: "📊 Что происходит?"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 0 {
		t.Errorf("non-admin message must be ignored")
	}
}

func TestRouterHandleMessage_DeleteFailureDoesNotAbort(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().UpdateThreadID(uid, 11)
	f := &fakeRouterTGFull{deleteErr: fmt.Errorf("403: bot lacks can_delete_messages")}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, UI: UIConfigSnapshot{DeleteUserCommandMessages: true}})
	tid := int64(11)
	msg := &tg.Message{MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345}, MessageThreadID: &tid, Text: "📊 Что происходит?"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Errorf("smart reply must still be sent after delete failure")
	}
}

func TestRouterDispatchSmartReply_RendersOK(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().UpdateThreadID(uid, 11)
	// Insert a fresh tunnel event so the smart reply sees a Tunnel.
	now := time.Now().UTC()
	_ = d.Events().Insert(uid, "tunnel_awg11", "ok", `{"tunnel_name":"amnezia","interface":"nwg0","handshake_age_sec":12,"ping_check_status":"ok","ping_check_last_latency_ms":15}`, now)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	msg := &tg.Message{
		MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "📊 Что происходит?",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Fatalf("want 1 send, got %d", len(f.rkSends))
	}
	body := f.rkSends[0].text
	for _, want := range []string{"✅", "vasya", "amnezia", "12с", "15 мс"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestRouterDispatchSmartReply_SilencedIncidentStillShownInManualStatus(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().UpdateThreadID(uid, 11)
	now := time.Now().UTC()
	_ = d.Events().Insert(uid, "tunnel_awg11", "ok", `{"tunnel_name":"amnezia","interface":"nwg0","handshake_age_sec":12,"ping_check_status":"ok","ping_check_last_latency_ms":15}`, now)
	// Insert a HARD incident, then silence it.
	_, err := d.SQL().Exec(
		`INSERT INTO incident_state(user_id, check_name, current_status, consecutive_fails, hard_since, silenced_until, acked)
		 VALUES (?, 'tunnel_awg11', 'hard', 5, ?, datetime('now','+1 hour'), 0)`,
		uid, now.Add(-10*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	msg := &tg.Message{
		MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "📊 Что происходит?",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Fatalf("want 1 send, got %d", len(f.rkSends))
	}
	body := f.rkSends[0].text
	if !strings.Contains(body, "🔴") {
		t.Errorf("manual status should show silenced active HARD incident, got: %s", body)
	}
}

func TestRouterDispatchSmartReply_AckedIncidentStillShownInManualStatus(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().UpdateThreadID(uid, 11)
	now := time.Now().UTC()
	_ = d.Events().Insert(uid, "dns", "fail", `{"error":"timeout"}`, now)
	_, err := d.SQL().Exec(
		`INSERT INTO incident_state(user_id, check_name, current_status, consecutive_fails, hard_since, acked)
		 VALUES (?, 'dns', 'hard', 6, ?, 1)`,
		uid, now.Add(-3*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	msg := &tg.Message{
		MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "📊 Что происходит?",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Fatalf("want 1 send, got %d", len(f.rkSends))
	}
	body := f.rkSends[0].text
	if !strings.Contains(body, "🔴") || !strings.Contains(body, "сайты по имени") {
		t.Fatalf("manual status should show acked active HARD incident, got:\n%s", body)
	}
}

func TestRouterDispatchSmartReply_NeverReportedShowsSpecialMessage(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().UpdateThreadID(uid, 11)
	// Do NOT insert any events — user has never reported.
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	msg := &tg.Message{
		MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "📊 Что происходит?",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Fatalf("want 1 send, got %d", len(f.rkSends))
	}
	body := f.rkSends[0].text
	if !strings.Contains(body, "🆕") || !strings.Contains(body, "ещё не отчитывался") {
		t.Errorf("never-reported user must get the special message, got: %s", body)
	}
	// The fabricated "1440 минут назад" must NOT appear.
	if strings.Contains(body, "1440") {
		t.Errorf("never-reported message must not fabricate a 1440-minute timestamp: %s", body)
	}
	// The Offline emoji must NOT appear.
	if strings.Contains(body, "📵") {
		t.Errorf("never-reported user must not be classified as Offline: %s", body)
	}
	// Regression: never-reported message must NOT reference journalctl.
	// Keenetic runs Entware (no systemd); the agent uses init.d S99wg-monitor.
	if strings.Contains(body, "journalctl") {
		t.Errorf("never-reported message must not reference journalctl (no systemd on Keenetic): %s", body)
	}
}

// Цикл 1: обслуживание переехало в мини-апп. Команды и кнопки меню бота
// больше не отвечают -- ни админу, ни оператору, и ничего не ставят в очередь.
func TestRouterHandleMessage_RemovedMaintenanceEntriesSilent(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 12345)
	tid := int64(55)
	for _, from := range []int64{12345, 200} {
		for _, text := range []string{"/maint", "/upgrade", "🛠 Обслуживание", "⬆ Обновить пакеты"} {
			f := &fakeRouterTG{}
			sink := &fakeEnqueuer{}
			r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
			r.HandleMessage(context.Background(), &tg.Message{
				MessageID:       99,
				Chat:            tg.Chat{ID: -100},
				From:            tg.User{ID: from},
				MessageThreadID: &tid,
				Text:            text,
			})
			if len(f.sentMsgs) != 0 || len(f.sentMarkups) != 0 || len(f.edits) != 0 || len(sink.calls) != 0 {
				t.Errorf("from=%d %q: бот ответил (msgs=%v markups=%d edits=%v calls=%+v)",
					from, text, f.sentMsgs, len(f.sentMarkups), f.edits, sink.calls)
			}
		}
	}
}

func TestCollectTunnelViewsDropsStaleEvents(t *testing.T) {
	d, uid := newTestDB(t)
	r := NewRouterWithSink(d, &fakeRouterTG{}, nil, Config{})
	if err := d.Events().Insert(uid, "tunnel_old", "ok", `{"tunnel_name":"old","interface":"nwg1"}`, time.Now().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "tunnel_fresh", "ok", `{"tunnel_name":"fresh","interface":"nwg2"}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	views := r.collectTunnelViews(uid)
	if len(views) != 1 || views[0].Name != "fresh" {
		t.Fatalf("want only fresh tunnel view, got %+v", views)
	}
}

func TestCollectTunnelViewsPrefersLiveRouteSnapshot(t *testing.T) {
	d, uid := newTestDB(t)
	r := NewRouterWithSink(d, &fakeRouterTG{}, nil, Config{})
	cache := &RoutesCache{TTL: time.Minute}
	cache.Put(uid, wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{
		ID: "awg10", Name: "live", Iface: "nwg0", NDMSName: "Wireguard0",
		Enabled: true, Status: "running", HasHandshake: true, HandshakeAge: 8,
		PingStatus: "ok", PingFails: 0, PingFailMax: 3,
	}}})
	r.SetRoutesCache(cache)
	if err := d.Events().Insert(uid, "tunnel_deleted", "ok", `{"tunnel_name":"deleted","interface":"nwg9"}`, time.Now()); err != nil {
		t.Fatal(err)
	}

	views := r.collectTunnelViews(uid)
	if len(views) != 1 || views[0].Name != "live" || views[0].CheckName != "tunnel_awg10" {
		t.Fatalf("want live cache tunnel only, got %+v", views)
	}
}

func TestCollectActiveIncidentsDropsDeletedTunnelWhenLiveSnapshotExists(t *testing.T) {
	d, uid := newTestDB(t)
	r := NewRouterWithSink(d, &fakeRouterTG{}, nil, Config{})
	cache := &RoutesCache{TTL: time.Minute}
	cache.Put(uid, wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{
		ID: "awg10", Name: "live", Iface: "nwg0", NDMSName: "Wireguard0", Enabled: true,
	}}})
	r.SetRoutesCache(cache)
	hs := time.Now().Add(-10 * time.Minute)
	if err := d.State().Save(uid, "tunnel_awg13", db.IncidentState{
		UserID: uid, CheckName: "tunnel_awg13", CurrentStatus: "hard", ConsecutiveFails: 5, HardSince: &hs,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.State().Save(uid, "dns", db.IncidentState{
		UserID: uid, CheckName: "dns", CurrentStatus: "hard", ConsecutiveFails: 5, HardSince: &hs,
	}); err != nil {
		t.Fatal(err)
	}

	incidents := r.collectActiveIncidents(uid)
	if len(incidents) != 1 || incidents[0].CheckName != "dns" {
		t.Fatalf("want only non-tunnel incident after live cache excludes deleted tunnel, got %+v", incidents)
	}
}

// makeCBQ builds a minimal CallbackQuery for the given callback_data string.
func makeCBQ(data string) *tg.CallbackQuery {
	return &tg.CallbackQuery{
		ID:      "cbq-test",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 42, Chat: tg.Chat{ID: -100}, Text: "panel text"},
		Data:    data,
	}
}

func TestRouterHandleCallback_MaintRestart_RendersConfirm(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	q := makeCBQ(fmt.Sprintf("maint_restart:%d:hrneo", uid))
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Fatalf("expected 1 edit (confirm screen), got %d", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "HydraRoute Neo") {
		t.Errorf("confirm screen should mention HydraRoute Neo, got: %q", f.edits[0])
	}
	// A pending entry must exist for this user.
	r.pendingMaint.mu.Lock()
	var found *pendingMaint
	for _, p := range r.pendingMaint.m {
		if p.UserID == uid && p.Name == "hrneo" {
			found = p
			break
		}
	}
	r.pendingMaint.mu.Unlock()
	if found == nil {
		t.Error("pendingMaint entry not created for hrneo restart")
	}
}

func TestRouterHandleCallback_MaintRestart_RouterMovedToApp(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	r.HandleCallback(context.Background(), makeCBQ(fmt.Sprintf("maint_restart:%d:router", uid)))

	if len(f.answers) != 1 || f.answers[0] != "это действие переехало в приложение" {
		t.Errorf("answers=%v", f.answers)
	}
	if len(f.edits) != 0 {
		t.Errorf("подтверждения перезагрузки быть не должно, edits=%v", f.edits)
	}
	r.pendingMaint.mu.Lock()
	count := len(r.pendingMaint.m)
	r.pendingMaint.mu.Unlock()
	if count != 0 {
		t.Errorf("токен перезагрузки создан: %d", count)
	}
}

func TestRouterHandleCallback_RemovedMaintPanelCallbacksUnknown(t *testing.T) {
	d, uid := newTestDB(t)
	for _, data := range []string{
		fmt.Sprintf("maint_open:%d:_panel_", uid),
		fmt.Sprintf("maint_close:%d:_panel_", uid),
		fmt.Sprintf("maint_fw_open:%d:_panel_", uid),
		fmt.Sprintf("maint_fw_check:%d:_panel_", uid),
		fmt.Sprintf("maint_fw_install:%d:_panel_", uid),
		fmt.Sprintf("maint_fw_confirm:%d:_panel_:deadbeef", uid),
	} {
		f := &fakeRouterTG{}
		sink := &fakeEnqueuer{}
		r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
		r.HandleCallback(context.Background(), makeCBQ(data))
		if len(f.answers) != 1 || f.answers[0] != "неизвестная кнопка" || len(sink.calls) != 0 || len(f.edits) != 0 {
			t.Errorf("%s: answers=%v calls=%+v edits=%v", data, f.answers, sink.calls, f.edits)
		}
	}
}

func allTexts(ss []rkSend) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.text
	}
	return out
}

func TestRouterDispatchListUsers(t *testing.T) {
	d, _ := newTestDB(t) // creates "vasya"
	if _, err := d.Users().InsertWithKind("petya", "tok2", "2.2.2.2", "nwg0", db.KindMobile); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().InsertWithKind("masha", "tok3", "3.3.3.3", "nwg0", db.KindStatic); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	if err := d.KV().SetTopicID("summary", 77); err != nil {
		t.Fatal(err)
	}
	tid := int64(77)
	msg := &tg.Message{MessageID: 50, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345}, MessageThreadID: &tid, Text: "📋 Список юзеров"}
	r.HandleMessage(context.Background(), msg)
	if len(f.sentMsgs) == 0 && len(f.rkSends) == 0 {
		t.Fatal("no message sent")
	}
	all := strings.Join(append(append([]string{}, f.sentMsgs...), allTexts(f.rkSends)...), "\n")
	for _, want := range []string{"vasya", "petya", "masha"} {
		if !strings.Contains(all, want) {
			t.Errorf("list missing %s in:\n%s", want, all)
		}
	}
	if !strings.Contains(all, "Всего: 3") {
		t.Errorf("missing total count in:\n%s", all)
	}
}

func TestRouterDispatchFleetHealth_AllGreen(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	if err := d.KV().SetTopicID("summary", 77); err != nil {
		t.Fatal(err)
	}
	tid := int64(77)
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345}, MessageThreadID: &tid, Text: "📊 Здоровье флота"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(f.rkSends))
	}
	body := f.rkSends[0].text
	if !strings.Contains(body, "Активных HARD: 0") {
		t.Errorf("expected zero-incident body, got: %s", body)
	}
}

func TestRouterDispatchFleetHealth_WithIncidents(t *testing.T) {
	d, uid := newTestDB(t)
	hs := time.Now().Add(-10 * time.Minute)
	st := db.IncidentState{UserID: uid, CheckName: "tunnel_awg11", CurrentStatus: "hard", ConsecutiveFails: 5, HardSince: &hs}
	if err := d.State().Save(uid, "tunnel_awg11", st); err != nil {
		t.Fatal(err)
	}
	uid2, err := d.Users().Insert("petya", "tok2", "2.2.2.2", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	hs2 := time.Now().Add(-5 * time.Minute)
	st2 := db.IncidentState{UserID: uid2, CheckName: "dns", CurrentStatus: "hard", ConsecutiveFails: 3, HardSince: &hs2}
	if err := d.State().Save(uid2, "dns", st2); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	if err := d.KV().SetTopicID("summary", 77); err != nil {
		t.Fatal(err)
	}
	tid := int64(77)
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345}, MessageThreadID: &tid, Text: "📊 Здоровье флота"}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 1 {
		t.Fatal("no reply")
	}
	body := f.rkSends[0].text
	for _, want := range []string{"Активных HARD: 2", "tunnel_awg11", "dns", "vasya", "petya"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

// ACL gate: a non-admin user who is not the bound owner of the targeted
// router gets rejected with the "это не твой роутер" toast, no action runs.
func TestACL_RejectsNonOwner(t *testing.T) {
	d, uid := newTestDB(t)
	const owner, intruder = int64(11111), int64(22222)
	if err := d.Users().SetTelegramUserID(uid, owner); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-acl-1",
		From:    tg.User{ID: intruder},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "не твой роутер") {
		t.Fatalf("expected 'не твой роутер' toast, got %v", f.answers)
	}
	if len(f.edits) != 0 {
		t.Fatalf("expected no edits (action must not run), got %d", len(f.edits))
	}
}

// ACL gate: bound owner can tap callbacks targeting their own router from the
// recorded per-router topic.
func TestACL_AllowsBoundOwner(t *testing.T) {
	d, uid := newTestDB(t)
	const owner = int64(11111)
	const thread = int64(4242)
	if err := d.Users().SetTelegramUserID(uid, owner); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(uid, thread); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := thread
	q := &tg.CallbackQuery{
		ID:      "cbk-acl-2",
		From:    tg.User{ID: owner},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Fatalf("expected action to apply (1 edit), got %d (answers=%v)", len(f.edits), f.answers)
	}
}

func TestACL_BoundOperatorRejectsForeignTopicCallback(t *testing.T) {
	d, uid := newTestDB(t)
	const owner = int64(11111)
	const operator = int64(22222)
	const routerThread = int64(4242)
	const foreignThread = int64(9999)
	if err := d.Users().SetTelegramUserID(uid, owner); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(uid, routerThread); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(uid, operator, 12345); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := foreignThread
	q := &tg.CallbackQuery{
		ID:      "cbk-acl-bound-operator-foreign-topic",
		From:    tg.User{ID: operator},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid, Text: "diag"},
		Data:    "diag_now:" + itoa(uid) + ":_menu",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 0 {
		t.Fatalf("foreign-topic operator callback must not enqueue command, got %+v", sink.calls)
	}
	if len(f.edits) != 0 {
		t.Fatalf("foreign-topic operator callback must not edit UI, edits=%v", f.edits)
	}
	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "топик этого роутера") {
		t.Fatalf("expected foreign-topic rejection toast, got %v", f.answers)
	}
}

// ACL gate: admin override — admin can tap regardless of owner binding.
func TestACL_AdminBypassesOwnerCheck(t *testing.T) {
	d, uid := newTestDB(t)
	const owner, admin = int64(11111), int64(12345)
	if err := d.Users().SetTelegramUserID(uid, owner); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: admin, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-acl-3",
		From:    tg.User{ID: admin},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Fatalf("admin should bypass ACL: expected 1 edit, got %d (answers=%v)", len(f.edits), f.answers)
	}
}

// ACL TOFU bind: unbound router, callback from owner's own per-router topic
// → SetTelegramUserID happens and action proceeds. The next non-owner tap
// will be rejected by TestACL_RejectsNonOwner-style logic.
func TestACL_TOFUBindFromOwnerTopic(t *testing.T) {
	d, uid := newTestDB(t)
	const thread = int64(4242)
	const newOwner = int64(99999)
	if err := d.Users().UpdateThreadID(uid, thread); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := thread
	q := &tg.CallbackQuery{
		ID:      "cbk-acl-4",
		From:    tg.User{ID: newOwner},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	u, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if u.TelegramUserID == nil || *u.TelegramUserID != newOwner {
		t.Fatalf("TOFU did not bind: got %v, want %d", u.TelegramUserID, newOwner)
	}
	if len(f.edits) != 1 {
		t.Fatalf("expected action to apply after TOFU, got %d edits", len(f.edits))
	}
}

// ACL: unbound router with a known topic, callback NOT from that topic —
// reject. Otherwise inline buttons from stale/foreign topic messages become
// global controls for the wrong router before TOFU owner binding happens.
func TestACL_TOFUBindFailureRejectsCallback(t *testing.T) {
	d, uid := newTestDB(t)
	d.SQL().SetMaxOpenConns(1)
	const thread = int64(4242)
	const newOwner = int64(99999)
	if err := d.Users().UpdateThreadID(uid, thread); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL().Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.SQL().Exec("PRAGMA query_only = OFF") })

	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := thread
	q := &tg.CallbackQuery{
		ID:      "cbk-acl-tofu-fail",
		From:    tg.User{ID: newOwner},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid, Text: "offline"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	if r.aclAllow(context.Background(), q, Args{UserID: uid}) {
		t.Fatal("TOFU bind failure must reject callback")
	}
	if len(f.answers) == 0 {
		t.Fatalf("TOFU bind failure should answer callback with an error")
	}
}

func TestACL_UnboundWithKnownTopicRejectsForeignTopicCallback(t *testing.T) {
	d, uid := newTestDB(t)
	const ownThread = int64(4242)
	const otherThread = int64(9999)
	const tapper = int64(77777)
	if err := d.Users().UpdateThreadID(uid, ownThread); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := otherThread
	q := &tg.CallbackQuery{
		ID:      "cbk-acl-5",
		From:    tg.User{ID: tapper},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	u, _ := d.Users().GetByID(uid)
	if u.TelegramUserID != nil {
		t.Fatalf("must NOT bind when callback not in owner's topic, got %v", u.TelegramUserID)
	}
	if len(f.edits) != 0 {
		t.Fatalf("foreign-topic callback must not apply action, edits=%v", f.edits)
	}
	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "топик этого роутера") {
		t.Fatalf("expected foreign-topic rejection toast, got %v", f.answers)
	}
}

func TestACL_UnboundWithoutTopicRejectsCallback(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-acl-unbound-no-topic",
		From:    tg.User{ID: 77777},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
		Data:    "diag_now:" + itoa(uid) + ":_menu",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 0 {
		t.Fatalf("unbound router without topic must not enqueue command, got %+v", sink.calls)
	}
	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "не привязан") {
		t.Fatalf("expected unbound-topic rejection toast, got %v", f.answers)
	}
}

func TestRouterConsumePendingRebindWrongActorRejectedWithoutConsuming(t *testing.T) {
	r := &Router{}
	r.putPendingRebind(&pendingRebind{
		UserID:    42,
		ActorTGID: 111,
		SrcID:     "old",
		DstID:     "new",
		Token:     "tok1",
		ExpiresAt: time.Now().Add(time.Minute),
	})

	if _, ok := r.consumePendingRebindForActor(42, 222, "tok1"); ok {
		t.Fatal("wrong actor should not consume pending rebind")
	}
	got, ok := r.consumePendingRebindForActor(42, 111, "tok1")
	if !ok {
		t.Fatal("right actor should still consume pending rebind")
	}
	if got.SrcID != "old" || got.DstID != "new" {
		t.Fatalf("bad pending rebind: %+v", got)
	}
}

func TestACL_AdminCannotUseRouterScopedCallbackFromForeignTopic(t *testing.T) {
	d, uid := newTestDB(t)
	const ownThread = int64(4242)
	const otherThread = int64(9999)
	if err := d.Users().UpdateThreadID(uid, ownThread); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := otherThread
	q := &tg.CallbackQuery{
		ID:      "cbk-admin-foreign-topic",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 0 {
		t.Fatalf("admin foreign-topic callback must not apply action, edits=%v", f.edits)
	}
	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "топик этого роутера") {
		t.Fatalf("expected foreign-topic rejection toast, got %v", f.answers)
	}
}

func TestRouterRoutesAddType_HRNeoUnavailableDoesNotSilentlyDowngradeToNDMS(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	cache := &RoutesCache{TTL: time.Hour}
	cache.Put(uid, wire.RouteSnapshot{
		HRNeo: wire.HRStatus{Installed: true, Running: false},
		Tunnels: []wire.TunnelMeta{{
			ID: "eth3", Name: "WAN", Iface: "eth3", Enabled: true, Available: true,
		}},
	})
	r.SetRoutesCache(cache)

	tid := int64(11)
	q := &tg.CallbackQuery{
		ID:   "cb-routes-add-hr",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: fmt.Sprintf("routes_add_type:%d:_panel_:dns_hr", uid),
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "HR-Neo") {
		t.Fatalf("expected HR-Neo unavailable answer, got answers=%v", f.answers)
	}
	if len(f.edits) != 0 {
		t.Fatalf("must not advance wizard and silently create NDMS draft, edits=%v", f.edits)
	}
}

func TestRouterRoutesAddTunnelOffersAWGManagerTemplatesButton(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
	cache := &RoutesCache{TTL: time.Hour}
	cache.Put(uid, wire.RouteSnapshot{
		HRNeo: wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{{
			ID: "awg11", Name: "exit", Iface: "nwg5", Enabled: true, Available: true,
		}},
	})
	r.SetRoutesCache(cache)

	tid := int64(11)
	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:   "cb-add-type",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: fmt.Sprintf("routes_add_type:%d:_panel_:dns_hr", uid),
	})
	tunnelCallback := firstCallbackWithPrefix(t, f.editMarkups[0], fmt.Sprintf("routes_add_tunnel:%d:_panel_:", uid))

	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:   "cb-add-tunnel",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: tunnelCallback,
	})

	if len(f.editMarkups) < 2 || !markupHasCallbackPrefix(f.editMarkups[1], fmt.Sprintf("routes_tpl_load:%d:_panel_:", uid)) {
		t.Fatalf("expected template load button after tunnel pick, markups=%#v", f.editMarkups)
	}
	if strings.Contains(f.edits[len(f.edits)-1], "template:") {
		t.Fatalf("operator-facing prompt must not ask for raw template:id, got %q", f.edits[len(f.edits)-1])
	}
}

func TestRouterRoutesTemplateLoadEnqueuesCatalogCommand(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	draft := r.RouteWizardStore().PutAddDraft(RouteAddDraft{
		UserID: uid, ActorTGID: 12345, ThreadID: &tid, RouterID: uid,
		Kind: "dns", TunnelID: "awg11", UseHRNeo: true,
	})

	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:   "cb-load-tpl",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: fmt.Sprintf("routes_tpl_load:%d:_panel_:%s", uid, draft.Token),
	})

	if len(sink.calls) != 1 || sink.calls[0].action != "route_templates" {
		t.Fatalf("expected route_templates enqueue, got %+v", sink.calls)
	}
	if len(f.edits) != 1 || !strings.Contains(strings.ToLower(f.edits[0]), "template") {
		t.Fatalf("expected loading edit for templates, got %v", f.edits)
	}
}

func TestRouterRoutesTemplatePickEnqueuesPreviewFromButton(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	draft := r.RouteWizardStore().PutAddDraft(RouteAddDraft{
		UserID: uid, ActorTGID: 12345, ThreadID: &tid, RouterID: uid,
		Kind: "dns", TunnelID: "awg11", UseHRNeo: true,
	})
	tplToken := r.RouteWizardStore().PutTemplateToken(RouteTemplateToken{
		UserID: uid, ActorTGID: 12345, ThreadID: &tid, RouterID: uid,
		DraftToken: draft.Token, TemplateID: "youtube", TemplateName: "YouTube",
	})

	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:   "cb-pick-tpl",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: fmt.Sprintf("routes_tpl_pick:%d:_panel_:%s:%s", uid, draft.Token, tplToken),
	})

	if len(sink.calls) != 1 || sink.calls[0].action != "route_add_plan" {
		t.Fatalf("expected route_add_plan enqueue, got %+v", sink.calls)
	}
	if got, _ := sink.calls[0].args["template_id"].(string); got != "youtube" {
		t.Fatalf("template_id = %q, want youtube; args=%+v", got, sink.calls[0].args)
	}
	if got, _ := sink.calls[0].args["tunnel_id"].(string); got != "awg11" {
		t.Fatalf("tunnel_id = %q, want awg11; args=%+v", got, sink.calls[0].args)
	}
}

func TestRouterRoutesTemplatePageRendersStoredCatalog(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	store := r.RouteWizardStore()
	store.TokenFunc = fixedTokens("draft1")
	draft := store.PutAddDraft(RouteAddDraft{
		UserID: uid, ActorTGID: 12345, ThreadID: &tid, RouterID: uid,
		Kind: "dns", TunnelID: "awg11",
	})
	templates := []wire.RouteTemplate{}
	for i := 1; i <= 11; i++ {
		templates = append(templates, wire.RouteTemplate{
			ID: "svc" + itoa(int64(i)), Name: "Service " + itoa(int64(i)), DNS: []string{"svc.example"},
		})
	}
	store.PutTemplateCatalog(RouteTemplateCatalog{
		UserID: uid, ActorTGID: 12345, ThreadID: &tid, RouterID: uid,
		DraftToken: draft.Token, Templates: templates,
	})

	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:   "cb-page-tpl",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: fmt.Sprintf("routes_tpl_page:%d:_panel_:%s:1", uid, draft.Token),
	})

	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "Service 9") || strings.Contains(f.edits[0], "- Service 1 -") {
		t.Fatalf("expected second template page, edits=%q", f.edits)
	}
	if len(f.editMarkups) != 1 || !markupHasCallbackPrefix(f.editMarkups[0], fmt.Sprintf("routes_tpl_pick:%d:_panel_:%s:", uid, draft.Token)) {
		t.Fatalf("expected pick buttons on stored catalog page, markups=%#v", f.editMarkups)
	}
	if len(f.answers) != 1 {
		t.Fatalf("expected callback answer, got %v", f.answers)
	}
}

func TestRouterRoutesAddConfirmKeepsDraftWhenEnqueueFails(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{err: fmt.Errorf("queue down")}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	store := r.RouteWizardStore()
	store.TokenFunc = fixedTokens("draft1", "confirm1")
	draft := store.PutAddDraft(RouteAddDraft{
		UserID: uid, ActorTGID: 12345, ThreadID: &tid, RouterID: uid,
		Kind: "dns", Name: "media", TunnelID: "awg11", Targets: []string{"example.com"},
	})
	confirmed, ok := store.SetAddConfirm(uid, &tid, uid, draft.Token, "hash1")
	if !ok {
		t.Fatal("expected confirm token")
	}
	q := &tg.CallbackQuery{
		ID:   "cb-route-add-confirm",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: fmt.Sprintf("routes_add_confirm:%d:_panel_:%s:%s", uid, draft.Token, confirmed.ConfirmToken),
	}

	r.HandleCallback(context.Background(), q)
	sink.err = nil
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 || sink.calls[0].action != "route_add" {
		t.Fatalf("same confirm should enqueue after transient failure, calls=%+v answers=%v", sink.calls, f.answers)
	}
}

func TestRouterRoutesDeleteConfirmKeepsDraftWhenEnqueueFails(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{err: fmt.Errorf("queue down")}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	store := r.RouteWizardStore()
	store.TokenFunc = fixedTokens("draft1", "confirm1")
	draft := store.PutDeleteDraft(RouteDeleteDraft{
		UserID: uid, ActorTGID: 12345, ThreadID: &tid, RouterID: uid,
		Kind: "dns", RouteID: "rule1", PreviewHash: "hash1",
	})
	confirmed, ok := store.SetDeleteConfirm(uid, &tid, uid, draft.Token, "hash1")
	if !ok {
		t.Fatal("expected confirm token")
	}
	q := &tg.CallbackQuery{
		ID:   "cb-route-del-confirm",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: fmt.Sprintf("routes_del_confirm:%d:_panel_:%s:%s", uid, draft.Token, confirmed.ConfirmToken),
	}

	r.HandleCallback(context.Background(), q)
	sink.err = nil
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 || sink.calls[0].action != "route_delete" {
		t.Fatalf("same confirm should enqueue after transient failure, calls=%+v answers=%v", sink.calls, f.answers)
	}
}

func TestRouterRoutesRollback_RendersReverseConfirmWithoutCache(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 11); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	tid := int64(11)
	q := &tg.CallbackQuery{
		ID:   "cb-routes-rollback",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: fmt.Sprintf("routes_rollback:%d:old:new", uid),
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Fatalf("rollback should render confirmation edit, got edits=%v", f.edits)
	}
	if !strings.Contains(f.edits[0], "new") || !strings.Contains(f.edits[0], "old") {
		t.Fatalf("rollback confirm text should mention reverse direction, got %q", f.edits[0])
	}
	if len(f.editMarkups) != 1 || !markupHasCallbackPrefix(f.editMarkups[0], fmt.Sprintf("routes_confirm:%d:new:old:", uid)) {
		t.Fatalf("rollback confirm should point to reverse routes_confirm, markup=%#v", f.editMarkups)
	}
	if len(sink.calls) != 0 {
		t.Fatalf("rollback must require explicit confirmation before enqueue, got calls=%+v", sink.calls)
	}
}

func TestRouterRoutesOpen_UsesCachedSnapshotOnlyWhileRefreshingLiveStatus(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
	cache := &RoutesCache{TTL: time.Minute}
	cache.Put(uid, wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{{ID: "old", Name: "old-tunnel", Iface: "nwg-old"}},
		Counts:  map[string]wire.TunnelCounts{"old": {DNS: 1}},
	})
	r.SetRoutesCache(cache)

	tid := int64(11)
	q := &tg.CallbackQuery{
		ID:   "cb-routes-open",
		From: tg.User{ID: 12345},
		Message: tg.Message{
			MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
		},
		Data: fmt.Sprintf("routes_open:%d:_panel_", uid),
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) == 0 || !strings.Contains(f.edits[0], "old-tunnel") {
		t.Fatalf("cached snapshot should render as temporary panel, edits=%v", f.edits)
	}
	if !strings.Contains(f.edits[0], "обнов") {
		t.Fatalf("temporary cached panel should say it is refreshing live data, got %q", f.edits[0])
	}
	if len(f.editMarkups) == 0 || markupHasCallbackPrefix(f.editMarkups[0], fmt.Sprintf("routes_rebind:%d:", uid)) ||
		markupHasCallbackPrefix(f.editMarkups[0], fmt.Sprintf("routes_add:%d:", uid)) ||
		markupHasCallbackPrefix(f.editMarkups[0], fmt.Sprintf("routes_del:%d:", uid)) {
		t.Fatalf("temporary cached panel must not expose route mutation buttons, markup=%#v", f.editMarkups)
	}
	if len(sink.calls) != 1 || sink.calls[0].action != "route_status" {
		t.Fatalf("routes_open must enqueue live route_status even with cache, got %+v", sink.calls)
	}
}

// Обновление пакетов и отключение фида переехали в мини-апп. Старые кнопки в
// истории чата отвечают «неизвестная кнопка» и ничего не ставят в очередь.
func TestRouterOpkgCallbacksRemoved(t *testing.T) {
	d, uid := newTestDB(t)
	for _, data := range []string{
		"opkg_upgrade:" + itoa(uid) + ":_menu",
		"opkg_disable:" + itoa(uid) + ":_menu:abcd1234",
		"opkg_disable_confirm:" + itoa(uid) + ":_menu:abcd1234",
	} {
		f := &fakeRouterTG{}
		sink := &fakeEnqueuer{}
		r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
		r.HandleCallback(context.Background(), &tg.CallbackQuery{
			ID:      "cbk-opkg",
			From:    tg.User{ID: 12345},
			Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "panel"},
			Data:    data,
		})
		if len(sink.calls) != 0 || len(f.edits) != 0 {
			t.Errorf("%s: calls=%+v edits=%v", data, sink.calls, f.edits)
		}
		if len(f.answers) != 1 || f.answers[0] != "неизвестная кнопка" {
			t.Errorf("%s: answers=%v, ожидалась «неизвестная кнопка»", data, f.answers)
		}
	}
}

func TestRouterOptionalPanelActionsNilActionToast(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, nil, Config{ChatID: -100, AdminUserID: 12345})

	for _, data := range []string{
		fmt.Sprintf("pingcheck_open:%d:_panel_", uid),
		fmt.Sprintf("diag_test:%d:abcd1234:mtu", uid),
	} {
		t.Run(data, func(t *testing.T) {
			f.answers = nil
			r.HandleCallback(context.Background(), &tg.CallbackQuery{
				ID:      "optional-action",
				From:    tg.User{ID: 12345},
				Data:    data,
				Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 1},
			})
			if len(f.answers) != 1 {
				t.Fatalf("expected 1 AnswerCallbackQuery, got %d", len(f.answers))
			}
			if !strings.Contains(f.answers[0], "not configured") {
				t.Fatalf("expected not configured toast, got %q", f.answers[0])
			}
		})
	}
}

func TestCallbackUserFacingNotFoundToastsAreRussian(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 12345})

	cases := []struct {
		name string
		run  func(*tg.CallbackQuery, Args)
		args Args
	}{
		{
			name: "acl missing router",
			run: func(q *tg.CallbackQuery, args Args) {
				r.aclAllow(context.Background(), q, args)
			},
			args: Args{UserID: 999999},
		},
		{
			name: "routes open missing user",
			run: func(q *tg.CallbackQuery, args Args) {
				r.handleRoutesOpen(context.Background(), q, args, false)
			},
			args: Args{UserID: 999999},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.answers = nil
			q := &tg.CallbackQuery{
				ID:      "q-" + tc.name,
				From:    tg.User{ID: 12345},
				Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 1},
			}
			tc.run(q, tc.args)
			if len(f.answers) != 1 {
				t.Fatalf("expected one toast, got %d: %+v", len(f.answers), f.answers)
			}
			assertNoEnglishUserFacingCopy(t, f.answers[0])
			if !strings.Contains(f.answers[0], "роутер") {
				t.Fatalf("toast should explain missing router in Russian, got %q", f.answers[0])
			}
		})
	}
}

func TestAclAllow_OperatorAllowed(t *testing.T) {
	d, _ := newTestDB(t)
	uid, err := d.Users().Insert("router-x", "tok-x", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Owner is bound to TG user 100; verify TG user 200 (operator) is also allowed.
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 999)

	r := NewRouterWithSink(d, &fakeRouterTG{}, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	q := &tg.CallbackQuery{ID: "q", From: tg.User{ID: 200}, Message: tg.Message{Chat: tg.Chat{ID: 7}}}

	if !r.aclAllow(context.Background(), q, Args{UserID: uid}) {
		t.Error("operator (TG 200) should be allowed for router uid")
	}
}

func TestAclAllow_FormerOperatorDenied(t *testing.T) {
	d, _ := newTestDB(t)
	uid, _ := d.Users().Insert("router-y", "tok-y", "1.1.1.1", "awg11")
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 999)
	_ = d.RouterOperators().Remove(uid, 200)

	r := NewRouterWithSink(d, &fakeRouterTG{}, &fakeEnqueuer{}, Config{ChatID: 7, AdminUserID: 42})
	q := &tg.CallbackQuery{ID: "q", From: tg.User{ID: 200}, Message: tg.Message{Chat: tg.Chat{ID: 7}}}

	if r.aclAllow(context.Background(), q, Args{UserID: uid}) {
		t.Error("removed operator (TG 200) must not be allowed")
	}
}

// Operator (non-admin, listed in router_operators) taps a reply-keyboard
// button in the router's per_router topic. Must pass the admin-gate in
// HandleMessage and reach the dispatch, mirroring the admin path.
// Regression for the rc25 gap where operators could tap inline buttons but
// not the reply-keyboard entries.
func TestRouterHandleMessage_OperatorReplyKeyboard_InOwnTopic(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	if err := d.RouterOperators().Add(uid, 200, 42); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42})

	tid := int64(55)
	r.HandleMessage(context.Background(), &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 200},
		MessageThreadID: &tid,
		Text:            "🩺 Проверка",
	})

	if len(f.sentMsgs) != 1 {
		t.Fatalf("operator should reach doctor dispatch; got sentMsgs=%d %v", len(f.sentMsgs), f.sentMsgs)
	}
	if len(sink.calls) != 1 || sink.calls[0].action != "router_doctor" {
		t.Errorf("expected router_doctor enqueue, got %+v", sink.calls)
	}
}

// Operator for router A taps the reply-keyboard in router B's topic. Must
// be dropped — operators only have access to their own router's topic.
func TestRouterHandleMessage_OperatorBlocked_DifferentRouterTopic(t *testing.T) {
	d, uidA := newTestDB(t)
	// Router A: operator 200 is whitelisted.
	if err := d.Users().UpdateThreadID(uidA, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uidA, 100)
	_ = d.RouterOperators().Add(uidA, 200, 42)

	// Router B: different router, different topic, operator 200 is NOT in its whitelist.
	uidB, err := d.Users().Insert("router-b", "tok-b", "2.2.2.2", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(uidB, 66); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uidB, 101)

	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42})

	tid := int64(66) // router B's topic
	msg := &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 200}, // operator of A, not B
		MessageThreadID: &tid,
		Text:            "🩺 Проверка",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 0 || len(sink.calls) != 0 {
		t.Errorf("operator must be dropped in foreign router topic; sentMsgs=%v calls=%+v",
			f.sentMsgs, sink.calls)
	}
}

// Operator types an admin-only slash command in their topic. Must be
// dropped — slash commands stay admin-only.
func TestRouterHandleMessage_OperatorBlocked_AdminSlashCommand(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 42})

	tid := int64(55)
	msg := &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 200},
		MessageThreadID: &tid,
		Text:            "/ensure_topics",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 0 {
		t.Errorf("operator slash command must be dropped, got sentMsgs=%v", f.sentMsgs)
	}
}

func TestRouterHandleMessage_OperatorFleetButtonsBlockedInOwnTopic(t *testing.T) {
	for _, text := range []string{"📋 Список юзеров", "📊 Здоровье флота"} {
		t.Run(text, func(t *testing.T) {
			d, uid := newTestDB(t)
			if err := d.Users().UpdateThreadID(uid, 55); err != nil {
				t.Fatal(err)
			}
			_ = d.Users().SetTelegramUserID(uid, 100)
			_ = d.RouterOperators().Add(uid, 200, 42)

			f := &fakeRouterTG{}
			sink := &fakeEnqueuer{}
			r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42})

			tid := int64(55)
			r.HandleMessage(context.Background(), &tg.Message{
				MessageID:       99,
				Chat:            tg.Chat{ID: -100},
				From:            tg.User{ID: 200},
				MessageThreadID: &tid,
				Text:            text,
			})

			if len(f.sentMsgs) != 0 || len(sink.calls) != 0 {
				t.Fatalf("operator must not access fleet-wide button %q from router topic; sent=%v calls=%+v", text, f.sentMsgs, sink.calls)
			}
		})
	}
}

func TestRouterHandleMessage_OperatorStatusSlash_InOwnTopic(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 42})

	tid := int64(55)
	r.HandleMessage(context.Background(), &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 200},
		MessageThreadID: &tid,
		Text:            "/status@wgmonitor_bot",
	})

	if len(f.sentMsgs) != 1 {
		t.Fatalf("operator /status should render smart reply, got %d sends", len(f.sentMsgs))
	}
	if !strings.Contains(f.sentMsgs[0], "ещё не отчитывался") {
		t.Fatalf("operator /status should use smart reply path, got %q", f.sentMsgs[0])
	}
}

func TestRouterHandleMessage_OperatorTunnelsSlash_InOwnTopic(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42})

	tid := int64(55)
	r.HandleMessage(context.Background(), &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 200},
		MessageThreadID: &tid,
		Text:            "/tunnels",
	})

	if len(f.sentMsgs) != 1 {
		t.Fatalf("operator /tunnels should send loading panel, got %d sends", len(f.sentMsgs))
	}
	if !strings.Contains(f.sentMsgs[0], "Туннели") || !strings.Contains(f.sentMsgs[0], "читаю") {
		t.Fatalf("operator /tunnels should use live loading path, got %q", f.sentMsgs[0])
	}
	if len(sink.calls) != 1 || sink.calls[0].action != "tunnels_status" {
		t.Fatalf("operator /tunnels should enqueue tunnels_status, got %+v", sink.calls)
	}
}

func TestRouterHandleMessage_OperatorRoutesSlash_InOwnTopic(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42})

	tid := int64(55)
	r.HandleMessage(context.Background(), &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 200},
		MessageThreadID: &tid,
		Text:            "/routes",
	})

	if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], "Маршруты") {
		t.Fatalf("operator /routes should render routes loading message, got %#v", f.sentMsgs)
	}
	if len(sink.calls) != 1 || sink.calls[0].action != "route_status" {
		t.Fatalf("operator /routes should enqueue route_status, got %+v", sink.calls)
	}
}

func TestRouterHandleMessage_OperatorUpgradeSlashIgnored(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42})

	tid := int64(55)
	r.HandleMessage(context.Background(), &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 200},
		MessageThreadID: &tid,
		Text:            "/upgrade",
	})
	if len(sink.calls) != 0 || len(f.sentMsgs) != 0 || len(f.sentMarkups) != 0 {
		t.Fatalf("/upgrade переехал в приложение и должен молчать: calls=%+v msgs=%v", sink.calls, f.sentMsgs)
	}
}

func TestRouterHandleMessage_OperatorKeyboardCommandWithBotSuffix(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTGFull{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 42})

	tid := int64(55)
	msg := &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 200},
		MessageThreadID: &tid,
		Text:            "/keyboard@wgmonitor_bot",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.rkSends) != 2 {
		t.Fatalf("operator /keyboard@botname should re-push bottom and visible menus, got %d sends", len(f.rkSends))
	}
	for i, sent := range f.rkSends {
		if sent.thread == nil || *sent.thread != tid {
			t.Fatalf("keyboard #%d must be sent to the operator's router topic, got %+v", i, sent.thread)
		}
	}
	if _, ok := f.rkSends[0].markup.(*tg.ReplyKeyboardMarkup); !ok {
		t.Fatalf("/keyboard must first bind the bottom reply keyboard, markup=%T %+v", f.rkSends[0].markup, f.rkSends[0].markup)
	}
	kb, ok := f.rkSends[1].markup.(*tg.InlineKeyboardMarkup)
	if !ok || !keyboardContainsCallback(kb, "compat_btn:0:tunnels") || keyboardContainsCallback(kb, "compat_btn:0:amnezia_premium") || keyboardContainsCallback(kb, "compat_btn:0:hidemyname") {
		t.Fatalf("/keyboard must also send the full visible operator menu, markup=%T %+v", f.rkSends[1].markup, f.rkSends[1].markup)
	}
}

func TestRouterCompatViaTunnelDispatchesViaTunnelCheck(t *testing.T) {
	d, uid := newTestDB(t)
	tid := int64(55)
	if err := d.Users().UpdateThreadID(uid, tid); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().SetTelegramUserID(uid, 100); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(uid, 200, 42); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42})

	q := &tg.CallbackQuery{
		ID:      "compat-via",
		From:    tg.User{ID: 200},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid, Text: "menu"},
		Data:    "compat_btn:0:via_tunnel",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 {
		t.Fatalf("expected one command, got %+v", sink.calls)
	}
	if sink.calls[0].userID != uid || sink.calls[0].action != "check_via_tunnel" {
		t.Fatalf("compat via_tunnel dispatched wrong command: %+v", sink.calls[0])
	}
}

func TestRouter_DiagRaw_ServesCachedBody(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 12345})
	tok := r.DiagCache().Put("RAW_BODY", time.Minute)

	q := &tg.CallbackQuery{
		ID:      "cbk",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "diag_raw:" + itoa(uid) + ":_panel_:" + tok,
	}
	r.HandleCallback(context.Background(), q)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 sent raw-report message, got %d", len(f.sentMsgs))
	}
	if !strings.Contains(f.sentMsgs[0], "RAW_BODY") {
		t.Errorf("raw body missing from sent message: %s", f.sentMsgs[0])
	}
	if len(f.sentMarkups) != 1 {
		t.Fatalf("raw report should include next-action keyboard, got %d markups", len(f.sentMarkups))
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("raw report keyboard should be inline, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"diag_back:" + itoa(uid) + ":_panel_:" + tok,
		"diag_now:" + itoa(uid) + ":_menu",
		"router_doctor:" + itoa(uid) + ":_menu",
	} {
		if !markupHasCallback(kb, want) {
			t.Fatalf("raw report keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestRouter_DiagRaw_ExpiredTokenAnswersToast(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:      "cbk",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "diag_raw:" + itoa(uid) + ":_panel_:deadbeef", // never staged
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "уже не доступен") {
		t.Errorf("expected expired-toast, got %v", f.answers)
	}
}

// Operator taps a reply-keyboard entry outside any router topic (e.g. in
// the summary topic or no topic). Must be dropped — operator scope is
// per_router topics only.
func TestRouterHandleMessage_OperatorBlocked_OutsideRouterTopic(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 42})

	// No MessageThreadID → resolveTopicKind == "unknown".
	msg := &tg.Message{
		MessageID: 99,
		Chat:      tg.Chat{ID: -100},
		From:      tg.User{ID: 200},
		Text:      "🩺 Проверка",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 0 {
		t.Errorf("operator outside per_router topic must be dropped, got %v", f.sentMsgs)
	}
}

// Кнопка под тревогой в личке владельца обязана работать: уведомления
// переехали туда, и у сообщения нет ни темы, ни чата роутера. Доступ при
// этом по-прежнему проверяется по человеку, а не по тому, что чат приватный.
func TestACL_OwnerPrivateChatAllowed(t *testing.T) {
	d, uid := newTestDB(t)
	const owner = int64(555001)
	const staleThread = int64(4242)
	// У роутера осталась тема с прежних времён -- именно она раньше и
	// отклоняла нажатия из лички.
	if err := d.Users().UpdateThreadID(uid, staleThread); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().SetTelegramUserID(uid, owner); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-dm-owner",
		From:    tg.User{ID: owner},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: owner}, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	if !r.aclAllow(context.Background(), q, Args{UserID: uid}) {
		t.Fatalf("нажатие из лички владельца отклонено, ответы: %v", f.answers)
	}
}

// Чужая личка остаётся закрытой: приватность чата сама по себе ничего не
// разрешает.
func TestACL_StrangerPrivateChatRejected(t *testing.T) {
	d, uid := newTestDB(t)
	const owner = int64(555001)
	const stranger = int64(777002)
	if err := d.Users().SetTelegramUserID(uid, owner); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-dm-stranger",
		From:    tg.User{ID: stranger},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: stranger}, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	if r.aclAllow(context.Background(), q, Args{UserID: uid}) {
		t.Fatal("нажатие постороннего из своей лички обязано быть отклонено")
	}
}
