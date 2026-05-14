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

// DiagFailingTest carries the minimum needed to render a drill-down button.
type DiagFailingTest struct {
	ID    string // slug — fits in callback_data
	Label string // human RU label, may be long
}

// DiagResultKeyboardWithTests is the failing-test-aware successor to
// DiagResultKeyboard. When failing is non-empty AND status == "ok",
// prepends a row of per-test drill-down buttons. status / userID /
// rawToken behave as in the original.
func DiagResultKeyboardWithTests(status string, userID int64, rawToken string, failing []DiagFailingTest) InlineKeyboardMarkup {
	rows := [][]InlineKeyboardButton{}
	const maxPerRow = 8
	if len(failing) > 0 && status == "ok" {
		var row []InlineKeyboardButton
		for _, f := range failing {
			row = append(row, InlineKeyboardButton{
				Text:         "❌ " + truncRunes(f.Label, 16),
				CallbackData: fmt.Sprintf("diag_test:%d:%s:%s", userID, rawToken, f.ID),
			})
			if len(row) >= maxPerRow {
				rows = append(rows, row)
				row = nil
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	// Append the original layout
	orig := DiagResultKeyboard(status, userID, rawToken)
	rows = append(rows, orig.InlineKeyboard...)
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

// truncRunes caps a string at n runes, suffixing "…" if truncated.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
