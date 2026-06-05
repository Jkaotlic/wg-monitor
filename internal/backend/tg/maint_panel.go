// internal/backend/tg/maint_panel.go
//
// Maintenance panel renderers — one of four screens at a time:
//   - Status        : current sysctl state + actions (MaintPanelText/Keyboard)
//   - RestartConfirm: per-service confirmation (RestartConfirmText/Keyboard)
//   - FirmwareScreen: current vs available firmware (FirmwareScreenText/Keyboard)
//   - FirmwareConfirm: install warning + confirm (FirmwareConfirmText/Keyboard)
//
// All text is plain (no MarkdownV2) per the repo's Telegram conventions.
package tg

import (
	"fmt"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// UpdateLine is one row in the Status screen's "🟡 Доступны обновления" block.
type UpdateLine struct {
	Name      string // e.g. "KeeneticOS"
	Installed string
	Available string
	Hint      string
}

// MaintPanelArgs is the full set of facts the Status screen renders.
// HrneoVersion=="" signals "hrneo not installed" and triggers a different
// status line. The two CooldownRemaining fields drive the destructive-button
// disable + label swap.
type MaintPanelArgs struct {
	Nickname                  string
	HrneoInstalled            bool
	HrneoVersion              string
	HrneoUptime               string
	HrneoRunning              bool
	AwgmgrVersion             string
	AwgmgrUptime              string
	AwgmgrRunning             bool
	KeeneticOS                string // model — e.g. "KN-1811"
	Firmware                  wire.FirmwareStatus
	Updates                   []UpdateLine
	RouterCooldownRemaining   time.Duration
	FirmwareCooldownRemaining time.Duration
}

// MaintPanelText is the body of the Status screen.
func MaintPanelText(a MaintPanelArgs) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🛠 Обслуживание — %s\n\n", a.Nickname)
	b.WriteString("Сервисы:\n")
	if !a.HrneoInstalled {
		b.WriteString("  • HydraRoute-Neo: ⚪ не установлен\n")
	} else {
		hrState := "❌ остановлен"
		if a.HrneoRunning {
			hrState = "✅ работает"
		}
		fmt.Fprintf(&b, "  • HydraRoute-Neo: %s", hrState)
		if a.HrneoVersion != "" {
			fmt.Fprintf(&b, ", v%s", a.HrneoVersion)
		}
		if a.HrneoUptime != "" {
			fmt.Fprintf(&b, ", работает %s", a.HrneoUptime)
		}
		b.WriteString("\n")
	}
	awgState := "❌ не отвечает"
	if a.AwgmgrRunning {
		awgState = "✅ работает"
	}
	fmt.Fprintf(&b, "  • awg-manager: %s", awgState)
	if a.AwgmgrVersion != "" {
		fmt.Fprintf(&b, ", v%s", a.AwgmgrVersion)
	}
	if a.AwgmgrUptime != "" {
		fmt.Fprintf(&b, ", работает %s", a.AwgmgrUptime)
	}
	b.WriteString("\n")
	if a.KeeneticOS != "" {
		fmt.Fprintf(&b, "  • Keenetic OS: %s, v%s\n", a.KeeneticOS, a.Firmware.Current)
	} else {
		fmt.Fprintf(&b, "  • Keenetic OS: v%s\n", a.Firmware.Current)
	}
	if len(a.Updates) > 0 {
		b.WriteString("\n🟡 Доступны обновления:\n")
		for _, u := range a.Updates {
			fmt.Fprintf(&b, "  • %s %s → %s\n", u.Name, u.Installed, u.Available)
			if u.Hint != "" {
				fmt.Fprintf(&b, "    подсказка: %s\n", u.Hint)
			}
		}
	}
	return b.String()
}

