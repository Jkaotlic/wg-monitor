package tg

import (
	"fmt"
	"strings"
)

// PingCheckPanelEntry — one row for the PingCheck Panel renderer.
//
// PerTunnelEnabled mirrors awg-mgr's per-tunnel watchdog flag (independent
// of the tunnel's own enabled flag). Status comes from awg-mgr
// ("alive"/"dead"/empty); LastLatencyMs == 0 renders as "---".
type PingCheckPanelEntry struct {
	TunnelID         string // "awg10" — used in callback_data for toggle
	Name             string // "amst" — display label
	NDMSName         string // "Wireguard0" — packed into toggle callback_data
	Status           string // "alive" | "dead" | ""
	PerTunnelEnabled bool   // false → ⏸ icon, watchdog suspended for this tunnel
	LastLatencyMs    int    // 0 → "---"
	SuccessCount     int64
	FailCount        int
	FailThreshold    int
	RestartCount     int
}

// PingCheckPanelText renders the message body. globalEnabled is the
// /api/pingcheck/status .data.enabled flag — false → grey banner.
func PingCheckPanelText(nickname string, globalEnabled bool, entries []PingCheckPanelEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📡 PingCheck — %s\n", nickname)
	if len(entries) == 0 {
		b.WriteString("\nТуннелей не обнаружено — PingCheck не отчитался.")
		return b.String()
	}
	b.WriteString("\nТуннели:\n")
	for _, e := range entries {
		b.WriteString("  • ")
		b.WriteString(formatPingCheckRow(e))
		b.WriteString("\n")
	}
	b.WriteString("\nСостояние:\n  • Мониторинг: ")
	if globalEnabled {
		b.WriteString("✅ включён")
	} else {
		b.WriteString("⏸ выключен")
	}
	b.WriteString("\n  • ✓ — успешные проверки, ✗ — ошибки подряд, restart — автоперезапуски")
	return b.String()
}

func formatPingCheckRow(e PingCheckPanelEntry) string {
	icon := "❓"
	switch {
	case !e.PerTunnelEnabled:
		icon = "⏸"
	case e.Status == "alive":
		icon = "🟢"
	case e.Status == "dead":
		icon = "🔴"
	}
	lat := "---"
	if e.LastLatencyMs > 0 {
		lat = fmt.Sprintf("%dms", e.LastLatencyMs)
	}
	warn := ""
	if e.RestartCount > 5 {
		warn = " ⚠"
	}
	name := e.Name
	if name == "" {
		name = e.TunnelID
	}
	return fmt.Sprintf("%s %s  %s  ✓%s  ✗%d/%d   restart×%d%s",
		icon, name, lat, formatCount(e.SuccessCount), e.FailCount, e.FailThreshold, e.RestartCount, warn)
}

// formatCount renders 0..9999 as plain int; >=10000 as "12.5k" (one decimal).
func formatCount(n int64) string {
	if n < 10000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// PingCheckPanelKeyboard builds the inline keyboard for the PingCheck
// Panel. callback_data shapes:
//
//	pingcheck_toggle:<userID>:<tunnel_id>:<ndms_name>:<0|1>   ← per-tunnel
//	pingcheck_now:<userID>:_menu                              ← global "check now"
//	pingcheck_open:<userID>:_panel_                           ← refresh self
//	close_panel:<userID>:_panel_                              ← close panel
//	panel:0:help:pingcheck                                    ← help screen
//
// Toggle icon meaning: shown icon = action that *would* happen on tap.
// Enabled tunnel → ⏸ button (disable on tap); disabled tunnel → ▶ button
// (enable on tap).
const pingcheckMaxPerRow = 8

func PingCheckPanelKeyboard(userID int64, entries []PingCheckPanelEntry) InlineKeyboardMarkup {
	rows := [][]InlineKeyboardButton{}

	var row []InlineKeyboardButton
	for _, e := range entries {
		var icon, flag string
		if e.PerTunnelEnabled {
			icon, flag = "⏸", "0"
		} else {
			icon, flag = "▶", "1"
		}
		label := e.Name
		if label == "" {
			label = e.TunnelID
		}
		row = append(row, InlineKeyboardButton{
			Text:         fmt.Sprintf("%s %s", icon, label),
			CallbackData: fmt.Sprintf("pingcheck_toggle:%d:%s:%s:%s", userID, e.TunnelID, e.NDMSName, flag),
		})
		if len(row) >= pingcheckMaxPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	rows = append(rows, []InlineKeyboardButton{
		{Text: "▶ Проверить сейчас", CallbackData: fmt.Sprintf("pingcheck_now:%d:_menu", userID)},
		{Text: "🔄 Обновить", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
	})
	rows = append(rows, []InlineKeyboardButton{
		{Text: "ℹ Помощь", CallbackData: "panel:0:help:pingcheck"},
		{Text: "✖ Закрыть", CallbackData: fmt.Sprintf("close_panel:%d:_panel_", userID)},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}
