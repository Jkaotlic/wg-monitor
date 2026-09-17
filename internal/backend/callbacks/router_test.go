package callbacks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
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
		for _, text := range []string{"/maint", "/upgrade", "🛠 Обслуживание", "⬆ Обновить пакеты", "/tunnels", "/routes"} {
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

// makeCBQ builds a minimal CallbackQuery for the given callback_data string.
func makeCBQ(data string) *tg.CallbackQuery {
	return &tg.CallbackQuery{
		ID:      "cbq-test",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 42, Chat: tg.Chat{ID: -100}, Text: "panel text"},
		Data:    data,
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
	if !ok || !keyboardContainsCallback(kb, "compat_btn:0:smart_reply") || keyboardContainsCallback(kb, "compat_btn:0:tunnels") || keyboardContainsCallback(kb, "compat_btn:0:amnezia_premium") || keyboardContainsCallback(kb, "compat_btn:0:hidemyname") {
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
