package callbacks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
	for _, want := range []string{"🛠 Maintenance", "📦 Routes", "📊 Status", "🪄 Оживить топики", "✖ Закрыть"} {
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
		Data: "panel:0:kind:maint",
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
	if !strings.Contains(got, "Maintenance") {
		t.Errorf("kind pick header missing 'Maintenance': %s", got)
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
