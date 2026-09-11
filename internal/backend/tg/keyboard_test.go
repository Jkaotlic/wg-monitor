package tg

import (
	"testing"
)

// Под каждой тревогой владелец видит ровно две кнопки: открыть роутер в
// приложении и отложить эту тревогу на час. Все прежние командные кнопки
// (restart_tunnel/diag_now/pingcheck_now/force_recheck/maint_restart/ack/
// mute/history) с тревоги убраны -- разбираться владелец идёт в приложение.
func TestAlertKeyboardHasOnlyAppAndSilence(t *testing.T) {
	kb := AlertKeyboard(7, "tunnel_awg11", "https://example.com/miniapp/?router=7")
	if len(kb.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows (app + silence), got %d: %+v", len(kb.InlineKeyboard), kb.InlineKeyboard)
	}
	appRow := kb.InlineKeyboard[0]
	if len(appRow) != 1 {
		t.Fatalf("expected 1 button in app row, got %d", len(appRow))
	}
	if appRow[0].WebApp == nil || appRow[0].WebApp.URL != "https://example.com/miniapp/?router=7" {
		t.Fatalf("app button web_app = %+v, want url https://example.com/miniapp/?router=7", appRow[0].WebApp)
	}
	if appRow[0].CallbackData != "" {
		t.Errorf("app button must not carry callback_data, got %q", appRow[0].CallbackData)
	}
	silenceRow := kb.InlineKeyboard[1]
	if len(silenceRow) != 1 {
		t.Fatalf("expected 1 button in silence row, got %d", len(silenceRow))
	}
	if silenceRow[0].Text != "⏸ Тише на час" {
		t.Errorf("silence button text = %q, want %q", silenceRow[0].Text, "⏸ Тише на час")
	}
	if silenceRow[0].CallbackData != "silence:7:tunnel_awg11:1h" {
		t.Errorf("silence button callback_data = %q, want %q", silenceRow[0].CallbackData, "silence:7:tunnel_awg11:1h")
	}
}

func TestAlertKeyboardWithoutAppURLKeepsSilence(t *testing.T) {
	kb := AlertKeyboard(7, "tunnel_awg11", "")
	if len(kb.InlineKeyboard) != 1 {
		t.Fatalf("expected 1 row (silence only) when appURL empty, got %d: %+v", len(kb.InlineKeyboard), kb.InlineKeyboard)
	}
	row := kb.InlineKeyboard[0]
	if len(row) != 1 || row[0].CallbackData != "silence:7:tunnel_awg11:1h" {
		t.Fatalf("expected sole silence button, got %+v", row)
	}
}

// Самое длинное имя проверки, которое различает checkCategory -- ветка
// tunnel_* не в счёт, у неё имя произвольной длины, но категории с
// фиксированным именем ограничены "external_reach" (14 символов).
func TestAlertKeyboardCallbackFitsTelegramLimit(t *testing.T) {
	kb := AlertKeyboard(999999999, "external_reach", "https://example.com/miniapp/?router=999999999")
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if len(btn.CallbackData) > 64 {
				t.Errorf("callback_data exceeds TG 64-byte limit: %d bytes (%q)", len(btn.CallbackData), btn.CallbackData)
			}
		}
	}
}

func TestMiniAppRouterURLRequiresHTTPS(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"", ""},
		{"http://example.com", ""},
		{"https://example.com/", "https://example.com/miniapp/?router=7"},
	}
	for _, c := range cases {
		if got := MiniAppRouterURL(c.base, 7); got != c.want {
			t.Errorf("MiniAppRouterURL(%q, 7) = %q, want %q", c.base, got, c.want)
		}
	}
}
