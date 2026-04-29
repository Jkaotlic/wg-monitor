package tg

import "fmt"

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// KeyboardOption customises HardAlertKeyboard layout. Default emits the base
// 6 silence/ack/mute/history buttons; opts append optional command-channel rows.
type KeyboardOption func(*kbOpts)

type kbOpts struct {
	tunnelActions bool
	mobileActions bool
}

// WithTunnelActions appends a row [🔁 Restart][📊 Diag][▶ Pingcheck] —
// only meaningful for tunnel_* checks where awg-manager can act on a tunnel.
func WithTunnelActions() KeyboardOption {
	return func(o *kbOpts) { o.tunnelActions = true }
}

// WithMobileActions appends a row [🔁 Force recheck] — used on heartbeat alerts
// for kind=mobile users so admin can force the agent to send-now after a 4G gap.
func WithMobileActions() KeyboardOption {
	return func(o *kbOpts) { o.mobileActions = true }
}

// HardAlertKeyboard returns the inline keyboard layout under each HARD alert.
// Base 6 buttons (per Stage 2 spec §6.2) are always present; opts add
// command-channel rows for tunnel/mobile-specific actions.
func HardAlertKeyboard(userID int64, checkName string, opts ...KeyboardOption) InlineKeyboardMarkup {
	o := kbOpts{}
	for _, op := range opts {
		op(&o)
	}
	silenceCD := func(ttl string) string {
		return fmt.Sprintf("silence:%d:%s:%s", userID, checkName, ttl)
	}
	plainCD := func(action string) string {
		return fmt.Sprintf("%s:%d:%s", action, userID, checkName)
	}
	rows := [][]InlineKeyboardButton{
		{
			{Text: "⏸ 1ч", CallbackData: silenceCD("1h")},
			{Text: "⏸ 4ч", CallbackData: silenceCD("4h")},
			{Text: "⏸ 24ч", CallbackData: silenceCD("24h")},
			{Text: "✅ Ack", CallbackData: plainCD("ack")},
		},
		{
			{Text: "📋 История 24ч", CallbackData: plainCD("history")},
			{Text: "🔇 Mute до утра", CallbackData: plainCD("mute")},
		},
	}
	if o.tunnelActions {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "🔁 Restart", CallbackData: plainCD("restart_tunnel")},
			{Text: "📊 Diag", CallbackData: plainCD("diag_now")},
			{Text: "▶ Pingcheck", CallbackData: plainCD("pingcheck_now")},
		})
	}
	if o.mobileActions {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "🔁 Force recheck", CallbackData: plainCD("force_recheck")},
		})
	}
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}
