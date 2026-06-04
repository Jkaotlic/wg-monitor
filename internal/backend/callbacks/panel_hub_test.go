package callbacks

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
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
	if strings.TrimSpace(got.text) == "" {
		t.Errorf("hub text is empty")
	}
	kb, ok := got.markup.(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("markup not InlineKeyboardMarkup: %T", got.markup)
	}
	flatCallbacks := flattenKbCallbacks(kb)
	for _, want := range []string{"panel:0:audit_all", "panel:0:update_all_confirm", "panel:0:kind:status", "panel:0:kind:doctor", "panel:0:kind:tunnels", "panel:0:kind:routes", "panel:0:kind:pingcheck", "panel:0:kind:maint", "panel:0:doctor_all", "panel:0:awaken_confirm", "panel:0:help:operator", "panel:0:close"} {
		if !containsStr(flatCallbacks, want) {
			t.Errorf("hub kb missing callback %q (have %v)", want, flatCallbacks)
		}
	}
}

func TestPanelHome_UsesRegistryKeyboard(t *testing.T) {
	_, got := panelHomeMessage()
	want := tg.AdminPanelHomeKeyboard()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("panel home keyboard must come from registry\nwant: %+v\ngot:  %+v", want, got)
	}
}

func TestPanelHome_AdminDMAllowed(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	msg := &tg.Message{
		MessageID: 61, Chat: tg.Chat{ID: 12345}, From: tg.User{ID: 12345}, Text: "/panel",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.rkSends) != 1 {
		t.Fatalf("admin DM /panel should open hub; sends=%d", len(f.rkSends))
	}
	if f.rkSends[0].chatID != 12345 {
		t.Fatalf("panel should be sent to admin DM, got chat %d", f.rkSends[0].chatID)
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

func flattenKbCallbacks(kb *tg.InlineKeyboardMarkup) []string {
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.CallbackData)
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
	for _, want := range []string{"betak", "vasya", "gamma"} {
		if !strings.Contains(got, want) {
			t.Errorf("kind pick missing router %q: %s", want, got)
		}
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
	if !strings.Contains(f.edits[0], "add-user") {
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

func TestPanelPush_FromAdminDMSendsSmartReplyToGroupTopic(t *testing.T) {
	d, uid := newTestDB(t)
	const targetThread = int64(4242)
	if err := d.Users().UpdateThreadID(uid, targetThread); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "tunnel_amnezia_for_awg", "ok", `{"tunnel_name":"amnezia_for_awg","status":"ok"}`, time.Now()); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	q := &tg.CallbackQuery{
		ID:   "cb-push-dm",
		From: tg.User{ID: 12345},
		Data: fmt.Sprintf("panel:%d:push:status", uid),
		Message: tg.Message{
			Chat:      tg.Chat{ID: 12345},
			MessageID: 75,
		},
	}

	r.HandleCallback(context.Background(), q)

	var landedInGroupTopic bool
	for _, s := range f.rkSends {
		if s.chatID == -100 && s.thread != nil && *s.thread == targetThread && strings.Contains(s.text, "vasya") {
			landedInGroupTopic = true
			break
		}
	}
	if !landedInGroupTopic {
		t.Fatalf("admin DM panel push must publish to configured group topic; rkSends=%+v", f.rkSends)
	}
	if len(f.edits) == 0 {
		t.Fatalf("expected admin DM hub edit with result")
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
	q := &tg.CallbackQuery{ID: "cb-stale", From: tg.User{ID: 12345}, Data: fmt.Sprintf("panel:%d:push:status", uid), Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 76}}
	r.HandleCallback(context.Background(), q)
	var stale bool
	for _, e := range f.edits {
		if strings.Contains(e, "/ensure_topics") || strings.Contains(e, "vasya") {
			stale = true
			break
		}
	}
	if !stale {
		t.Errorf("expected stale-topic error in hub edit; got %v", f.edits)
	}
}

func TestPanelPush_TunnelsUsesLiveRefresh(t *testing.T) {
	d, uid := newTestDB(t)
	const targetThread = int64(4242)
	if err := d.Users().UpdateThreadID(uid, targetThread); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "tunnel_deleted", "ok", `{"tunnel_name":"deleted","interface":"nwg9"}`, time.Now()); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:      "cb-push-tunnels",
		From:    tg.User{ID: 12345},
		Data:    fmt.Sprintf("panel:%d:push:tunnels", uid),
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 75},
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 || sink.calls[0].action != "tunnels_status" {
		t.Fatalf("panel push tunnels should enqueue live tunnels_status, got %+v", sink.calls)
	}
	if len(f.rkSends) != 0 {
		t.Fatalf("panel push tunnels must not render event-history panel, got rkSends=%+v", f.rkSends)
	}
}

func TestPanelCallback_NonAdminRejected(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	q := &tg.CallbackQuery{ID: "cb-panel-non-admin", From: tg.User{ID: 200}, Data: "panel:0:home", Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 77}}
	r.HandleCallback(context.Background(), q)
	if len(f.answers) != 1 {
		t.Fatalf("want admin-only answer, got %+v", f.answers)
	}
	if len(f.edits) != 0 || len(f.rkSends) != 0 {
		t.Fatalf("non-admin panel callback must not mutate UI; edits=%v sends=%v", f.edits, f.rkSends)
	}
}

