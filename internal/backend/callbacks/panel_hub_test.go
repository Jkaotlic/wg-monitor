package callbacks

import (
	"context"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/tg"
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
