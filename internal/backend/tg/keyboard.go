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
	tunnelActions     bool
	mobileActions     bool
	hydraRouteActions bool
}

// WithTunnelActions appends a row [🔁 Restart awg-manager][📊 Diag][▶ Pingcheck].
// The restart_tunnel wire action restarts awg-manager, not a single tunnel.
func WithTunnelActions() KeyboardOption {
	return func(o *kbOpts) { o.tunnelActions = true }
}

// WithMobileActions appends a row [🔁 Force recheck] — used on heartbeat alerts
// for kind=mobile users so admin can force the agent to send-now after a 4G gap.
func WithMobileActions() KeyboardOption {
	return func(o *kbOpts) { o.mobileActions = true }
}

func WithHydraRouteActions() KeyboardOption {
	return func(o *kbOpts) { o.hydraRouteActions = true }
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
			{Text: "⏸ Тише на 1ч", CallbackData: silenceCD("1h")},
			{Text: "⏸ Тише на 4ч", CallbackData: silenceCD("4h")},
			{Text: "⏸ Тише на 24ч", CallbackData: silenceCD("24h")},
			{Text: "✅ Понял", CallbackData: plainCD("ack")},
		},
		{
			{Text: "📋 История за 24ч", CallbackData: plainCD("history")},
			{Text: "🔇 Тихо до утра", CallbackData: plainCD("mute")},
		},
	}
	if o.tunnelActions {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "🔁 Перезапуск awg-manager", CallbackData: plainCD("restart_tunnel")},
			{Text: "📊 Диагностика", CallbackData: plainCD("diag_now")},
			{Text: "▶ Тест связи", CallbackData: plainCD("pingcheck_now")},
		})
	}
	if o.mobileActions {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "🔄 Дай отчёт сейчас", CallbackData: plainCD("force_recheck")},
		})
	}
	if o.hydraRouteActions {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "▶ Запустить HR-Neo", CallbackData: fmt.Sprintf("maint_restart:%d:hrneo_start", userID)},
			{Text: "🔁 Перезапустить HR-Neo", CallbackData: fmt.Sprintf("maint_restart:%d:hrneo", userID)},
		})
	}
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}
