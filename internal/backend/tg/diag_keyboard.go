package tg

import "fmt"

// DiagResultKeyboard returns the inline keyboard that sits under a
// diag_now Card. status MUST match the wire.CommandResult.Status that
// produced the Card: "ok" → primary "Полный отчёт" + rerun + close;
// any other value → retry + help + close. userID is the router target
// encoded into the callback grammar; rawToken is the diagCache token
// (only used on the ok path).
func DiagResultKeyboard(status string, userID int64, rawToken string) InlineKeyboardMarkup {
	if status == "ok" {
		return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "📄 Полный отчёт", CallbackData: fmt.Sprintf("diag_raw:%d:_panel_:%s", userID, rawToken)},
				{Text: "🔁 Перезапустить", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
			},
			{
				{Text: "✖ Закрыть", CallbackData: fmt.Sprintf("routes_close:%d:_panel_", userID)},
			},
		}}
	}
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{
			{Text: "🔁 Попробовать снова", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
			{Text: "ℹ Помощь", CallbackData: "panel:0:help:diag"},
		},
		{
			{Text: "✖ Закрыть", CallbackData: fmt.Sprintf("routes_close:%d:_panel_", userID)},
		},
	}}
}
