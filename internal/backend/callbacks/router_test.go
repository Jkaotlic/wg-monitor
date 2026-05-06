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
	mu        sync.Mutex
	answers   []string
	edits     []string
	sentMsgs  []string
	sendErr   error
	answerErr error
	editErr   error
}

func (f *fakeRouterTG) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	f.sentMsgs = append(f.sentMsgs, text)
	return 1, f.sendErr
}
func (f *fakeRouterTG) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	f.mu.Lock(); defer f.mu.Unlock()
	f.answers = append(f.answers, text)
	return f.answerErr
}
func (f *fakeRouterTG) EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error {
	f.mu.Lock(); defer f.mu.Unlock()
	f.edits = append(f.edits, text)
	return f.editErr
}
func (f *fakeRouterTG) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tg.Update, error) {
	return nil, nil
}
func (f *fakeRouterTG) SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentMsgs = append(f.sentMsgs, text)
	return 1, nil
}
func (f *fakeRouterTG) DeleteMessage(ctx context.Context, chatID, messageID int64) error { return nil }
func (f *fakeRouterTG) GetFile(_ context.Context, _ string) (string, error)              { return "", nil }
func (f *fakeRouterTG) DownloadFile(_ context.Context, _ string) ([]byte, error)         { return nil, nil }

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

	if len(f.answers) != 1 { t.Errorf("expected 1 answer, got %d", len(f.answers)) }
	if len(f.edits) != 1 {
		t.Errorf("expected 1 edit, got %d", len(f.edits))
	} else if !strings.Contains(f.edits[0], "Silenced") {
		t.Errorf("edit text missing 'Silenced': %q", f.edits[0])
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
	if !strings.Contains(strings.ToLower(f.answers[0]), "unknown") {
		t.Errorf("expected 'unknown' in answer, got %q", f.answers[0])
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
	if !strings.Contains(strings.ToLower(f.answers[0]), "error") {
		t.Errorf("expected 'error' toast when sink is nil, got %q", f.answers[0])
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
	if len(f.answers) != 1 { t.Fatal("expected answer") }
	if !strings.Contains(f.answers[0], "error") {
		t.Errorf("expected 'error' in answer, got %q", f.answers[0])
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
	for _, want := range []string{"✅", "vasya", "amnezia", "12с", "15 ms"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestRouterDispatchSmartReply_SilencedIncidentDropsFromHard(t *testing.T) {
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
	// Silenced incident must drop from Hard view — expect OK template (✅) not 🔴.
	if strings.Contains(body, "🔴") {
		t.Errorf("silenced incident should NOT trigger Hard template: %s", body)
	}
	if !strings.Contains(body, "✅") {
		t.Errorf("expected OK template (✅) when only incident is silenced, got: %s", body)
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
