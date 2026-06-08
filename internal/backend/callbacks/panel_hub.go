package callbacks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
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
	text := "🎛 Панель управления\n\nЧто открыть?\n\nРоутер:\n  • выбери раздел\n  • затем конкретный роутер\n\nФлот:\n  • массовая проверка\n  • топики и доступы"
	return text, tg.AdminPanelHomeKeyboard()
}

// handlePanelCallback is the top-level dispatcher for panel:* callbacks.
// Routed from Router.HandleCallback after the panel admin gate. Each screen runs as
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
	case "doctor_all":
		r.startFleetCommand(ctx, q, "Router check", "router_doctor", nil)
	case "audit_all":
		r.panelAuditAll(ctx, q)
	case "update_all_confirm":
		r.panelUpdateAllConfirm(ctx, q)
	case "update_all_do":
		r.panelUpdateAllDo(ctx, q)
	case "mobile":
		r.panelMobileFleet(ctx, q)
	case "help":
		r.panelHandleHelp(ctx, q, args)
	default:
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "экран ещё не готов")
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
	targetChat := q.Message.Chat
	if r.cfg.ChatID != 0 {
		targetChat.ID = r.cfg.ChatID
	}
	synth := &tg.Message{
		MessageID:       0, // sentinel: skip user-message deletion
		Chat:            targetChat,
		From:            q.From,
		MessageThreadID: &threadID,
	}
	publishErr := r.panelPublish(ctx, synth, u, args.PanelKind)
	kindLabel := panelKindLabel(args.PanelKind)
	var resultText string
	switch {
	case publishErr == nil:
		resultText = alerts.Card{
			Badge:   "✅",
			Label:   "🎛 Панель управления",
			Summary: "панель отправлена",
			Meta:    []string{alerts.KV("раздел", kindLabel), alerts.KV("роутер", "@"+u.Nickname)},
		}.Render(alerts.CardOpts{})
	case tg.IsTopicNotFound(publishErr):
		resultText = alerts.Card{
			Badge:   "❌",
			Label:   "🎛 Панель управления",
			Summary: "топик роутера похоже удалён",
			Meta:    []string{alerts.KV("роутер", "@"+u.Nickname)},
			Hint:    "Сделай /recreate_topic внутри его топика или /ensure_topics.",
		}.Render(alerts.CardOpts{})
	default:
		sum, hint := alerts.HintFor("panel_push", publishErr.Error())
		card := alerts.Card{Badge: "❌", Label: fmt.Sprintf("Не удалось опубликовать %s в @%s", kindLabel, u.Nickname), Summary: sum, Hint: hint}
		resultText = "🎛 Панель управления\n\n" + card.Render(alerts.CardOpts{MaxBytes: 3500})
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
	probeMsg := fmt.Sprintf("🎛 %s готовится…", panelKindLabel(kind))
	probeMID, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, probeMsg, "", nil)
	if err != nil {
		return err
	}
	switch kind {
	case "maint":
		r.openMaintPanelMessage(ctx, m, u)
	case "routes":
		r.openRoutesPanelMessage(ctx, m, u)
	case "tunnels":
		r.openTunnelsPanelMessage(ctx, m, u)
	case "status":
		r.dispatchSmartReply(ctx, m, u)
	case "pingcheck":
		r.openPingCheckPanelMessage(ctx, m, u)
	case "doctor":
		if r.command == nil {
			return fmt.Errorf("command action is not configured")
		}
		return r.command.DispatchFromMessage(ctx, "router_doctor", u.ID, m.Chat.ID, probeMID, m.MessageThreadID)
	default:
		return fmt.Errorf("unknown panel kind: %q", kind)
	}
	return nil
}

