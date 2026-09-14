package tg

import (
	"strings"
	"testing"
)

func findCallbackData(kb InlineKeyboardMarkup, want string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == want {
				return true
			}
		}
	}
	return false
}

func TestRestartConfirmText_Hrneo(t *testing.T) {
	text := RestartConfirmText("hrneo", "a1b2c3d4")
	for _, want := range []string{"⚠️", "HydraRoute Neo", "Код подтверждения: a1b2c3d4", "живёт 5 мин"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Обслуживание") {
		t.Errorf("панели обслуживания больше нет, а заголовок о ней говорит:\n%s", text)
	}
}

// Отмена больше не ведёт в удалённую панель: она просто закрывает сообщение.
func TestRestartConfirmKeyboard(t *testing.T) {
	kb := RestartConfirmKeyboard(42, "awgmgr", "deadbeef")
	if !findCallbackData(kb, "maint_confirm:42:awgmgr:deadbeef") {
		t.Error("missing confirm callback_data")
	}
	if !findCallbackData(kb, "close_panel:42:_panel_") {
		t.Error("missing cancel callback_data")
	}
}

func TestMiniAppRouterSettingsURL(t *testing.T) {
	if got := MiniAppRouterSettingsURL("https://wgm.example.com/", 42); got != "https://wgm.example.com/miniapp/?router=42&open=settings" {
		t.Errorf("got %q", got)
	}
	for _, base := range []string{"", "http://wgm.example.com"} {
		if got := MiniAppRouterSettingsURL(base, 42); got != "" {
			t.Errorf("base %q: got %q, хотим пусто", base, got)
		}
	}
}
