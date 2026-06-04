package tg

import (
	"fmt"
	"strings"
)

// TunnelPanelEntry — one row for the Tunnels Panel renderer.
//
// Status is the agent-side `tu.Status` ("running" / "disabled" / "stopped" / …);
// Enabled mirrors `tu.Enabled` so the toggle button can pick the right action
// (`tunnel_enable` vs `tunnel_disable`) without a second DB lookup.
//
// HandshakeAge is seconds; 0 means "never". CheckName is the FSM key
// ("tunnel_awg11") so callback_data ties back to the per-tunnel state.
// NDMSName ("Wireguard0") is propagated through callback_data because the
// agent needs it for `ndmc -c "interface <NDMSName> up|down"` — there's no
// per-tunnel start endpoint in awg-manager.
type TunnelPanelEntry struct {
	Name         string // "amnezia_for_awg"
	CheckName    string // "tunnel_awg11"
	Interface    string // "nwg1"
	NDMSName     string // "Wireguard0"
	Enabled      bool
	Status       string
	HandshakeAge int
}

// TunnelsPanelText renders the message body for the Tunnels Panel — a static
// status snapshot above the inline toggle row. Operators wanted ONE line per
// tunnel, no kitchen-sink dump.
func TunnelsPanelText(nickname string, entries []TunnelPanelEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🎛 Туннели — %s\n", nickname)
	if len(entries) == 0 {
		b.WriteString("\n(туннелей не обнаружено — агент ещё не отчитался)")
		return b.String()
	}
	b.WriteString("\nСписок:\n")
	for _, e := range entries {
		b.WriteString("  • ")
		b.WriteString(formatTunnelRow(e))
		b.WriteString("\n")
	}
	b.WriteString("\nДействия:\n  • кнопки переключают вкл/выкл\n  • ряд 🔁 с именами туннелей делает выкл→вкл одного туннеля\n  • «🔁 Перезагрузить awg-mgr» рестартит awg-manager целиком")
	return b.String()
}

func formatTunnelRow(e TunnelPanelEntry) string {
	icon := "✅"
	state := ""
	switch {
	case !e.Enabled:
		icon = "⏸"
		state = "выключен"
	case e.Status != "" && e.Status != "running":
		icon = "🔴"
		state = humanTunnelStatus(e.Status)
	case e.HandshakeAge > 0:
		state = "обмен ключами " + humanAgeShort(e.HandshakeAge)
	case e.HandshakeAge == 0:
		icon = "🔴"
		state = "обмена ключами ещё не было"
	}
	label := e.Name
	if label == "" {
		label = e.CheckName
	}
	if e.Interface != "" {
		label = fmt.Sprintf("%s (%s)", label, e.Interface)
	}
	if state != "" {
		return fmt.Sprintf("%s %s · %s", icon, label, state)
	}
	return fmt.Sprintf("%s %s", icon, label)
}

func humanTunnelStatus(status string) string {
	switch status {
	case "disabled":
		return "выключен"
	case "stopped":
		return "остановлен"
	case "dead", "fail", "failed":
		return "не на связи"
	default:
		return status
	}
}

func humanAgeShort(s int) string {
	if s < 60 {
		return fmt.Sprintf("%dс", s)
	}
	m := s / 60
	if m < 60 {
		return fmt.Sprintf("%dм", m)
	}
	h := m / 60
	return fmt.Sprintf("%dч", h)
}

// TunnelsPanelKeyboard builds the inline keyboard for the Tunnels Panel.
//
// Layout:
//
//	Row 1: per-tunnel toggle buttons — "[⏸ awg]" if currently enabled,
//	       "[▶ awg2]" if currently disabled. Up to 8 in one row (TG limit).
//	       Wraps to a new row beyond that.
//	Row N+1: per-tunnel delete buttons.
//	Row N+2: [🛣 Маршруты / перенос] [🔁 Перезагрузить awg-mgr] [🔄 Обновить]
//
// callback_data shape:
//
//	tunnel_enable:<userID>:<check_name>:<ndms_name>     ← short tunnel name in label
//	tunnel_disable:<userID>:<check_name>:<ndms_name>
//	tunnel_delete_ask:<userID>:<check_name>:<ndms_name> ← opens confirm screen
//	routes_open:<userID>:_panel_                        ← open route transfer panel
//	restart_tunnel:<userID>:_panel_                     ← global restart-all
//	tunnels_refresh:<userID>:_panel_                    ← re-render
//
// We pack ndms_name as a 4th colon-segment so the parser splits cleanly. The
// existing Parse splits on ":" and reads parts[0..2]; we extend Args.NDMSName
// from parts[3] when present (only for tunnel_enable/disable).
const tunnelsMaxPerRow = 8