func (r *Router) panelDoctorAll(ctx context.Context, q *tg.CallbackQuery) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось прочитать роутеры")
		return
	}
	if r.command == nil || r.cmdSink == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "command channel не настроен")
		return
	}
	var queued, noTopic int
	var failLines []string
	targetChatID := q.Message.Chat.ID
	if r.cfg.ChatID != 0 {
		targetChatID = r.cfg.ChatID
	}
	for _, u := range users {
		if u.TelegramThreadID == nil {
			noTopic++
			continue
		}
		ackText := "⏳ Проверяю роутер изнутри: awg-manager, туннели, pingcheck и процессы…"
		ackMID, sendErr := r.tg.SendMessageWithReplyKeyboard(ctx, targetChatID, u.TelegramThreadID, ackText, "", nil, r.cfg.UI.KeyboardForTopic("per_router"))
		if sendErr != nil {
			failLines = append(failLines, fmt.Sprintf("❌ %s: %v", u.Nickname, sendErr))
			continue
		}
		if err := r.command.DispatchFromMessage(ctx, "router_doctor", u.ID, targetChatID, ackMID, u.TelegramThreadID); err != nil {
			failLines = append(failLines, fmt.Sprintf("❌ %s: %v", u.Nickname, err))
			continue
		}
		queued++
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🎛 Панель управления\n\n🩺 Проверка всех роутеров\n\nИтог:\n  • ✅ Поставлено в очередь: %d", queued)
	if noTopic > 0 {
		fmt.Fprintf(&b, "\n  • ⚠ Без топика: %d", noTopic)
	}
	if len(failLines) > 0 {
		fmt.Fprintf(&b, "\n  • ❌ Ошибок: %d", len(failLines))
		b.WriteString("\n\nОшибки:")
		for _, line := range failLines {
			b.WriteString("\n  ")
			b.WriteString(line)
		}
	}
	if queued > 0 {
		b.WriteString("\n\nЧто будет дальше:\n  • Результаты придут ответами в соответствующие топики.")
	}
	text := b.String()
	if len(text) > 4096 {
		text = text[:4093] + "..."
	}
	kb := panelResultKb()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel doctor all result edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) panelAuditAll(ctx context.Context, q *tg.CallbackQuery) {
	r.startFleetCommand(ctx, q, "Version audit", "version_audit", nil)
}

func (r *Router) panelUpdateAllConfirm(ctx context.Context, q *tg.CallbackQuery) {
	version := strings.TrimSpace(r.cfg.BackendVersion)
	if version == "" || version == "dev" || version == "unknown" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "backend version is unknown")
		return
	}
	text := fmt.Sprintf("Fleet: update all routers\n\nTarget: %s\n\nOnly agents with confirmed self_update fix (>= v0.13.0-rc32) will be queued. Older agents will be skipped in the report.", version)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{{Text: "Подтвердить", CallbackData: "panel:0:update_all_do"}, {Text: "Назад", CallbackData: "panel:0:home"}},
	}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel update all confirm edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) panelUpdateAllDo(ctx context.Context, q *tg.CallbackQuery) {
	version := strings.TrimSpace(r.cfg.BackendVersion)
	r.startFleetCommand(ctx, q, "Self-update to "+version, "self_update", r.selfUpdateArgs(version))
}

func (r *Router) selfUpdateArgs(version string) map[string]any {
	args := map[string]any{"version": version}
	if base := strings.TrimRight(strings.TrimSpace(r.cfg.PublicBaseURL), "/"); base != "" {
		args["repo_base"] = base + "/v1/releases/download"
	}
	return args
}

