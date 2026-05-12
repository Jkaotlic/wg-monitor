package callbacks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/db"
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
				{Text: "👥 Доступ", CallbackData: "access:0:home"},
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
	case "push":
		r.panelHandlePush(ctx, q, args)
	case "no_topic":
		r.panelHandleNoTopic(ctx, q, args)
	case "awaken_confirm":
		r.panelAwakenConfirm(ctx, q)
	case "awaken_do":
		r.panelAwakenDo(ctx, q)
	default:
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "screen TBA")
	}
}

// panelHandlePush publishes the chosen panel kind into the target router's
// per_router topic via a synthetic tg.Message, then edits the hub message
// to the result screen. The synthetic-Message pattern mirrors compat_btn
// (callbacks/compat_inline.go) — MessageID=0 marks the message as
// synthetic so the user-message deletion branch in HandleMessage skips.
func (r *Router) panelHandlePush(ctx context.Context, q *tg.CallbackQuery, args Args) {
	u, err := r.d.Users().GetByID(args.UserID)
	if err != nil || u == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	if u.TelegramThreadID == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "у роутера нет топика")
		return
	}
	threadID := *u.TelegramThreadID
	synth := &tg.Message{
		MessageID:       0, // sentinel: skip user-message deletion
		Chat:            q.Message.Chat,
		From:            q.From,
		MessageThreadID: &threadID,
	}
	publishErr := r.panelPublish(ctx, synth, u, args.PanelKind)
	kindLabel := map[string]string{"maint": "Maintenance", "routes": "Routes", "status": "Status"}[args.PanelKind]
	var resultText string
	switch {
	case publishErr == nil:
		resultText = fmt.Sprintf("🎛 Панель управления\n\n✅ %s отправлен в топик @%s.", kindLabel, u.Nickname)
	case tg.IsTopicNotFound(publishErr):
		resultText = fmt.Sprintf("🎛 Панель управления\n\n❌ Топик роутера @%s похоже удалён. Сделай /recreate_topic внутри его топика или /ensure_topics.", u.Nickname)
	default:
		resultText = fmt.Sprintf("🎛 Панель управления\n\n❌ Не удалось опубликовать %s в @%s: %v", kindLabel, u.Nickname, publishErr)
	}
	kb := panelResultKb()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, resultText, "", &kb); err != nil {
		slog.Warn("panel push result edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

// panelPublish dispatches to the appropriate kind-specific builder. Each
// builder posts a fresh message in the target topic (via the synthetic
// Message) and enqueues any associated agent command. To surface
// stale-topic errors synchronously (the builders log errors internally
// rather than returning them), we do a cheap probe SendMessage first;
// the panel will then overwrite/replace it normally.
func (r *Router) panelPublish(ctx context.Context, m *tg.Message, u *db.User, kind string) error {
	probeMsg := fmt.Sprintf("🎛 %s готовится…", map[string]string{"maint": "Maintenance", "routes": "Routes", "status": "Status"}[kind])
	if _, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, probeMsg, "", nil); err != nil {
		return err
	}
	switch kind {
	case "maint":
		r.openMaintPanelMessage(ctx, m, u)
	case "routes":
		r.openRoutesPanelMessage(ctx, m, u)
	case "status":
		r.dispatchSmartReply(ctx, m, u)
	default:
		return fmt.Errorf("unknown panel kind: %q", kind)
	}
	return nil
}

func panelResultKb() tg.InlineKeyboardMarkup {
	return tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{
				{Text: "« К панели", CallbackData: "panel:0:home"},
				{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
			},
		},
	}
}

func (r *Router) panelHandleNoTopic(ctx context.Context, q *tg.CallbackQuery, _ Args) {
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "У роутера нет топика — сделай /ensure_topics")
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

// panelAwakenConfirm renders the "Оживить топики" confirmation screen
// showing the count of routers that have a topic and will receive a
// welcome message. Two-button kb: Подтвердить (panel:0:awaken_do) /
// « Назад (panel:0:home).
func (r *Router) panelAwakenConfirm(ctx context.Context, q *tg.CallbackQuery) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось прочитать роутеров")
		return
	}
	var count int
	for _, u := range users {
		if u.TelegramThreadID != nil {
			count++
		}
	}
	text := fmt.Sprintf("🎛 Панель управления\n\n🪄 Оживить топики (отправить приветствие с кнопками во все per_router топики)\n\nБудут затронуты: %d топика", count)
	kb := tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{
				{Text: "✓ Подтвердить", CallbackData: "panel:0:awaken_do"},
				{Text: "« Назад", CallbackData: "panel:0:home"},
			},
		},
	}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel awaken confirm edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) panelAwakenDo(ctx context.Context, q *tg.CallbackQuery) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось прочитать роутеров")
		return
	}
	start := time.Now()
	var sent, failed int
	var failLines []string
	const sleep = 200 * time.Millisecond
	first := true
	for _, u := range users {
		if u.TelegramThreadID == nil {
			continue
		}
		if !first {
			// Use a timer + select so a context cancellation is honoured
			// immediately rather than waiting out the full 200 ms sleep.
			t := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				t.Stop()
				goto done
			case <-t.C:
			}
		}
		first = false
		if werr := alerts.SendWelcome(ctx, r.tg, r.cfg.ChatID, *u.TelegramThreadID, u.Nickname, r.cfg.UI.KeyboardForTopic("per_router")); werr != nil {
			failed++
			failLines = append(failLines, fmt.Sprintf("❌ %s: %v", u.Nickname, werr))
			continue
		}
		sent++
	}
done:
	slog.Info("panel awaken", "sent", sent, "failed", failed, "elapsed_ms", time.Since(start).Milliseconds())
	var b strings.Builder
	fmt.Fprintf(&b, "🎛 Панель управления\n\n✅ Оживлено: %d топиков, %d ошибок.", sent, failed)
	for _, line := range failLines {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	text := b.String()
	if len(text) > 4096 {
		text = text[:4093] + "..."
	}
	kb := panelResultKb()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel awaken result edit failed", "err", err)
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
