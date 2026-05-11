package callbacks

import (
	"context"
	"log/slog"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

// adminPanelOpen posts the hub Home screen. Called from handleAdminCommand
// on /panel. Admin gate is the existing m.From.ID == cfg.AdminUserID check
// in HandleMessage — no extra auth here.
func (r *Router) adminPanelOpen(ctx context.Context, m *tg.Message) {
	text, kb := panelHomeMessage()
	if _, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, &kb); err != nil {
		slog.Warn("panel open send failed", "err", err)
	}
}

// panelHomeMessage builds the (text, inline-kb) for the hub Home screen.
// Pure function — easy to test.
func panelHomeMessage() (string, tg.InlineKeyboardMarkup) {
	text := "🎛 Панель управления\n\nЧто открыть?"
	kb := tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{
				{Text: "🛠 Maintenance", CallbackData: "panel:0:kind:maint"},
				{Text: "📦 Routes", CallbackData: "panel:0:kind:routes"},
			},
			{
				{Text: "📊 Status", CallbackData: "panel:0:kind:status"},
				{Text: "🪄 Оживить топики", CallbackData: "panel:0:awaken_confirm"},
			},
			{
				{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
			},
		},
	}
	return text, kb
}
