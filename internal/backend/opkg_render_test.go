package backend

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/callbacks"
	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRenderOpkgUpgradeReply_WithFailedFeeds_AddsRepairButtons(t *testing.T) {
	store := callbacks.NewPendingOpkgRepairStore()
	payload, _ := json.Marshal(wire.OpkgUpgradeResult{
		FailedFeeds: []string{
			"https://dead.example/Packages.gz",
			"https://other.example/all/Packages.gz",
		},
	})
	res := wire.CommandResult{
		ID:      "c1",
		Status:  "ok",
		Output:  "✅ Все пакеты актуальны...",
		Payload: payload,
	}
	text, markup := renderOpkgUpgradeReply(res, 42, store, func() string { return "tok-fixed" })
	if !strings.Contains(text, "Все пакеты актуальны") {
		t.Errorf("text missing upgrade body: %q", text)
	}
	if markup == nil {
		t.Fatalf("markup should not be nil with FailedFeeds present")
	}
	if len(markup.InlineKeyboard) != 2 {
		t.Errorf("expected 2 rows (one per feed), got %d", len(markup.InlineKeyboard))
	}
	for _, row := range markup.InlineKeyboard {
		if len(row) != 1 {
			t.Errorf("row must have 1 button, got %d", len(row))
		}
		btn := row[0]
		if !strings.HasPrefix(btn.CallbackData, "opkg_disable:42:_menu:") {
			t.Errorf("callback_data=%q", btn.CallbackData)
		}
		if !strings.HasPrefix(btn.Text, "🔧 Отключить мёртвый фид") {
			t.Errorf("button text=%q", btn.Text)
		}
	}
}

func TestRenderOpkgUpgradeReply_NoFailedFeeds_NoButtons(t *testing.T) {
	store := callbacks.NewPendingOpkgRepairStore()
	res := wire.CommandResult{ID: "c1", Status: "ok", Output: "✅ ok"}
	_, markup := renderOpkgUpgradeReply(res, 42, store, func() string { return "tok" })
	if markup != nil {
		t.Errorf("markup should be nil with no FailedFeeds")
	}
}

func TestRenderOpkgUpgradeReply_EmptyFailedFeedsArray_NoButtons(t *testing.T) {
	store := callbacks.NewPendingOpkgRepairStore()
	payload, _ := json.Marshal(wire.OpkgUpgradeResult{Output: "ok"})
	res := wire.CommandResult{
		ID:      "c1",
		Status:  "ok",
		Output:  "✅ ok",
		Payload: payload,
	}
	_, markup := renderOpkgUpgradeReply(res, 42, store, func() string { return "tok" })
	if markup != nil {
		t.Errorf("markup should be nil with empty FailedFeeds array")
	}
}

func TestNormalizeFeedURLBackend(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://x.com/all/Packages.gz", "https://x.com/all"},
		{"https://x.com/all/", "https://x.com/all"},
		{"https://x.com/all", "https://x.com/all"},
	}
	for _, c := range cases {
		got := normalizeFeedURLBackend(c.in)
		if got != c.want {
			t.Errorf("normalizeFeedURLBackend(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHostFromURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://anonym-tsk.github.io/nfqws-keenetic/all", "anonym-tsk.github.io"},
		{"http://bin.entware.net:8080/", "bin.entware.net"},
		{"not-a-url", "not-a-url"},
		{"https://example.com", "example.com"},
	}
	for _, c := range cases {
		got := hostFromURL(c.in)
		if got != c.want {
			t.Errorf("hostFromURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
