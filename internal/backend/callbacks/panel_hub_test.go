package callbacks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func TestPanelHome_RendersHubMessage(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := int64(99)
	msg := &tg.Message{
		MessageID: 60, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "/panel",
	}
	r.HandleMessage(context.Background(), msg)

	// Hub posts via SendMessageWithReplyKeyboard so it can carry inline kb.
	if len(f.rkSends) != 1 {
		t.Fatalf("want 1 send, got %d", len(f.rkSends))
	}
	got := f.rkSends[0]
	if !strings.Contains(got.text, "🎛 Панель управления") {
		t.Errorf("missing hub header: %s", got.text)
	}
	if !strings.Contains(got.text, "Что открыть?") {
		t.Errorf("missing hub prompt: %s", got.text)
	}
	kb, ok := got.markup.(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("markup not InlineKeyboardMarkup: %T", got.markup)
	}
	flatTexts := flattenKbTexts(kb)
	for _, want := range []string{"📊 Status", "🩺 Проверка", "🎛 Туннели", "🛣 Маршруты", "📡 PingCheck", "🛠 Обслуживание", "🩺 Все роутеры", "🪄 Оживить топики", "✖ Закрыть"} {
		if !containsStr(flatTexts, want) {
			t.Errorf("hub kb missing %q (have %v)", want, flatTexts)
		}
	}
}

func flattenKbTexts(kb *tg.InlineKeyboardMarkup) []string {
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.Text)
		}
	}
	return out
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestPanelKindPick_ListsRoutersWithThreadFlag(t *testing.T) {
	d, _ := newTestDB(t) // vasya, no thread
	// Add a second user WITH thread, a third WITHOUT thread.
	uid2, err := d.Users().Insert("betak", "tok-b", "2.2.2.2", "nwg1")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(uid2, 1234); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("gamma", "tok-g", "3.3.3.3", "nwg1"); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:   "cb-1",
		From: tg.User{ID: 12345},
		Data: "panel:0:kind:tunnels",
		Message: tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 70,
		},
	}
	r.HandleCallback(context.Background(), q)

	// Hub edited (not new send).
	if len(f.edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(f.edits))
	}
	got := f.edits[0]
	if !strings.Contains(got, "Туннели") {
		t.Errorf("kind pick header missing 'Туннели': %s", got)
	}
	for _, want := range []string{"betak", "vasya", "gamma"} {
		if !strings.Contains(got, want) {
			t.Errorf("kind pick missing router %q: %s", want, got)
		}
	}
	// Users without thread carry a warning marker.
	if !strings.Contains(got, "⚠") {
		t.Errorf("expected ⚠ marker for users without thread, got: %s", got)
	}
}

func TestPanelKindPick_NoUsersShowsEmptyState(t *testing.T) {
	d := newTestDBEmpty(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:   "cb-empty",
		From: tg.User{ID: 12345},
		Data: "panel:0:kind:routes",
		Message: tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 71,
		},
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "Роутеров нет") {
		t.Errorf("expected empty-state text, got: %s", f.edits[0])
	}
}

// newTestDBEmpty opens a fresh test DB without inserting any users.
// (Sibling helper to newTestDB which inserts a default "vasya".)
func newTestDBEmpty(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestPanelPush_StatusSendsSmartReplyToTargetThread(t *testing.T) {
	d, uid := newTestDB(t) // vasya, no thread by default
	const targetThread = int64(4242)
	if err := d.Users().UpdateThreadID(uid, targetThread); err != nil {
		t.Fatal(err)
	}
	// Smart-reply needs at least one event to avoid the "never reported" branch.
	if err := d.Events().Insert(uid, "tunnel_amnezia_for_awg", "ok", `{"tunnel_name":"amnezia_for_awg","status":"ok"}`, time.Now()); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:   "cb-push",
		From: tg.User{ID: 12345},
		Data: fmt.Sprintf("panel:%d:push:status", uid),
		Message: tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 75,
		},
	}
	r.HandleCallback(context.Background(), q)

	// At least one rkSend went into the target thread with the user's nickname.
	var landedInTarget bool
	for _, s := range f.rkSends {
		if s.thread != nil && *s.thread == targetThread && strings.Contains(s.text, "vasya") {
			landedInTarget = true
			break
		}
	}
	if !landedInTarget {
		t.Errorf("smart-reply did not land in target thread %d; rkSends=%+v", targetThread, f.rkSends)
	}
	// Hub edited to show result.
	if len(f.edits) == 0 {
		t.Errorf("expected hub edit with result, got 0 edits")
	}
}