func (r *Router) startFleetCommand(ctx context.Context, q *tg.CallbackQuery, title, action string, args map[string]any) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "failed to read routers")
		return
	}
	if r.cmdSink == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "command channel is not configured")
		return
	}
	batch := &fleetBatch{
		ID:        newFleetBatchID(),
		Title:     title,
		Action:    action,
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
		CreatedAt: time.Now(),
		Items:     make(map[string]*fleetBatchItem),
	}
	for _, u := range users {
		item := &fleetBatchItem{Nick: u.Nickname, Status: "queued", Summary: "queued"}
		batch.Items[u.Nickname] = item
		batch.Order = append(batch.Order, u.Nickname)
		if action == "self_update" {
			installed := ""
			if u.LastDeployedVersion != nil {
				installed = *u.LastDeployedVersion
			}
			if !isSafeSelfUpdateVersion(installed) {
				item.Status = "skipped"
				item.Summary = "installed version is too old for safe self_update: " + installed
				continue
			}
		}
		cmd := fleetCommand(action, args)
		ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID, BulkID: batch.ID, BulkNick: u.Nickname}
		if err := r.cmdSink.EnqueueWithRef(u.ID, cmd, ref); err != nil {
			item.Status = "failed"
			item.Summary = err.Error()
		}
	}
	r.fleetBatches.start(batch)
	kb := panelResultKb()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, batch.render(), "", &kb); err != nil {
		slog.Warn("panel fleet command edit failed", "err", err, "action", action)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
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

func (r *Router) panelMobileFleet(ctx context.Context, q *tg.CallbackQuery) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось прочитать роутеры")
		return
	}
	rows := make([]tg.MobileFleetRow, 0, len(users))
	for _, u := range users {
		row := tg.MobileFleetRow{
			Nickname: u.Nickname,
			Kind:     u.Kind,
		}
		if u.LastSeenAt != nil {
			ts := *u.LastSeenAt
			row.LastSeenAt = &ts
		}
		if u.LastDeployedVersion != nil {
			row.LastDeployedVersion = *u.LastDeployedVersion
		}
		if u.Ring != nil {
			row.Ring = *u.Ring
		}
		if u.PendingVersion != nil {
			row.PendingVersion = *u.PendingVersion
		}
		rows = append(rows, row)
	}
	text := tg.MobileFleetPanelText(rows, time.Now())
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{{Text: "« Назад", CallbackData: "panel:0:home"}, {Text: "✖ Закрыть", CallbackData: "panel:0:close"}},
	}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("panel mobile edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
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

// panelHandleHelp renders a static help body for the requested panel screen.
// « Назад returns to the kind-pick screen (for maint/routes/status) or to home;
// ✖ Закрыть clears the keyboard.
func (r *Router) panelHandleHelp(ctx context.Context, q *tg.CallbackQuery, args Args) {
	body := tg.HelpForScreen(args.PanelKind)
	if !r.isAdminTG(q.From.ID) {
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{
				{Text: "✖ Закрыть", CallbackData: "close_panel:0:_panel_"},
			},
		}}
		if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, body, "", &kb); err != nil {
			slog.Warn("panel help edit failed", "err", err)
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
		return
	}
	var backCB string
	switch args.PanelKind {
	case "maint", "routes", "tunnels", "status", "pingcheck", "doctor":
		backCB = "panel:0:kind:" + args.PanelKind
	default:
		backCB = "panel:0:home"
	}
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{
			{Text: "« Назад", CallbackData: backCB},
			{Text: "✖ Закрыть", CallbackData: "panel:0:close"},
		},
	}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, body, "", &kb); err != nil {
		slog.Warn("panel help edit failed", "err", err)
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
// visible menu card. Two-button kb: Подтвердить (panel:0:awaken_do) /
// « Назад (panel:0:home).
func (r *Router) panelAwakenConfirm(ctx context.Context, q *tg.CallbackQuery) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось прочитать роутеров")
		return
	}
	var count, skippedNoTopic int
	for _, u := range users {
		if u.TelegramThreadID != nil {
			count++
		} else {
			skippedNoTopic++
		}
	}
	text := fmt.Sprintf("🎛 Панель управления\n\n🪄 Оживить топики\n\nЧто будет:\n  • отправим видимое меню роутера во все per_router топики\n  • Будут затронуты: %d топика\n  • Без топика: %d", count, skippedNoTopic)
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
	var sent, failed, skippedNoTopic int
	var failLines []string
	const sleep = 200 * time.Millisecond
	first := true
	for _, u := range users {
		if u.TelegramThreadID == nil {
			skippedNoTopic++
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
		if werr := alerts.SendWelcomeRoleMenu(ctx, r.tg, r.cfg.ChatID, *u.TelegramThreadID, u.Nickname, tg.ReplyKeyboardForTopic("per_router"), tg.OperatorMenuInlineKeyboardForTopic("per_router")); werr != nil {
			failed++
			failLines = append(failLines, fmt.Sprintf("❌ %s: %v", u.Nickname, werr))
			continue
		}
		sent++
	}
done:
	slog.Info("panel awaken", "sent", sent, "failed", failed, "skipped_no_topic", skippedNoTopic, "elapsed_ms", time.Since(start).Milliseconds())
	var b strings.Builder
	fmt.Fprintf(&b, "🎛 Панель управления\n\n🪄 Оживить топики\n\nИтог:\n  • ✅ Оживлено: %d\n  • ❌ Ошибок: %d\n  • Без топика: %d", sent, failed, skippedNoTopic)
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
	kindLabel := panelKindLabel(kind)
	if kindLabel == "" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "неизвестный раздел")
		return
	}
	if len(users) == 0 {
		text := "🎛 " + kindLabel + "\n\nВыбери роутер:\n  • Роутеров нет\n  • Сначала добавь — wizard или CLI `add-user`"
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
	text := "🎛 " + kindLabel + "\n\nВыбери роутер:\n  • " + strings.Join(userLines, "\n  • ")
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

func panelKindLabel(kind string) string {
	return tg.AdminPanelKindLabel(kind)
}
