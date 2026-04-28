package tg

import "fmt"

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// HardAlertKeyboard returns the 6-button layout under each HARD alert
// per spec §6.2: row 1 [⏸1h][⏸4h][⏸24h][✅Ack], row 2 [📋History][🔇Mute].
func HardAlertKeyboard(userID int64, checkName string) InlineKeyboardMarkup {
	silenceCD := func(ttl string) string {
		return fmt.Sprintf("silence:%d:%s:%s", userID, checkName, ttl)
	}
	plainCD := func(action string) string {
		return fmt.Sprintf("%s:%d:%s", action, userID, checkName)
	}
	return InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
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
		},
	}
}
