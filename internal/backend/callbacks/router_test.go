package callbacks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
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
func (f *fakeRouterTG) GetFile(_ context.Context, _ string) (string, error)              { return "", nil }
func (f *fakeRouterTG) DownloadFile(_ context.Context, _ string) ([]byte, error)         { return nil, nil }

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
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-anon",
		From:    tg.User{ID: 99999}, // not admin
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "🔴"},
		Data:    "silence:" + itoa(uid) + ":awg_handshake:1h",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Errorf("expected 1 edit (action applied), got %d", len(f.edits))
	}
}

func TestRouterRejectsOperatorSelfHostedAmneziaIssue(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.RouterOperators().Add(uid, 555, 12345); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{
		ChatID:      -100,
		AdminUserID: 12345,
		SelfHostedAmnezia: selfhostedamnezia.Config{
			Enabled: true, EndpointHost: "vpn.example.com", EndpointPort: 47567,
		},
	})

	q := &tg.CallbackQuery{
		ID:      "selfhosted-op",
		From:    tg.User{ID: 555},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "amz_selfhosted_issue:" + itoa(uid) + ":_panel_",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "админа") {
		t.Fatalf("answers = %+v, want admin-only rejection", f.answers)
	}
	if len(f.edits) != 0 {
		t.Fatalf("operator should not see self-hosted confirm edit: %+v", f.edits)
	}
	if len(sink.calls) != 0 {
		t.Fatalf("operator should not enqueue anything: %+v", sink.calls)
	}
}

func TestAdminSelfHostedCommandAddsProviderAndPanelShowsIt(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 11); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	storePath := t.TempDir() + "/selfhosted.json"
	r := NewRouter(d, f, Config{
		ChatID:      -100,
		AdminUserID: 12345,
		SelfHostedAmnezia: selfhostedamnezia.Config{
			StorePath: storePath,
		},
	})

	r.HandleMessage(context.Background(), &tg.Message{
		MessageID: 1,
		Chat:      tg.Chat{ID: -100},
		From:      tg.User{ID: 12345},
		Text:      "/selfhosted add home vpn.example.com 47567 Home VPS",
	})
	if len(f.sentMsgs) == 0 || !strings.Contains(f.sentMsgs[len(f.sentMsgs)-1], "home") {
		t.Fatalf("admin reply missing added provider: %+v", f.sentMsgs)
	}

	r.HandleMessage(context.Background(), &tg.Message{
		MessageID:       2,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 12345},
		MessageThreadID: ptrInt64(11),
		Text:            "Amnezia Premium",
	})
	if len(f.sentMarkups) == 0 {
		t.Fatal("expected Amnezia panel markup")
	}
	kb, ok := f.sentMarkups[len(f.sentMarkups)-1].(*tg.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("markup type = %T", f.sentMarkups[len(f.sentMarkups)-1])
	}
	var found bool
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.Text, "Home VPS") && btn.CallbackData == "amz_selfhosted_issue:"+itoa(uid)+":_panel_:home" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("self-hosted button not found in %+v", kb.InlineKeyboard)
	}
}

func TestAdminAmneziaPanelShowsSelfHostedManageButtonBeforeProviders(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 11); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{
		ChatID:      -100,
		AdminUserID: 12345,
		SelfHostedAmnezia: selfhostedamnezia.Config{
			StorePath: t.TempDir() + "/selfhosted.json",
		},
	})

	r.HandleMessage(context.Background(), &tg.Message{
		MessageID:       1,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 12345},
		MessageThreadID: ptrInt64(11),
		Text:            "Amnezia Premium",
	})

	if len(f.sentMarkups) == 0 {
		t.Fatal("expected Amnezia panel markup")
	}
	kb, ok := f.sentMarkups[len(f.sentMarkups)-1].(*tg.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("markup type = %T", f.sentMarkups[len(f.sentMarkups)-1])
	}
	if !keyboardContainsCallback(kb, "amz_selfhosted_manage:"+itoa(uid)+":_panel_") {
		t.Fatalf("self-hosted manage button not found in %+v", kb.InlineKeyboard)
	}
}