func TestPanelPush_StaleTopicSurfacesError(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 555); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	f.sendErr = &tg.APIError{Method: "sendMessage", Description: "message thread not found", Code: 400}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:   "cb-stale",
		From: tg.User{ID: 12345},
		Data: fmt.Sprintf("panel:%d:push:status", uid),
		Message: tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 76,
		},
	}
	r.HandleCallback(context.Background(), q)

	// Hub edit should surface stale-topic guidance.
	var stale bool
	for _, e := range f.edits {
		if strings.Contains(e, "удалён") || strings.Contains(e, "не найден") {
			stale = true
			break
		}
	}
	if !stale {
		t.Errorf("expected stale-topic error in hub edit; got %v", f.edits)
	}
}

func TestPanelCallback_NonAdminRejected(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})

	q := &tg.CallbackQuery{
		ID:   "cb-panel-non-admin",
		From: tg.User{ID: 200},
		Data: "panel:0:home",
		Message: tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 77,
		},
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "админа") {
		t.Fatalf("want admin-only answer, got %+v", f.answers)
	}
	if len(f.edits) != 0 || len(f.rkSends) != 0 {
		t.Fatalf("non-admin panel callback must not mutate UI; edits=%v sends=%v", f.edits, f.rkSends)
	}
}

func TestPanelDoctorAll_EnqueuesRoutersWithTopics(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 101); err != nil {
		t.Fatal(err)
	}
	uid2, err := d.Users().Insert("betak", "tb", "2.2.2.2", "nwg1")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(uid2, 202); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("gamma", "tc", "3.3.3.3", "nwg1"); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42})

	q := &tg.CallbackQuery{
		ID:   "cb-doctor-all",
		From: tg.User{ID: 42},
		Data: "panel:0:doctor_all",
		Message: tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 78,
		},
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 2 {
		t.Fatalf("want 2 queued doctor commands, got %d (%+v)", len(sink.calls), sink.calls)
	}
	for _, call := range sink.calls {
		if call.action != "router_doctor" {
			t.Errorf("queued action=%q, want router_doctor", call.action)
		}
	}
	if len(f.rkSends) != 2 {
		t.Fatalf("want 2 ack messages in router topics, got %d", len(f.rkSends))
	}
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "Поставлено в очередь: 2") || !strings.Contains(f.edits[0], "Без топика: 1") {
		t.Fatalf("bad result edit: %v", f.edits)
	}
}

func TestPanelAwakenConfirm_ShowsCountOfTopics(t *testing.T) {
	d, uid := newTestDB(t) // vasya — no thread by default
	if err := d.Users().UpdateThreadID(uid, 100); err != nil {
		t.Fatal(err)
	}
	uid2, err := d.Users().Insert("betak", "tb", "2.2.2.2", "nwg1")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(uid2, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("gamma", "tc", "3.3.3.3", "nwg1"); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID: "cb-aw-c", From: tg.User{ID: 12345},
		Data:    "panel:0:awaken_confirm",
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 80},
	}
	r.HandleCallback(context.Background(), q)
	if len(f.edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "Будут затронуты: 2") {
		t.Errorf("expected 'Будут затронуты: 2 топика' (vasya+betak have thread), got %q", f.edits[0])
	}
}

func TestPanelAwakenDo_SendsWelcomeOnlyToUsersWithThread(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 100); err != nil {
		t.Fatal(err)
	}
	uid2, _ := d.Users().Insert("betak", "tb", "2.2.2.2", "nwg1")
	if err := d.Users().UpdateThreadID(uid2, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("gamma", "tc", "3.3.3.3", "nwg1"); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID: "cb-aw-do", From: tg.User{ID: 12345},
		Data:    "panel:0:awaken_do",
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 81},
	}
	r.HandleCallback(context.Background(), q)

	var welcomeCount int
	for _, s := range f.rkSends {
		if strings.HasPrefix(s.text, "👋 Топик роутера") {
			welcomeCount++
		}
	}
	if welcomeCount != 2 {
		t.Errorf("want 2 welcomes (vasya+betak), got %d", welcomeCount)
	}
	// Hub edit shows result with count.
	if len(f.edits) == 0 || !strings.Contains(f.edits[len(f.edits)-1], "Оживлено: 2") {
		t.Errorf("expected hub result mentioning 'Оживлено: 2', got: %v", f.edits)
	}
}

func TestPanelClose_EditsToClosedText(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID: "cb-close", From: tg.User{ID: 12345},
		Data:    "panel:0:close",
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 85},
	}
	r.HandleCallback(context.Background(), q)
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "закрыта") {
		t.Errorf("expected 'закрыта' in edit, got %v", f.edits)
	}
}

func TestPanelHub_HelpScreen_EditsBody(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:      "cbk",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 1, Chat: tg.Chat{ID: -100}},
		Data:    "panel:0:help:maint",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "Restart hrneo") {
		t.Errorf("maint help body should mention 'Restart hrneo':\n%s", f.edits[0])
	}
}