func TestPanelHelp_NonAdminAllowed(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	q := &tg.CallbackQuery{ID: "cb-panel-help", From: tg.User{ID: 200}, Data: "panel:0:help:premium", Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 77}}
	r.HandleCallback(context.Background(), q)
	if len(f.edits) != 1 {
		t.Fatalf("non-admin help should render, edits=%v answers=%v", f.edits, f.answers)
	}
	if !strings.Contains(f.edits[0], "HideMy.name") {
		t.Fatalf("premium help body not rendered: %s", f.edits[0])
	}
}

func TestPanelHelp_NonAdminNavigationStaysOperatorSafe(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	q := &tg.CallbackQuery{
		ID:      "cb-panel-help",
		From:    tg.User{ID: 200},
		Data:    "panel:0:help:pingcheck",
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 77, Text: "help"},
	}
	r.HandleCallback(context.Background(), q)

	if len(f.editMarkups) != 1 || f.editMarkups[0] == nil {
		t.Fatalf("non-admin help should render with a keyboard, markups=%+v", f.editMarkups)
	}
	callbacks := flattenKbCallbacks(f.editMarkups[0])
	for _, blocked := range []string{"panel:0:kind:pingcheck", "panel:0:home", "panel:0:close"} {
		if containsStr(callbacks, blocked) {
			t.Fatalf("non-admin help keyboard must not expose admin-only callback %q: %v", blocked, callbacks)
		}
	}
	if !containsStr(callbacks, "close_panel:0:_panel_") {
		t.Fatalf("non-admin help keyboard should expose operator-safe close_panel, got %v", callbacks)
	}

	f.answers = nil
	f.edits = nil
	f.editMarkups = nil
	q.ID = "cb-panel-help-close"
	q.Data = "close_panel:0:_panel_"
	r.HandleCallback(context.Background(), q)
	if len(f.answers) != 1 {
		t.Fatalf("safe close should answer once, got answers=%+v", f.answers)
	}
	if strings.Contains(strings.ToLower(f.answers[0]), "admin") || strings.Contains(f.answers[0], "Ð°Ð´Ð¼Ð¸Ð½") {
		t.Fatalf("safe close must not hit admin-only rejection, got %q", f.answers[0])
	}
	if len(f.edits) != 1 {
		t.Fatalf("safe close should clear the keyboard with one edit, got edits=%+v", f.edits)
	}
}

func TestPanelDoctorAll_EnqueuesAggregateBatch(t *testing.T) {
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

	if len(sink.calls) != 3 {
		t.Fatalf("want 3 queued doctor commands, got %d (%+v)", len(sink.calls), sink.calls)
	}
	for _, call := range sink.calls {
		if call.action != "router_doctor" {
			t.Errorf("queued action=%q, want router_doctor", call.action)
		}
	}
	if len(sink.refs) != 3 {
		t.Fatalf("want 3 message refs, got %d", len(sink.refs))
	}
	for _, ref := range sink.refs {
		if ref.chatID != -100 || ref.messageID != 78 || ref.bulkID == "" {
			t.Fatalf("doctor-all should attach refs to one batch admin message, got %+v", ref)
		}
	}
	if len(f.rkSends) != 0 {
		t.Fatalf("aggregate doctor-all should not spam router-topic acks, got %d", len(f.rkSends))
	}
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "queued: 3") || !strings.Contains(f.edits[0], "skipped: 0") {
		t.Fatalf("bad aggregate result edit: %v", f.edits)
	}
	if !strings.Contains(f.edits[0], "will update here") {
		t.Fatalf("doctor-all result should promise aggregate report refresh, got: %v", f.edits)
	}

}

