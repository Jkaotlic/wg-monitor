// DEPRECATED v0.6.0: ControlPanel-based pinned menu was replaced by
// ReplyKeyboardForTopic + smart-replies (spec 2026-04-30). Kept for one
// release window so any in-flight callbacks dispatched against pinned
// messages still parse correctly. Remove in v0.7.0.
package tg

import "fmt"

// TunnelEntry describes one tunnel for control-panel button labels.
// Name is short pretty label (e.g. "amnezia"), CheckName is the FSM key
// passed in callback_data (e.g. "tunnel_awg12") so the same router that
// handles HARD-alert callbacks dispatches menu callbacks unchanged.
type TunnelEntry struct {
	Name      string // pretty display label
	CheckName string // FSM key, must match agent's per-tunnel check_name
}

// ControlPanelKeyboard renders the persistent control-panel keyboard.
// Each tunnel gets its own row of three actions (Restart / Diag / Pingcheck),
// followed by user-wide controls (force_recheck for mobile, opkg_upgrade for all).
//
// callback_data shape: <action>:<user_id>:<check_name>_menu
// The "_menu" suffix is the marker that callbacks.Parse strips and uses to set
// Args.IsMenu=true — that flag tells router.HandleCallback NOT to edit/destroy
// the keyboard (so the pinned panel keeps working after every tap, unlike
// HARD-alert keyboards which collapse to text+status after one use).
//
// For wide buttons (opkg_upgrade, force_recheck) the check_name is just the
// suffix on its own ("_menu"), which Parse maps to the sentinel CheckName="_menu".
func ControlPanelKeyboard(userID int64, tunnels []TunnelEntry, isMobile bool) InlineKeyboardMarkup {
	const suffix = "_menu"
	cd := func(action, checkName string) string {
		return fmt.Sprintf("%s:%d:%s%s", action, userID, checkName, suffix)
	}
	rows := make([][]InlineKeyboardButton, 0, len(tunnels)+2)
	for _, t := range tunnels {
		label := t.Name
		if label == "" {
			label = t.CheckName
		}
		rows = append(rows, []InlineKeyboardButton{
			{Text: "🔁 Restart " + label, CallbackData: cd("restart_tunnel", t.CheckName)},
			{Text: "📊 Diag " + label, CallbackData: cd("diag_now", t.CheckName)},
			{Text: "▶ Ping " + label, CallbackData: cd("pingcheck_now", t.CheckName)},
		})
	}
	wide := make([]InlineKeyboardButton, 0, 2)
	if isMobile {
		wide = append(wide, InlineKeyboardButton{
			Text: "🔄 Force recheck", CallbackData: cd("force_recheck", ""),
		})
	}
	wide = append(wide, InlineKeyboardButton{
		Text: "⬆ Opkg upgrade", CallbackData: cd("opkg_upgrade", ""),
	})
	rows = append(rows, wide)
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

// ControlPanelText returns the body of the pinned control-panel message.
// Kept dumb on purpose so the user can pin it and forget — refresh is a CLI op.
func ControlPanelText(nickname string, tunnels []TunnelEntry) string {
	if len(tunnels) == 0 {
		return fmt.Sprintf("🎛 Control panel — %s\n(no tunnels detected yet)", nickname)
	}
	s := fmt.Sprintf("🎛 Control panel — %s\nТуннели:\n", nickname)
	for _, t := range tunnels {
		label := t.Name
		if label == "" {
			label = t.CheckName
		}
		s += fmt.Sprintf("  • %s\n", label)
	}
	s += "\nКнопки работают 24/7. Закрепи это сообщение в топике."
	return s
}
