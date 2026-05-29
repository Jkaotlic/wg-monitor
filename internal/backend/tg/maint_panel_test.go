package tg

import (
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// --- MaintPanelText / MaintPanelKeyboard ---

func TestMaintPanelText_FullState(t *testing.T) {
	args := MaintPanelArgs{
		Nickname:       "testkeen",
		HrneoVersion:   "2.4.0",
		HrneoInstalled: true,
		HrneoUptime:    "3д 4ч",
		HrneoRunning:   true,
		AwgmgrVersion:  "2.8.2",
		AwgmgrRunning:  true,
		AwgmgrUptime:   "7д 12ч",
		KeeneticOS:     "KN-1811",
		Firmware:       wire.FirmwareStatus{Current: "5.00.C.11.0-0"},
		Updates: []UpdateLine{
			{Name: "KeeneticOS", Installed: "5.00.C.11.0-0", Available: "5.00.C.12.0-0"},
			{Name: "awg-manager", Installed: "2.8.2", Available: "2.9.0"},
		},
	}
	text := MaintPanelText(args)
	for _, want := range []string{
		"🛠 Обслуживание — testkeen",
		"HydraRoute-Neo",
		"v2.4.0",
		"3д 4ч",
		"awg-manager",
		"v2.8.2",
		"7д 12ч",
		"KN-1811",
		"5.00.C.11.0-0",
		"🟡 Доступны обновления",
		"KeeneticOS 5.00.C.11.0-0 → 5.00.C.12.0-0",
		"awg-manager 2.8.2 → 2.9.0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "     ") {
		t.Errorf("maintenance text should not rely on wide space padding:\n%s", text)
	}
}

func TestMaintPanelText_RendersUpdateHint(t *testing.T) {
	args := MaintPanelArgs{
		Nickname:      "testkeen",
		AwgmgrVersion: "2.10.5",
		AwgmgrRunning: true,
		Firmware:      wire.FirmwareStatus{Current: "5.0.0"},
		Updates: []UpdateLine{
			{Name: "awg-manager", Installed: "2.10.5", Available: "2.10.7", Hint: "NativeWG update may need router reboot"},
		},
	}
	text := MaintPanelText(args)
	for _, want := range []string{"NativeWG", "router reboot"} {
		if !strings.Contains(text, want) {
			t.Errorf("update hint missing %q:\n%s", want, text)
		}
	}
}

func TestMaintPanelText_EmptyKeeneticOSHidesComma(t *testing.T) {
	args := MaintPanelArgs{
		Nickname:      "x",
		AwgmgrVersion: "2.8.2",
		Firmware:      wire.FirmwareStatus{Current: "5.0.0"},
		// KeeneticOS deliberately omitted
	}
	text := MaintPanelText(args)
	if strings.Contains(text, ", v5.0.0") {
		t.Errorf("empty KeeneticOS must not produce leading-comma artifact:\n%s", text)
	}
	if !strings.Contains(text, "Keenetic OS: v5.0.0") {
		t.Errorf("expected compact Keenetic OS line, got:\n%s", text)
	}
}

func TestMaintPanelText_HrneoNotInstalled(t *testing.T) {
	args := MaintPanelArgs{
		Nickname:       "testkeen",
		HrneoVersion:   "", // empty signals not installed
		HrneoInstalled: false,
		HrneoRunning:   false,
		AwgmgrVersion:  "2.8.2",
		AwgmgrRunning:  true,
		AwgmgrUptime:   "7д 12ч",
		KeeneticOS:     "KN-1811",
		Firmware:       wire.FirmwareStatus{Current: "5.0.0"},
	}
	text := MaintPanelText(args)
	if !strings.Contains(text, "не установлен") {
		t.Errorf("hrneo-not-installed line missing:\n%s", text)
	}
	if strings.Contains(text, "🟡 Доступны обновления") {
		t.Errorf("Updates section should be hidden when empty")
	}
}

func TestMaintPanelText_HrneoInstalledButStopped(t *testing.T) {
	args := MaintPanelArgs{
		Nickname:       "testkeen",
		HrneoInstalled: true,
		HrneoRunning:   false,
		HrneoVersion:   "2.4.0",
		AwgmgrVersion:  "2.8.2",
		AwgmgrRunning:  true,
		Firmware:       wire.FirmwareStatus{Current: "5.0.0"},
	}
	text := MaintPanelText(args)
	if !strings.Contains(text, "HydraRoute-Neo: ❌ остановлен, v2.4.0") {
		t.Errorf("stopped hrneo should render as stopped, got:\n%s", text)
	}
	if strings.Contains(text, "HydraRoute-Neo: ✅ running") {
		t.Errorf("stopped hrneo must not render as running:\n%s", text)
	}
}

func TestMaintPanelKeyboard_NormalAndCooldown(t *testing.T) {
	// No cooldown — reboot button shows the destructive action in Russian.
	args := MaintPanelArgs{Nickname: "x"}
	kb := MaintPanelKeyboard(42, args)
	if !findButtonText(kb, "🔁 Перезапустить hrneo") {
		t.Error("missing Restart hrneo button")
	}
	if !findButtonText(kb, "🔁 Перезапустить awg-manager") {
		t.Error("missing Restart awg-mgr button")
	}
	if !findButtonText(kb, "🔁 Перезагрузить роутер") {
		t.Error("missing Reboot router button (no cooldown)")
	}

	// With router cooldown — Reboot button replaced with cooldown indicator.
	args.RouterCooldownRemaining = 2*time.Minute + 23*time.Second
	kb = MaintPanelKeyboard(42, args)
	if findButtonText(kb, "🔁 Перезагрузить роутер") {
		t.Error("Reboot router button must NOT show during cooldown")
	}
	if !findButtonContains(kb, "Кулдаун") {
		t.Error("Cooldown button must show during cooldown")
	}
}

func TestMaintPanelKeyboard_CallbackData(t *testing.T) {
	args := MaintPanelArgs{Nickname: "x"}
	kb := MaintPanelKeyboard(42, args)
	wants := []string{
		"maint_restart:42:hrneo",
		"maint_restart:42:awgmgr",
		"maint_restart:42:router",
		"maint_fw_open:42:_panel_",
		"maint_open:42:_panel_",
		"maint_close:42:_panel_",
	}
	for _, want := range wants {
		if !findCallbackData(kb, want) {
			t.Errorf("missing callback_data %q", want)
		}
	}
}

func TestMaintPanelKeyboard_OffersPostActionChecks(t *testing.T) {
	kb := MaintPanelKeyboard(42, MaintPanelArgs{Nickname: "x"})
	for _, want := range []string{
		"router_doctor:42:_menu",
		"tunnels_refresh:42:_panel_",
	} {
		if !findCallbackData(kb, want) {
			t.Errorf("maintenance panel should offer post-action check %q", want)
		}
	}
}

// --- RestartConfirmText / RestartConfirmKeyboard ---

func TestRestartConfirmText_Hrneo(t *testing.T) {
	text := RestartConfirmText("hrneo", "a1b2c3d4")
	for _, want := range []string{"⚠️", "HydraRoute-Neo", "Код подтверждения: a1b2c3d4", "живёт 5 мин"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestRestartConfirmText_Router_MentionsCooldown(t *testing.T) {
	text := RestartConfirmText("router", "deadbeef")
	if !strings.Contains(text, "Кулдаун") {
		t.Errorf("router confirm must mention cooldown:\n%s", text)
	}
}

func TestRestartConfirmKeyboard(t *testing.T) {
	kb := RestartConfirmKeyboard(42, "router", "deadbeef")
	if !findCallbackData(kb, "maint_confirm:42:router:deadbeef") {
		t.Error("missing confirm callback_data")
	}
	if !findCallbackData(kb, "maint_open:42:_panel_") {
		t.Error("missing back/cancel callback_data")
	}
}

// --- FirmwareScreenText / FirmwareScreenKeyboard ---

func TestFirmwareScreenText_NoUpdate(t *testing.T) {
	text := FirmwareScreenText("testkeen", wire.FirmwareStatus{Current: "5.0.1", Channel: "stable"})
	for _, want := range []string{"📦 Прошивка", "Текущая:", "5.0.1", "актуальная", "Канал: stable"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestFirmwareScreenText_WithUpdate(t *testing.T) {
	text := FirmwareScreenText("testkeen", wire.FirmwareStatus{Current: "5.0.1", Available: "5.0.2", Channel: "stable"})
	for _, want := range []string{"Доступная:", "5.0.2", "⬆ обновление"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestFirmwareScreenKeyboard_HasInstallWhenAvailable(t *testing.T) {
	kb := FirmwareScreenKeyboard(42, wire.FirmwareStatus{Current: "5.0.1", Available: "5.0.2"}, 0)
	if !findButtonContains(kb, "Установить") {
		t.Error("install button missing when update available")
	}
	if !findCallbackData(kb, "maint_fw_install:42:_panel_") {
		t.Error("install callback_data missing")
	}
}

func TestFirmwareScreenKeyboard_NoInstallWhenCurrent(t *testing.T) {
	kb := FirmwareScreenKeyboard(42, wire.FirmwareStatus{Current: "5.0.1"}, 0)
	if findButtonContains(kb, "Установить") {
		t.Error("install button must be hidden when no update available")
	}
	// Recheck and Back must still be present
	if !findCallbackData(kb, "maint_fw_check:42:_panel_") {
		t.Error("recheck button missing")
	}
	if !findCallbackData(kb, "maint_open:42:_panel_") {
		t.Error("back button missing")
	}
}

func TestFirmwareScreenKeyboard_CooldownReplacesInstall(t *testing.T) {
	kb := FirmwareScreenKeyboard(42, wire.FirmwareStatus{Current: "5.0.1", Available: "5.0.2"}, 3*time.Minute)
	if findButtonContains(kb, "Установить") {
		t.Error("install button must NOT show during cooldown")
	}
	if !findButtonContains(kb, "Кулдаун") {
		t.Error("cooldown indicator must show during cooldown")
	}
}

// --- FirmwareConfirmText / FirmwareConfirmKeyboard ---

func TestFirmwareConfirmText(t *testing.T) {
	text := FirmwareConfirmText("deadbeef")
	for _, want := range []string{"⚠️", "Прошивка", "перезагрузку", "Код подтверждения: deadbeef", "Кулдаун"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestFirmwareConfirmKeyboard(t *testing.T) {
	kb := FirmwareConfirmKeyboard(42, "deadbeef")
	if !findCallbackData(kb, "maint_fw_confirm:42:_panel_:deadbeef") {
		t.Error("confirm callback_data missing")
	}
	if !findCallbackData(kb, "maint_fw_open:42:_panel_") {
		t.Error("cancel/back callback_data missing")
	}
}

// --- Helpers ---

func findButtonText(kb InlineKeyboardMarkup, want string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.Text == want {
				return true
			}
		}
	}
	return false
}

func findButtonContains(kb InlineKeyboardMarkup, substr string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, substr) {
				return true
			}
		}
	}
	return false
}

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