func TestPanelUpdateAll_SkipsAgentsBelowSafeSelfUpdate(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateLastSeenAgentVersion(uid, "v0.13.0-rc32"); err != nil {
		t.Fatal(err)
	}
	oldID, err := d.Users().Insert("oldbox", "tok-old", "2.2.2.2", "nwg1")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(oldID, "v0.13.0-rc31"); err != nil {
		t.Fatal(err)
	}

	f := &fakeRouterTGFull{}
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 42, BackendVersion: "v0.13.0-rc40"})

	q := &tg.CallbackQuery{
		ID:      "cb-update-all",
		From:    tg.User{ID: 42},
		Data:    "panel:0:update_all_do",
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 79},
	}
	r.HandleCallback(context.Background(), q)

	if len(sink.calls) != 1 {
		t.Fatalf("want only one safe self_update queued, got %d (%+v)", len(sink.calls), sink.calls)
	}
	if sink.calls[0].action != "self_update" || sink.calls[0].userID != uid {
		t.Fatalf("queued wrong update: %+v", sink.calls[0])
	}
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "queued: 1") || !strings.Contains(f.edits[0], "skipped: 1") {
		t.Fatalf("bad update-all report: %v", f.edits)
	}
}

func TestPanelAwakenConfirm_ShowsCountOfTopics(t *testing.T) {
	d, uid := newTestDB(t)
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
	q := &tg.CallbackQuery{ID: "cb-aw-c", From: tg.User{ID: 12345}, Data: "panel:0:awaken_confirm", Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 80}}
	r.HandleCallback(context.Background(), q)
	if len(f.edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "2") {
		t.Errorf("expected affected topic count, got %q", f.edits[0])
	}
	if !strings.Contains(f.edits[0], "Без топика: 1") {
		t.Errorf("expected skipped no-topic count, got %q", f.edits[0])
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
	q := &tg.CallbackQuery{ID: "cb-aw-do", From: tg.User{ID: 12345}, Data: "panel:0:awaken_do", Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 81}}
	r.HandleCallback(context.Background(), q)
	if len(f.rkSends) != 4 {
		t.Errorf("want 4 welcome sends (bottom+visible for vasya+betak), got %d", len(f.rkSends))
	}
	if len(f.edits) == 0 || !strings.Contains(f.edits[len(f.edits)-1], "2") {
		t.Errorf("expected hub result mentioning count 2, got: %v", f.edits)
	}
	if len(f.edits) == 0 || !strings.Contains(f.edits[len(f.edits)-1], "Без топика: 1") {
		t.Errorf("expected hub result mentioning skipped no-topic count, got: %v", f.edits)
	}
}

func TestPanelClose_EditsToClosedText(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	q := &tg.CallbackQuery{ID: "cb-close", From: tg.User{ID: 12345}, Data: "panel:0:close", Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 85}}
	r.HandleCallback(context.Background(), q)
	if len(f.edits) != 1 {
		t.Errorf("expected close edit, got %v", f.edits)
	}
}

func TestPanelHub_HelpScreen_EditsBody(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	q := &tg.CallbackQuery{ID: "cbk", From: tg.User{ID: 12345}, Message: tg.Message{MessageID: 1, Chat: tg.Chat{ID: -100}}, Data: "panel:0:help:maint"}
	r.HandleCallback(context.Background(), q)
	if len(f.edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "hrneo") {
		t.Errorf("maint help body should mention hrneo:\n%s", f.edits[0])
	}
}

func TestPanelHub_OperatorHelpScreen_EditsBody(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	q := &tg.CallbackQuery{ID: "cbk", From: tg.User{ID: 12345}, Message: tg.Message{MessageID: 1, Chat: tg.Chat{ID: -100}}, Data: "panel:0:help:operator"}
	r.HandleCallback(context.Background(), q)
	if len(f.edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "Operator") || !strings.Contains(f.edits[0], "Premium") {
		t.Errorf("operator help body should be the full operator guide:\n%s", f.edits[0])
	}
}
