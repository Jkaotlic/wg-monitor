package tg

import (
	"strings"
	"testing"
)

// Кнопки под тревогами видит владелец в личке: словарь приложения, а не
// движка. «awg-manager», «HR-Neo» и «Дай отчёт» -- из старой панели бота.
func TestHardAlertKeyboardSpeaksToOwner(t *testing.T) {
	kb := HardAlertKeyboard(42, "tunnel_amnezia", WithTunnelActions(), WithMobileActions(), WithHydraRouteActions())
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

// Подтверждение запуска и перезапуска HydraRoute открывается кнопками из-под
// тревоги в личке. Раньше оно говорило языком админа: «DNS-routes перестанут
// резолвиться», «ip.list», «демон».
func TestRestartConfirmText_HydraRouteSpeaksToOwner(t *testing.T) {
	for _, name := range []string{"hrneo", "hrneo_start"} {
		text := RestartConfirmText(name, "a1b2c3d4")
		for _, bad := range []string{"DNS-routes", "Static-routes", "ip.list", "демон", "HR-Neo", "HydraRoute-Neo", "резолв"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s: владелец читает %q:\n%s", name, bad, text)
			}
		}
		if !strings.Contains(text, "движок умной раздельной маршрутизации") {
			t.Errorf("%s: HydraRoute без пояснения:\n%s", name, text)
		}
	}
	// Кнопка «▶ Запустить HydraRoute Neo» открывала «Перезапустить …?»:
	// заголовок был один на все действия, и запуск звался перезапуском.
	if text := RestartConfirmText("hrneo_start", "a1b2c3d4"); !strings.Contains(text, "Запустить HydraRoute Neo?") ||
		strings.Contains(text, "Перезапустить") {
		t.Errorf("подтверждение запуска называет его перезапуском:\n%s", text)
	}
}