// MaintPanelKeyboard renders the action grid:
//
//	[🔁 Перезапустить hrneo]       [🔁 Перезапустить awg-manager]
//	[🔁 Перезагрузить роутер*]     [📦 Прошивка]
//	[🔄 Проверить апдейты] [✖ Закрыть]
//
// * If RouterCooldownRemaining > 0, the Reboot button is replaced with a
// disabled-looking "🕒 Кулдаун MM:SS" indicator that re-opens the panel
// (no destructive action).
func MaintPanelKeyboard(userID int64, a MaintPanelArgs) InlineKeyboardMarkup {
	cd := func(action, arg string) string { return fmt.Sprintf("%s:%d:%s", action, userID, arg) }
	rebootLabel := "🔁 Перезагрузить роутер"
	rebootCD := cd("maint_restart", "router")
	if a.RouterCooldownRemaining > 0 {
		rebootLabel = "🕒 Кулдаун " + fmtCooldown(a.RouterCooldownRemaining)
		rebootCD = cd("maint_open", "_panel_")
	}
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{
			{Text: "🔁 Перезапустить hrneo", CallbackData: cd("maint_restart", "hrneo")},
			{Text: "🔁 Перезапустить awg-manager", CallbackData: cd("maint_restart", "awgmgr")},
		},
		{
			{Text: rebootLabel, CallbackData: rebootCD},
			{Text: "📦 Прошивка", CallbackData: cd("maint_fw_open", "_panel_")},
		},
		{
			{Text: "🔄 Проверить апдейты", CallbackData: cd("maint_open", "_panel_")},
		},
		{
			{Text: "🩺 Проверка", CallbackData: fmt.Sprintf("router_doctor:%d:_menu", userID)},
			{Text: "🎛 Туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
		},
		HelpRowFor("maint"),
		{
			{Text: "✖ Закрыть", CallbackData: cd("maint_close", "_panel_")},
		},
	}}
}

// RestartConfirmText is the warning shown after a "🔁 Restart X" tap. The
// "What happens" section is service-specific.
func RestartConfirmText(name, token string) string {
	display := nameToDisplay(name)
	var what string
	switch name {
	case "hrneo":
		what = "  • DNS-routes на короткое время (~5 сек) перестанут резолвиться по правилам.\n" +
			"  • Static-routes продолжат работать.\n" +
			"  • Кратковременная просадка на доменах из ip.list."
	case "hrneo_start":
		what = "  • HydraRoute-Neo будет запущен.\n" +
			"  • DNS/HR-Neo правила начнут применяться после старта демона.\n" +
			"  • Если сервис уже работает, команда безопасно завершится без лишних изменений."
	case "hrneo_stop":
		what = "  • HydraRoute-Neo будет остановлен.\n" +
			"  • DNS/HR-Neo правила временно перестанут маршрутизировать домены.\n" +
			"  • Static-routes awg-manager продолжат работать."
	case "awgmgr":
		what = "  • Веб-интерфейс awg-manager на ~3-5 сек перестанет отвечать.\n" +
			"  • Туннели НЕ разрываются — это перезапуск только демона awg-manager.\n" +
			"  • API-вызовы из бэкенда (recheck, restart_tunnel) дадут ошибку, если попадут в окно."
	case "router":
		what = "  • Все туннели разорвутся (~1-2 мин downtime).\n" +
			"  • Если ты сейчас сидишь через VPN, TG отвалится до восстановления.\n" +
			"  • Алерты придут сразу после reboot — это нормально.\n" +
			"  • Кулдаун: 5 мин (повторное нажатие заблокировано)."
	}
	return fmt.Sprintf("🛠 Обслуживание\n\n⚠️ Перезапустить %s?\n\nЧто произойдёт:\n%s\n\nКод подтверждения: %s (живёт 5 мин)",
		display, what, token)
}

// RestartConfirmKeyboard is two buttons: Confirm (sends maint_confirm with
// the bound name+token) and Cancel (re-opens the status panel).
func RestartConfirmKeyboard(userID int64, name, token string) InlineKeyboardMarkup {
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "✅ Подтвердить", CallbackData: fmt.Sprintf("maint_confirm:%d:%s:%s", userID, name, token)},
		{Text: "↩ Отмена", CallbackData: fmt.Sprintf("maint_open:%d:_panel_", userID)},
	}}}
}