func TestAdminSelfHostedPanelAddsProviderFromButtonFlow(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 11); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	storePath := t.TempDir() + "/selfhosted.json"
	r := NewRouter(d, f, Config{
		ChatID:      -100,
		AdminUserID: 12345,
		SelfHostedAmnezia: selfhostedamnezia.Config{
			StorePath: storePath,
		},
	})

	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:      "selfhosted-add",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, MessageThreadID: ptrInt64(11)},
		Data:    "amz_selfhosted_add:" + itoa(uid) + ":_panel_",
	})
	r.HandleMessage(context.Background(), &tg.Message{
		MessageID:       8,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 12345},
		MessageThreadID: ptrInt64(11),
		Text:            "home vpn.example.com 47567 Home VPS",
	})

	store, err := selfhostedamnezia.LoadStore(storePath, selfhostedamnezia.Config{})
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := store.Get("home")
	if !ok || !inst.Enabled || inst.EndpointHost != "vpn.example.com" || inst.EndpointPort != 47567 {
		t.Fatalf("bad stored instance: %+v ok=%v", inst, ok)
	}
	if len(f.editMarkups) == 0 {
		t.Fatal("expected prompt/panel edits")
	}
	got := f.editMarkups[len(f.editMarkups)-1]
	if !keyboardContainsCallback(got, "amz_selfhosted_issue:"+itoa(uid)+":_panel_:home") {
		t.Fatalf("stored provider issue button not found in %+v", got.InlineKeyboard)
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
		ID:      "cbk-restart",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "🔴 alert"},
		Data:    "restart_tunnel:" + itoa(uid) + ":tunnel_amnezia_for_awg2",
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.calls))
	}
	if sink.calls[0].action != "restart_tunnel" || sink.calls[0].userID != uid {
		t.Errorf("got %+v", sink.calls[0])
	}
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "очередь") {
		t.Errorf("expected edit containing 'очередь', got %v", f.edits)
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

	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "Удалить тоннель") {
		t.Fatalf("expected delete confirmation edit, got %v", f.edits)
	}
	if len(f.editMarkups) != 1 || !markupHasCallback(f.editMarkups[0], "tunnel_delete:"+itoa(uid)+":tunnel_awg13:Wireguard3") {
		t.Fatalf("confirm markup missing tunnel_delete callback: %+v", f.editMarkups)
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

	if len(f.edits) != 1 || strings.Contains(f.edits[0], "Удалить тоннель") {
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
	if !strings.Contains(body, "🔴") || !strings.Contains(body, "DNS-") {
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

// TestRouterHandleMessage_MaintButton verifies that tapping "🛠 Обслуживание"
// in a per_router thread sends a loading placeholder and enqueues version_audit.
func TestRouterHandleMessage_MaintButton(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	tid := int64(55)
	msg := &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 12345},
		MessageThreadID: &tid,
		Text:            "🛠 Обслуживание",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 loading message sent, got %d: %v", len(f.sentMsgs), f.sentMsgs)
	}
	if !strings.Contains(f.sentMsgs[0], "Обслуживание") {
		t.Errorf("loading text missing 'Обслуживание': %q", f.sentMsgs[0])
	}
	if len(sink.calls) != 1 {
		t.Fatalf("want 1 enqueue (version_audit), got %d", len(sink.calls))
	}
	if sink.calls[0].action != "version_audit" {
		t.Errorf("enqueued action=%q, want version_audit", sink.calls[0].action)
	}
	if sink.calls[0].userID != uid {
		t.Errorf("enqueued userID=%d, want %d", sink.calls[0].userID, uid)
	}
}

// TestRouterHandleMessage_MaintButton_WrongTopic verifies that the maint
// button in a non-per_router thread sends an error message instead.
func TestRouterHandleMessage_MaintButton_WrongTopic(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, nil, Config{ChatID: -100, AdminUserID: 12345})

	// MessageThreadID nil → resolves to "unknown"
	msg := &tg.Message{
		MessageID: 10,
		Chat:      tg.Chat{ID: -100},
		From:      tg.User{ID: 12345},
		Text:      "🛠 Обслуживание",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 error message, got %d", len(f.sentMsgs))
	}
	if !strings.Contains(f.sentMsgs[0], "топике пользователя") {
		t.Errorf("error message missing expected text: %q", f.sentMsgs[0])
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

// seedUser creates a user and returns their ID. Mirrors newTestDB but for
// routers that already have a DB.
func seedUser(t *testing.T, d *db.DB, nick string) int64 {
	t.Helper()
	uid, err := d.Users().Insert(nick, "tok-"+nick, "1.2.3.4", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	return uid
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

func TestRouterHandleCallback_MaintClose_ClearsKeyboard(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := makeCBQ(fmt.Sprintf("maint_close:%d:_panel_", uid))
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 || f.answers[0] != "закрыто" {
		t.Errorf("expected answer='закрыто', got %v", f.answers)
	}
	if len(f.edits) != 1 {
		t.Fatalf("expected 1 edit (empty keyboard), got %d", len(f.edits))
	}
	// text is preserved (the original message text)
	if f.edits[0] != "panel text" {
		t.Errorf("maint_close should preserve original text, got %q", f.edits[0])
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
	if !strings.Contains(f.edits[0], "HydraRoute-Neo") {
		t.Errorf("confirm screen should mention HydraRoute-Neo, got: %q", f.edits[0])
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

func TestRouterHandleCallback_MaintRestart_Router_CooldownBlocks(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	// Pre-set cooldown.
	r.cooldown.set(uid, "router_reboot", 5*time.Minute)

	q := makeCBQ(fmt.Sprintf("maint_restart:%d:router", uid))
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "кулдаун") {
		t.Errorf("expected cooldown toast, got %v", f.answers)
	}
	// No confirm screen should be shown.
	if len(f.edits) != 0 {
		t.Errorf("cooldown path must NOT edit message, got %d edits", len(f.edits))
	}
	// No pendingMaint entry.
	r.pendingMaint.mu.Lock()
	count := len(r.pendingMaint.m)
	r.pendingMaint.mu.Unlock()
	if count != 0 {
		t.Errorf("no pendingMaint should be created when blocked by cooldown, got %d", count)
	}
}

func TestRouterHandleCallback_MaintFwOpen_FromCache(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	// Pre-seed firmware status in the audit cache.
	r.auditCache.PutFirmwareStatus(uid, wire.FirmwareStatus{Current: "5.0.1"})

	q := makeCBQ(fmt.Sprintf("maint_fw_open:%d:_panel_", uid))
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Fatalf("expected 1 edit (firmware screen), got %d", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "📦 Прошивка") {
		t.Errorf("firmware screen missing header, got: %q", f.edits[0])
	}
	if !strings.Contains(f.edits[0], "5.0.1") {
		t.Errorf("firmware screen missing version 5.0.1, got: %q", f.edits[0])
	}
}

func TestRouterHandleCallback_MaintFwOpen_NoCacheTriggersFwCheck(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	// No cache pre-seed — should fall through to handleMaintFwCheck.
	q := makeCBQ(fmt.Sprintf("maint_fw_open:%d:_panel_", uid))
	r.HandleCallback(context.Background(), q)

	if len(sink.refs) != 1 {
		t.Fatalf("expected 1 EnqueueWithRef call (firmware_status), got %d", len(sink.refs))
	}
	if len(sink.calls) != 1 || sink.calls[0].action != "firmware_status" {
		t.Errorf("expected firmware_status command, got %v", sink.calls)
	}
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "📦 Прошивка") {
		t.Errorf("expected loading edit with 📦 Прошивка, got %v", f.edits)
	}
}

func TestRouterHandleCallback_MaintOpen_EnqueueFailRestoresRecoveryKeyboard(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{err: fmt.Errorf("queue down")}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	q := makeCBQ(fmt.Sprintf("maint_open:%d:_panel_", uid))
	r.HandleCallback(context.Background(), q)

	if len(f.edits) < 2 {
		t.Fatalf("expected loading and recovery edits, got %d: %v", len(f.edits), f.edits)
	}
	lastText := f.edits[len(f.edits)-1]
	if !strings.Contains(lastText, "Обслуживание") && !strings.Contains(lastText, "ÐžÐ±ÑÐ»ÑƒÐ¶Ð¸Ð²Ð°Ð½Ð¸Ðµ") {
		t.Fatalf("recovery edit should mention maintenance, got %q", lastText)
	}
	lastKB := f.editMarkups[len(f.editMarkups)-1]
	for _, want := range []string{
		fmt.Sprintf("maint_open:%d:_panel_", uid),
		fmt.Sprintf("router_doctor:%d:_menu", uid),
		fmt.Sprintf("tunnels_refresh:%d:_panel_", uid),
	} {
		if !markupHasCallback(lastKB, want) {
			t.Fatalf("recovery keyboard missing %q: %+v", want, lastKB)
		}
	}
}

func TestRouterHandleCallback_MaintFwCheck_EnqueueFailRestoresRecoveryKeyboard(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{err: fmt.Errorf("queue down")}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	q := makeCBQ(fmt.Sprintf("maint_fw_check:%d:_panel_", uid))
	r.HandleCallback(context.Background(), q)

	if len(f.edits) < 2 {
		t.Fatalf("expected loading and recovery edits, got %d: %v", len(f.edits), f.edits)
	}
	lastText := f.edits[len(f.edits)-1]
	if !strings.Contains(lastText, "Прошивка") && !strings.Contains(lastText, "ÐŸÑ€Ð¾ÑˆÐ¸Ð²ÐºÐ°") {
		t.Fatalf("recovery edit should mention firmware, got %q", lastText)
	}
	lastKB := f.editMarkups[len(f.editMarkups)-1]
	for _, want := range []string{
		fmt.Sprintf("maint_fw_check:%d:_panel_", uid),
		fmt.Sprintf("maint_open:%d:_panel_", uid),
		fmt.Sprintf("router_doctor:%d:_menu", uid),
	} {
		if !markupHasCallback(lastKB, want) {
			t.Fatalf("firmware recovery keyboard missing %q: %+v", want, lastKB)
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

// ACL gate: bound owner can tap callbacks targeting their own router.
func TestACL_AllowsBoundOwner(t *testing.T) {
	d, uid := newTestDB(t)
	const owner = int64(11111)
	if err := d.Users().SetTelegramUserID(uid, owner); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	q := &tg.CallbackQuery{
		ID:      "cbk-acl-2",
		From:    tg.User{ID: owner},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}, Text: "🔴"},
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

// TestRouter_OpkgDisable_DispatchesEnqueue verifies the full dispatch path for
// the opkg_disable callback: SetOpkgRepair wires the action, a tap by the
// admin on a valid token enqueues opkg_feed_disable and returns a toast.
func TestRouter_OpkgDisable_DispatchesEnqueue(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	store := newPendingOpkgRepairStore()
	store.put(&pendingOpkgRepair{
		UserID:    uid,
		URL:       "https://anonym-tsk.github.io/nfqws-keenetic/all",
		Token:     "tok1",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	r.SetOpkgRepair(store, NewOpkgRepairAction(sink, store, func() string { return "cmd-1" }))

	// opkg_disable:<uid>:_menu:<token> — _menu suffix sets IsMenu=true so the
	// router answers with the status toast and does NOT edit the message.
	q := &tg.CallbackQuery{
		ID:   "q1",
		From: tg.User{ID: 12345},
		// AdminUserID=12345 passes ACL; uid stored in the pending entry.
		Data:    fmt.Sprintf("opkg_disable:%d:_menu:tok1", uid),
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 555},
	}
	r.HandleCallback(context.Background(), q)

	// The action must have enqueued exactly one opkg_feed_disable command.
	if len(sink.calls) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.calls))
	}
	if sink.calls[0].action != "opkg_feed_disable" {
		t.Errorf("enqueued action=%q, want opkg_feed_disable", sink.calls[0].action)
	}
	if sink.calls[0].userID != uid {
		t.Errorf("enqueued userID=%d, want %d", sink.calls[0].userID, uid)
	}
	// IsMenu path: AnswerCallbackQuery with the status toast; no EditMessageText.
	if len(f.answers) != 1 {
		t.Fatalf("expected 1 AnswerCallbackQuery, got %d", len(f.answers))
	}
	if !strings.Contains(f.answers[0], "фид") {
		t.Errorf("toast should mention фид, got %q", f.answers[0])
	}
	if len(f.edits) != 0 {
		t.Errorf("opkg_disable(_menu) must NOT edit the message, got %d edits", len(f.edits))
	}
}

// TestRouter_OpkgDisable_NilAction toasts "не настроен" when SetOpkgRepair
// has not been called (action is nil).
func TestRouter_OpkgDisable_NilAction(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, nil, Config{ChatID: -100, AdminUserID: 12345})
	// Do NOT call r.SetOpkgRepair — action stays nil.

	q := &tg.CallbackQuery{
		ID:      "q2",
		From:    tg.User{ID: 12345},
		Data:    fmt.Sprintf("opkg_disable:%d:_menu:sometoken", uid),
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 1},
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 {
		t.Fatalf("expected 1 AnswerCallbackQuery, got %d", len(f.answers))
	}
	if !strings.Contains(f.answers[0], "не настроен") {
		t.Errorf("expected 'не настроен' toast when action is nil, got %q", f.answers[0])
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
// HandleMessage and reach the maintenance-panel dispatch, mirroring the
// admin path. Regression for the rc25 gap where operators could tap inline
// buttons but not the reply-keyboard entries.
func TestRouterHandleMessage_OperatorReplyKeyboard_InOwnTopic(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	// Bind a different owner so this user is not the operator we test below.
	_ = d.Users().SetTelegramUserID(uid, 100)
	// Operator: TG 200 whitelisted for this router.
	if err := d.RouterOperators().Add(uid, 200, 42); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42})

	tid := int64(55)
	msg := &tg.Message{
		MessageID:       99,
		Chat:            tg.Chat{ID: -100},
		From:            tg.User{ID: 200},
		MessageThreadID: &tid,
		Text:            "🛠 Обслуживание",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("operator should reach maint dispatch; got sentMsgs=%d %v", len(f.sentMsgs), f.sentMsgs)
	}
	if !strings.Contains(f.sentMsgs[0], "Обслуживание") {
		t.Errorf("expected maint loading text, got %q", f.sentMsgs[0])
	}
	if len(sink.calls) != 1 || sink.calls[0].action != "version_audit" {
		t.Errorf("expected version_audit enqueue, got %+v", sink.calls)
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
		Text:            "🛠 Обслуживание",
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

	if len(f.rkSends) != 1 {
		t.Fatalf("operator /keyboard@botname should re-push keyboard, got %d sends", len(f.rkSends))
	}
	if f.rkSends[0].thread == nil || *f.rkSends[0].thread != tid {
		t.Fatalf("keyboard must be sent to the operator's router topic, got %+v", f.rkSends[0].thread)
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
		Text:      "🛠 Обслуживание",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 0 {
		t.Errorf("operator outside per_router topic must be dropped, got %v", f.sentMsgs)
	}
}
