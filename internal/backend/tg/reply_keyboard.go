package tg

// ReplyKeyboardButton mirrors the TG Bot API ReplyKeyboardButton object.
// Only `text` is needed for our two-button layouts (we never request
// contact/location/poll/web_app variants).
type ReplyKeyboardButton struct {
	Text string `json:"text"`
}

// ReplyKeyboardMarkup mirrors the TG Bot API ReplyKeyboardMarkup object.
// Field order matches the canonical JSON for stable golden assertions.
type ReplyKeyboardMarkup struct {
	Keyboard       [][]ReplyKeyboardButton `json:"keyboard"`
	IsPersistent   bool                    `json:"is_persistent,omitempty"`
	ResizeKeyboard bool                    `json:"resize_keyboard,omitempty"`
	Selective      bool                    `json:"selective,omitempty"`
}

// ReplyKeyboardRemove mirrors the TG Bot API ReplyKeyboardRemove object.
// Sent in place of a ReplyKeyboardMarkup to clear the bottom panel
// (used in unknown / non-topic chats per spec §5.1).
type ReplyKeyboardRemove struct {
	RemoveKeyboard bool `json:"remove_keyboard"`
	Selective      bool `json:"selective,omitempty"`
}

// ReplyKeyboardForTopic returns the right ReplyKeyboardMarkup (or a
// ReplyKeyboardRemove) for a given topic kind. Returned type is `any` so
// callers can pass either *ReplyKeyboardMarkup or *ReplyKeyboardRemove
// directly into SendMessageWithReplyKeyboard.
//
// kind ∈ {"per_router","summary","systemic","unknown"}.
func ReplyKeyboardForTopic(kind string) any {
	switch kind {
	case "per_router":
		return &ReplyKeyboardMarkup{
			Keyboard: [][]ReplyKeyboardButton{
				{{Text: "📊 Что происходит?"}, {Text: "🎛 Туннели"}},
				{{Text: "🌍 Через тоннель?"}, {Text: "🇷🇺 Напрямую?"}},
				{{Text: "🛣 Маршруты"}, {Text: "⬆ Обновить пакеты"}},
			},
			IsPersistent:   true,
			ResizeKeyboard: true,
		}
	case "summary", "systemic":
		return &ReplyKeyboardMarkup{
			Keyboard: [][]ReplyKeyboardButton{
				{{Text: "📋 Список юзеров"}, {Text: "📊 Здоровье флота"}},
			},
			IsPersistent:   true,
			ResizeKeyboard: true,
		}
	default:
		return &ReplyKeyboardRemove{RemoveKeyboard: true}
	}
}
