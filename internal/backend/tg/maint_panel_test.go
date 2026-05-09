package tg

import (
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// --- MaintPanelText / MaintPanelKeyboard ---

func TestMaintPanelText_FullState(t *testing.T) {
	args := MaintPanelArgs{
		Nickname:      "testkeen",
		HrneoVersion:  "2.4.0",
		HrneoUptime:   "3д 4ч",
		HrneoRunning:  true,
		AwgmgrVersion: "2.8.2",
		AwgmgrUptime:  "7д 12ч",
		KeeneticOS:    "KN-1811",
		Firmware:      wire.FirmwareStatus{Current: "5.00.C.11.0-0"},
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
}

func TestMaintPanelText_HrneoNotInstalled(t *testing.T) {
	args := MaintPanelArgs{
		Nickname:      "testkeen",
		HrneoVersion:  "", // empty signals not installed
		HrneoRunning:  false,
		AwgmgrVersion: "2.8.2",
		AwgmgrUptime:  "7д 12ч",
		KeeneticOS:    "KN-1811",
		Firmware:      wire.FirmwareStatus{Current: "5.0.0"},
	}
	text := MaintPanelText(args)
	if !strings.Contains(text, "не установлен") {
		t.Errorf("hrneo-not-installed line missing:\n%s", text)
	}
	if strings.Contains(text, "🟡 Доступны обновления") {
		t.Errorf("Updates section should be hidden when empty")
	}
}

func TestMaintPanelKeyboard_NormalAndCooldown(t *testing.T) {
	// No cooldown — reboot button shows "Reboot router"
	args := MaintPanelArgs{Nickname: "x"}
	kb := MaintPanelKeyboard(42, args)
	if !findButtonText(kb, "🔁 Restart hrneo") {
		t.Error("missing Restart hrneo button")
	}
	if !findButtonText(kb, "🔁 Restart awg-mgr") {
		t.Error("missing Restart awg-mgr button")
	}
	if !findButtonText(kb, "🔁 Reboot router") {
		t.Error("missing Reboot router button (no cooldown)")
	}

	// With router cooldown — Reboot button replaced with Cooldown indicator
	args.RouterCooldownRemaining = 2*time.Minute + 23*time.Second
	kb = MaintPanelKeyboard(42, args)
	if findButtonText(kb, "🔁 Reboot router") {
		t.Error("Reboot router button must NOT show during cooldown")
	}
	if !findButtonContains(kb, "Cooldown") {
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

// --- RestartConfirmText / RestartConfirmKeyboard ---

func TestRestartConfirmText_Hrneo(t *testing.T) {
	text := RestartConfirmText("hrneo", "a1b2c3d4")
	for _, want := range []string{"⚠️", "HydraRoute-Neo", "Token: a1b2c3d4", "TTL 5"} {
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
	if !findButtonContains(kb, "Cooldown") {
		t.Error("cooldown indicator must show during cooldown")
	}
}

// --- FirmwareConfirmText / FirmwareConfirmKeyboard ---

func TestFirmwareConfirmText(t *testing.T) {
	text := FirmwareConfirmText("deadbeef")
	for _, want := range []string{"⚠️", "Прошивка", "reboot", "Token: deadbeef", "Кулдаун"} {
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
