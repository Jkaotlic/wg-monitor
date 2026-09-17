package tg

import (
	"strings"
	"testing"
)

// Кнопки под тревогами видит владелец в личке: словарь приложения, а не
// движка. «awg-manager», «HR-Neo» и «Дай отчёт» -- из старой панели бота.
func TestHardAlertKeyboardSpeaksToOwner(t *testing.T) {
	kb := AlertKeyboard(42, "tunnel_amnezia", "https://example.com/miniapp/?router=42")
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			for _, bad := range []string{"awg-manager", "HR-Neo", "Дай "} {
				if strings.Contains(b.Text, bad) {
					t.Errorf("кнопка %q говорит %q", b.Text, bad)
				}
			}
		}
	}
}
