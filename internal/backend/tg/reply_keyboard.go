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
