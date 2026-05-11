package callbacks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
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

// handlePanelCallback is the top-level dispatcher for panel:* callbacks.
// Routed from Router.HandleCallback after aclAllow. Each screen runs as
// an EditMessageText on the hub message; new messages (panel publication
// into per_router topic) are sent separately and don't touch the hub.
func (r *Router) handlePanelCallback(ctx context.Context, q *tg.CallbackQuery, args Args) {
	slog.Info("panel callback", "screen", args.PanelScreen, "kind", args.PanelKind, "from", q.From.ID, "user_id", args.UserID)
	switch args.PanelScreen {
	case "home":
		r.panelEditToHome(ctx, q)
	case "kind":
		r.panelEditToKindPick(ctx, q, args.PanelKind)
	case "close":
		r.panelClose(ctx, q)
	default:
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "screen TBA")
	}
}

func (r *Router) panelEditToHome(ctx context.Context, q *tg.CallbackQuery) {
	text, kb := panelHomeMessage()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel home edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) panelClose(ctx context.Context, q *tg.CallbackQuery) {
	empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "🎛 Панель закрыта.", "", &empty); err != nil {
		slog.Warn("panel close edit failed (non-fatal)", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

// panelEditToKindPick renders the router selection screen for the chosen
// kind. Users without TelegramThreadID render with a ⚠ prefix and a
// no_topic callback that toasts an explanation.
func (r *Router) panelEditToKindPick(ctx context.Context, q *tg.CallbackQuery, kind string) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось прочитать роутеров")
		slog.Warn("panel kind pick: users list failed", "err", err)
		return
	}
	kindLabel := map[string]string{"maint": "Maintenance", "routes": "Routes", "status": "Status"}[kind]
	if kindLabel == "" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "unknown kind")
		return
	}
	if len(users) == 0 {
		text := "🎛 " + kindLabel + " → выбери роутер:\n\nРоутеров нет. Сначала добавь — wizard или CLI `add-user`."
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "« Назад", CallbackData: "panel:0:home"}, {Text: "✖ Закрыть", CallbackData: "panel:0:close"}},
		}}
		_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
		return
	}
	var userLines []string
	rows := make([][]tg.InlineKeyboardButton, 0, len(users)+1)
	for _, u := range users {
		if u.TelegramThreadID != nil {
			userLines = append(userLines, u.Nickname)
			rows = append(rows, []tg.InlineKeyboardButton{
				{Text: u.Nickname, CallbackData: fmt.Sprintf("panel:%d:push:%s", u.ID, kind)},
			})
			continue
		}
		userLines = append(userLines, "⚠ "+u.Nickname)
		rows = append(rows, []tg.InlineKeyboardButton{
			{Text: "⚠ " + u.Nickname + " (нет топика)", CallbackData: fmt.Sprintf("panel:%d:no_topic", u.ID)},
		})
	}
	text := "🎛 " + kindLabel + " → выбери роутер:\n" + strings.Join(userLines, "\n")
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: "« Назад", CallbackData: "panel:0:home"},
		{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
	})
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: rows}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel kind pick edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}
