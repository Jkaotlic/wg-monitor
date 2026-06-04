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
	rows := keyboardRowsForKind(kind)
	if rows == nil {
		return &ReplyKeyboardRemove{RemoveKeyboard: true}
	}
	keys := make([][]ReplyKeyboardButton, 0, len(rows))
	for _, row := range rows {
		btns := make([]ReplyKeyboardButton, 0, len(row))
		for _, label := range row {
			btns = append(btns, ReplyKeyboardButton{Text: label})
		}
		keys = append(keys, btns)
	}
	return &ReplyKeyboardMarkup{
		Keyboard:       keys,
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
}

// CompatInlineKeyboardForTopic returns the same set of buttons as
// ReplyKeyboardForTopic, but encoded as an InlineKeyboardMarkup. Used for
// users on Telegram Desktop where the persistent ReplyKeyboardMarkup is
// flaky in forum topics — the operator can opt into ui.compat_inline_keyboard
// in backend.yaml and every bot message that would normally re-attach the
// reply keyboard instead carries the same buttons inline.
//
// callbackData layout: "compat_btn:0:<code>". The user_id slot is always 0
// because the lookup is keyed off Chat/Thread, not the user. The router
// converts a tap into a synthetic message dispatch through
// handleNonCommandMessage.
//
// Returns nil for "unknown" / any kind without a keyboard.
func CompatInlineKeyboardForTopic(kind string) *InlineKeyboardMarkup {
	rows := keyboardRowsForKind(kind)
	if rows == nil {
		return nil
	}
	out := make([][]InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		btns := make([]InlineKeyboardButton, 0, len(row))
		for _, label := range row {
			code, ok := compatBtnCodeFor(label)
			if !ok {
				continue
			}
			btns = append(btns, InlineKeyboardButton{
				Text:         label,
				CallbackData: "compat_btn:0:" + code,
			})
		}
		if len(btns) > 0 {
			out = append(out, btns)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return &InlineKeyboardMarkup{InlineKeyboard: out}
}

// OperatorMenuInlineKeyboardForTopic returns the full always-visible menu for
// router topics. It keeps the same labels and compat_btn actions as the
// bottom reply keyboard, so operators see the familiar button set while the
// callback router can dispatch taps through the existing safe text-command
// path. Unlike ReplyKeyboardMarkup, this menu is attached to the message
// itself and does not depend on Telegram keeping a bottom keyboard visible.
func OperatorMenuInlineKeyboardForTopic(kind string) *InlineKeyboardMarkup {
	rows := operatorMenuRowsForKind(kind)
	if rows == nil {
		return nil
	}
	out := make([][]InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		btns := make([]InlineKeyboardButton, 0, len(row))
		for _, btn := range row {
			btns = append(btns, InlineKeyboardButton{
				Text:         btn.text,
				CallbackData: "compat_btn:0:" + btn.code,
			})
		}
		out = append(out, btns)
	}
	return &InlineKeyboardMarkup{InlineKeyboard: out}
}

// keyboardRowsForKind is the single source of truth for the per-topic button
// layout — both the reply-keyboard and the compat inline-keyboard derive
// from these rows so they stay byte-identical to the user.
func keyboardRowsForKind(kind string) [][]string {
	switch kind {
	case "per_router":
		return labelRows(routerMenuItems, 2)
	case "summary", "systemic":
		return labelRows(fleetMenuItems, 2)
	}
	return nil
}

type operatorMenuButton struct {
	text string
	code string
}

func operatorMenuRowsForKind(kind string) [][]operatorMenuButton {
	switch kind {
	case "per_router":
		return buttonRows(routerMenuItems, 2)
	case "summary", "systemic":
		return buttonRows(fleetMenuItems, 2)
	}
	return nil
}

// compatBtnCodes maps a button label to a stable short code used in
// callback_data (TG limit 64 bytes). Reverse lookup via CompatBtnTextByCode.
var compatBtnCodes = map[string]string{
	"🩺 Домашний роутер": "router_doctor",
}

func init() {
	for _, item := range append(cloneMenuItems(routerMenuItems), fleetMenuItems...) {
		if item.Label != "" && item.Code != "" {
			compatBtnCodes[item.Label] = item.Code
		}
	}
}

func compatBtnCodeFor(label string) (string, bool) {
	c, ok := compatBtnCodes[label]
	return c, ok
}

// CompatBtnTextByCode is the reverse lookup used by the callback router to
// turn a "compat_btn:0:<code>" tap back into the original button label so
// it can be dispatched through handleNonCommandMessage as if the user had
// typed the text.
func CompatBtnTextByCode(code string) string {
	for _, item := range append(cloneMenuItems(routerMenuItems), fleetMenuItems...) {
		if item.Code == code {
			return item.Label
		}
	}
	for label, c := range compatBtnCodes {
		if c == code {
			return label
		}
	}
	return ""
}

func labelRows(items []BotMenuItem, perRow int) [][]string {
	var rows [][]string
	var row []string
	for _, item := range items {
		if item.Label == "" {
			continue
		}
		row = append(row, item.Label)
		if len(row) >= perRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

func buttonRows(items []BotMenuItem, perRow int) [][]operatorMenuButton {
	var rows [][]operatorMenuButton
	var row []operatorMenuButton
	for _, item := range items {
		if item.Label == "" || item.Code == "" {
			continue
		}
		row = append(row, operatorMenuButton{text: item.Label, code: item.Code})
		if len(row) >= perRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}
