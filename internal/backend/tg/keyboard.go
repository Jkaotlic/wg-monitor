package tg

import "fmt"

// WebAppInfo is Telegram's InlineKeyboardButton.web_app shape: opening this
// button launches the given URL as a Telegram Mini App instead of firing a
// callback_data update.
type WebAppInfo struct {
	URL string `json:"url"`
}

type InlineKeyboardButton struct {
	Text         string      `json:"text"`
	CallbackData string      `json:"callback_data,omitempty"`
	WebApp       *WebAppInfo `json:"web_app,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// AlertKeyboard returns the inline keyboard under every alert card (HARD,
// ROUTER OFFLINE, STILL-DOWN reminders). Owners get exactly two things to do
// from a Telegram alert: open the router in the Mini App to see and fix
// what's wrong, or silence this particular check for an hour. Every other
// command button (restart/diag/pingcheck/force_recheck/ack/mute/history)
// moved into the app; the callback handlers stay wired for old messages
// still sitting in owners' chats.
func AlertKeyboard(userID int64, checkName, appURL string) InlineKeyboardMarkup {
	var rows [][]InlineKeyboardButton
	if appURL != "" {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "📱 Открыть в приложении", WebApp: &WebAppInfo{URL: appURL}},
		})
	}
	rows = append(rows, []InlineKeyboardButton{
		{Text: "⏸ Тише на час", CallbackData: fmt.Sprintf("silence:%d:%s:1h", userID, checkName)},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}