func TunnelsPanelKeyboard(userID int64, entries []TunnelPanelEntry) InlineKeyboardMarkup {
	rows := [][]InlineKeyboardButton{}

	// Toggle row(s).
	var row []InlineKeyboardButton
	for _, e := range entries {
		if strings.TrimSpace(e.NDMSName) == "" {
			continue
		}
		var icon, action string
		if e.Enabled {
			icon = "⏸"
			action = "tunnel_disable"
		} else {
			icon = "▶"
			action = "tunnel_enable"
		}
		label := shortTunnelLabel(e.Name, e.CheckName)
		row = append(row, InlineKeyboardButton{
			Text:         fmt.Sprintf("%s %s", icon, label),
			CallbackData: fmt.Sprintf("%s:%d:%s:%s", action, userID, e.CheckName, e.NDMSName),
		})
		if len(row) >= tunnelsMaxPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	// Restart row(s). This restarts one selected interface (down -> up), not
	// the global awg-manager daemon.
	row = nil
	for _, e := range entries {
		if strings.TrimSpace(e.NDMSName) == "" {
			continue
		}
		label := shortTunnelLabel(e.Name, e.CheckName)
		row = append(row, InlineKeyboardButton{
			Text:         fmt.Sprintf("🔁 %s", label),
			CallbackData: fmt.Sprintf("tunnel_restart:%d:%s:%s", userID, e.CheckName, e.NDMSName),
		})
		if len(row) >= tunnelsMaxPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	// Delete row(s). Deletion is destructive, so these buttons open a confirm
	// screen instead of enqueueing the agent command directly.
	row = nil
	for _, e := range entries {
		if strings.TrimSpace(e.NDMSName) == "" {
			continue
		}
		label := shortTunnelLabel(e.Name, e.CheckName)
		row = append(row, InlineKeyboardButton{
			Text:         fmt.Sprintf("🗑 %s", label),
			CallbackData: fmt.Sprintf("tunnel_delete_ask:%d:%s:%s", userID, e.CheckName, e.NDMSName),
		})
		if len(row) >= tunnelsMaxPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	// Global controls.
	rows = append(rows, HelpRowFor("tunnels"))
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🛣 Маршруты / перенос", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		{Text: "🔁 Перезагрузить awg-mgr", CallbackData: fmt.Sprintf("restart_tunnel:%d:_panel_", userID)},
		{Text: "🔄 Обновить", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

func TunnelDeleteConfirmText(e TunnelPanelEntry) string {
	label := e.Name
	if label == "" {
		label = e.CheckName
	}
	if e.NDMSName != "" {
		label = fmt.Sprintf("%s (%s)", label, e.NDMSName)
	}
	return fmt.Sprintf("🗑 Удалить тоннель %s?\n\nЭто удалит конфиг из awg-manager на роутере. Если на него завязаны маршруты, их нужно будет перенести отдельно.", label)
}

func TunnelDeleteConfirmKeyboard(userID int64, e TunnelPanelEntry) InlineKeyboardMarkup {
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{
			Text:         "🗑 Да, удалить",
			CallbackData: fmt.Sprintf("tunnel_delete:%d:%s:%s", userID, e.CheckName, e.NDMSName),
		}},
		{{
			Text:         "↩ Назад",
			CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID),
		}},
	}}
}

// shortTunnelLabel picks a compact label for the toggle button: prefer the
// last underscore-separated segment of Name (e.g. "amnezia_for_awg" → "awg"),
// falling back to the CheckName tail.
func shortTunnelLabel(name, checkName string) string {
	src := name
	if src == "" {
		src = strings.TrimPrefix(checkName, "tunnel_")
	}
	if i := strings.LastIndex(src, "_"); i >= 0 && i < len(src)-1 {
		return src[i+1:]
	}
	return src
}