func OpkgUpgradeConfirmText(token string) string {
	return fmt.Sprintf("🛠 Обслуживание\n\n⚠️ Запустить opkg upgrade?\n\nЧто произойдет:\n  • агент выполнит opkg update, проверит свободное место в /opt и запустит opkg upgrade.\n  • обновление пакетов может перезапустить Entware-сервисы или кратко задеть роутерные утилиты.\n  • если фиды сломаны, результат предложит действия для ремонта фидов.\n\nКод подтверждения: %s (живёт 5 мин)", token)
}

// FirmwareScreenText shows current + available firmware. When Available is
// empty, displays "актуальная" instead of an install hint.
func FirmwareScreenText(nickname string, fs wire.FirmwareStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📦 Прошивка — %s\n\nТекущая:    KeeneticOS %s\n", nickname, fs.Current)
	if fs.Available == "" {
		b.WriteString("Доступная:  актуальная\n")
	} else {
		fmt.Fprintf(&b, "Доступная:  KeeneticOS %s   ⬆ обновление\n", fs.Available)
	}
	if fs.Channel != "" {
		fmt.Fprintf(&b, "\nКанал: %s\n", fs.Channel)
	}
	return b.String()
}

// FirmwareScreenKeyboard returns the install/recheck/back buttons.
// Install is hidden when no update is available; replaced with a Cooldown
// indicator when cdRem > 0.
func FirmwareScreenKeyboard(userID int64, fs wire.FirmwareStatus, cdRem time.Duration) InlineKeyboardMarkup {
	cd := func(action, arg string) string { return fmt.Sprintf("%s:%d:%s", action, userID, arg) }
	rows := [][]InlineKeyboardButton{}
	if fs.Available != "" {
		var label, data string
		if cdRem > 0 {
			label = "🕒 Кулдаун " + fmtCooldown(cdRem)
			data = cd("maint_fw_open", "_panel_")
		} else {
			label = "⬆ Установить и перезагрузить"
			data = cd("maint_fw_install", "_panel_")
		}
		rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: data}})
	}
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🔄 Перепроверить", CallbackData: cd("maint_fw_check", "_panel_")},
		{Text: "↩ Назад", CallbackData: cd("maint_open", "_panel_")},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

// FirmwareConfirmText is the install warning (after tap of "⬆ Установить").
func FirmwareConfirmText(token string) string {
	return fmt.Sprintf(
		"📦 Прошивка\n\n⚠️ Установить новую прошивку и перезагрузить роутер?\n\n"+
			"  • После старта установки роутер уйдёт в перезагрузку (~2-3 мин).\n"+
			"  • Все туннели разорвутся.\n"+
			"  • Кулдаун: 5 мин.\n\n"+
			"Код подтверждения: %s (живёт 5 мин)",
		token)
}

// FirmwareConfirmKeyboard is two buttons: Confirm (maint_fw_confirm) and
// Cancel (back to firmware screen).
func FirmwareConfirmKeyboard(userID int64, token string) InlineKeyboardMarkup {
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "✅ Подтвердить", CallbackData: fmt.Sprintf("maint_fw_confirm:%d:_panel_:%s", userID, token)},
		{Text: "↩ Отмена", CallbackData: fmt.Sprintf("maint_fw_open:%d:_panel_", userID)},
	}}}
}

// nameToDisplay translates a service-name token into the human label used in
// confirm screens.
func nameToDisplay(name string) string {
	switch name {
	case "hrneo":
		return "HydraRoute-Neo"
	case "hrneo_start":
		return "HydraRoute-Neo"
	case "hrneo_stop":
		return "HydraRoute-Neo"
	case "awgmgr":
		return "awg-manager"
	case "router":
		return "роутер"
	default:
		return name
	}
}

// fmtCooldown formats a duration as "MM:SS" for cooldown badges.
func fmtCooldown(d time.Duration) string {
	d = d.Round(time.Second)
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%02d:%02d", m, s)
}
