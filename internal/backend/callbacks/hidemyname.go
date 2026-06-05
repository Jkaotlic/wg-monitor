package callbacks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/hidemy"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

const hideMyPageSize = 10

func (r *Router) handleHideMyCodeMessage(ctx context.Context, m *tg.Message, kind string, user *db.User) bool {
	code := strings.TrimSpace(m.Text)
	if !hidemy.ValidAccessCode(code) {
		return false
	}
	if kind != "per_router" || user == nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"HideMy.name code нужно прислать в топик конкретного роутера.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return true
	}
	client := hidemy.New(r.cfg.HideMyBaseURL)
	if _, err := client.ServerList(ctx, code); err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"Не удалось проверить HideMy.name code: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return true
	}
	stored, err := r.addHideMyCode(user.ID, code)
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"Не удалось сохранить HideMy.name code: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return true
	}
	if m.MessageID != 0 {
		_ = r.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
	}
	r.sendHideMyCodePanel(ctx, m.Chat.ID, m.MessageThreadID, nil, user, stored.ID, 0)
	return true
}

func (r *Router) sendHideMyPremiumPanel(ctx context.Context, chatID int64, threadID *int64, replyTo *int64, user *db.User) {
	codes, err := r.listHideMyCodes(user.ID)
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, chatID, threadID, "Не удалось прочитать HideMy.name codes: "+err.Error(), "", replyTo, r.cfg.UI.KeyboardForTopic("per_router"))
		return
	}
	text, kb := hideMyCodeListView(user, codes)
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, chatID, threadID, text, "", replyTo, &kb)
}

func hideMyCodeListView(user *db.User, codes hideMyCodes) (string, tg.InlineKeyboardMarkup) {
	text := "HideMy.name - " + user.Nickname + "\n\n"
	if len(codes.Codes) == 0 {
		text += "Кодов пока нет.\n\nПришли сюда цифровой HideMy.name access code: я проверю его, сохраню за этим топиком и удалю сообщение с кодом."
		return text, tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{premiumHelpRow()}}
	}
	text += fmt.Sprintf("Сохранено кодов: %d\nПришли ещё один цифровой код, чтобы добавить его в этот топик.", len(codes.Codes))
	rows := make([][]tg.InlineKeyboardButton, 0, len(codes.Codes)+1)
	for _, code := range codes.Codes {
		label := code.Label
		if code.ID == codes.ActiveID {
			label += " *"
		}
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text:         "Открыть " + label,
			CallbackData: fmt.Sprintf("hmn_open:%d:_panel_:%s", user.ID, code.ID),
		}, {
			Text:         "Удалить",
			CallbackData: fmt.Sprintf("hmn_delete:%d:_panel_:%s", user.ID, code.ID),
		}})
	}
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text:         "Обновить список",
		CallbackData: fmt.Sprintf("hmn_refresh:%d:_panel_", user.ID),
	}})
	rows = append(rows, premiumHelpRow())
	return text, tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func hideMyImportQueuedView(userID int64, codeID, docWarn string) (string, tg.InlineKeyboardMarkup) {
	text := "HideMy.name AmneziaWG 2.0 config выгружен в топик. Импорт туннеля поставлен в очередь роутера." + docWarn +
		"\n\nДальше:\n1. Нажми «Проверить тоннели» и дождись живого списка.\n2. Если правила были на старом туннеле, открой «Маршруты / перенос»."
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🎛 Проверить тоннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
	}, {
		{Text: "🛣 Маршруты / перенос", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
	}, {
		{Text: "🌐 К серверам", CallbackData: fmt.Sprintf("hmn_open:%d:_panel_:%s", userID, codeID)},
	}}}
	return text, kb
}

func (r *Router) sendHideMyCodePanel(ctx context.Context, chatID int64, threadID *int64, replyTo *int64, user *db.User, codeID string, page int) {
	text, kb, err := r.hideMyServerListView(ctx, user, codeID, page)
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, chatID, threadID, "HideMy.name error: "+err.Error(), "", replyTo, r.cfg.UI.KeyboardForTopic("per_router"))
		return
	}
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, chatID, threadID, text, "", replyTo, &kb)
}

func (r *Router) hideMyServerListView(ctx context.Context, user *db.User, codeID string, page int) (string, tg.InlineKeyboardMarkup, error) {
	stored, ok := r.hideMyStoredCode(user.ID, codeID)
	if !ok {
		return "", tg.InlineKeyboardMarkup{}, fmt.Errorf("code not saved")
	}
	client := hidemy.New(r.cfg.HideMyBaseURL)
	servers, err := client.ServerList(ctx, stored.AccessCode)
	if err != nil {
		return "", tg.InlineKeyboardMarkup{}, err
	}
	if page < 0 {
		page = 0
	}
	pages := (len(servers) + hideMyPageSize - 1) / hideMyPageSize
	if pages == 0 {
		pages = 1
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * hideMyPageSize
	end := start + hideMyPageSize
	if end > len(servers) {
		end = len(servers)
	}
	text := fmt.Sprintf("HideMy.name - %s / %s\n\nAmneziaWG 2.0 config (.conf)\nСерверов доступно: %d\nСтраница: %d/%d", user.Nickname, stored.Label, len(servers), page+1, pages)
	rows := make([][]tg.InlineKeyboardButton, 0, hideMyPageSize+3)
	for _, server := range servers[start:end] {
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text:         server.Name,
			CallbackData: fmt.Sprintf("hmn_dl:%d:_panel_:%s:%s", user.ID, stored.ID, server.ID),
		}})
	}
	nav := []tg.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, tg.InlineKeyboardButton{Text: "Назад", CallbackData: fmt.Sprintf("hmn_page:%d:_panel_:%s:%d", user.ID, stored.ID, page-1)})
	}
	if page+1 < pages {
		nav = append(nav, tg.InlineKeyboardButton{Text: "Дальше", CallbackData: fmt.Sprintf("hmn_page:%d:_panel_:%s:%d", user.ID, stored.ID, page+1)})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: "К списку кодов", CallbackData: fmt.Sprintf("hmn_refresh:%d:_panel_", user.ID)},
		{Text: "Удалить код", CallbackData: fmt.Sprintf("hmn_delete:%d:_panel_:%s", user.ID, stored.ID)},
	})
	rows = append(rows, premiumHelpRow())
	return text, tg.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func (r *Router) handleHideMyRefresh(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	codes, err := r.listHideMyCodes(user.ID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ошибка кодов")
		return
	}
	text, kb := hideMyCodeListView(user, codes)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обновлено")
}

func (r *Router) handleHideMyOpen(ctx context.Context, q *tg.CallbackQuery, args Args) {
	r.handleHideMyPage(ctx, q, args)
}

func (r *Router) handleHideMyPage(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	text, kb, err := r.hideMyServerListView(ctx, user, args.HideMyCodeID, args.HideMyPage)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleHideMyDeleteAsk(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	stored, ok := r.hideMyStoredCode(user.ID, args.HideMyCodeID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "код не найден")
		return
	}
	text := fmt.Sprintf("Удалить %s из HideMy.name этого топика?\n\nЯ удалю только сохранение в боте.", stored.Label)
	tok := r.putPendingConfirm(q, user.ID, "hmn_delete_confirm", args.HideMyCodeID)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "Да, удалить", CallbackData: fmt.Sprintf("hmn_delete_confirm:%d:_panel_:%s:%s", user.ID, args.HideMyCodeID, tok)},
	}, {
		{Text: "Назад", CallbackData: fmt.Sprintf("hmn_refresh:%d:_panel_", user.ID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleHideMyDeleteConfirm(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	if !r.consumePendingConfirm(ctx, q, args, args.HideMyCodeID) {
		return
	}
	if err := r.deleteHideMyCode(user.ID, args.HideMyCodeID); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	codes, err := r.listHideMyCodes(user.ID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ошибка кодов")
		return
	}
	text, kb := hideMyCodeListView(user, codes)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "код удалён")
}

func (r *Router) handleHideMyDownloadAsk(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	stored, ok := r.hideMyStoredCode(user.ID, args.HideMyCodeID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "код не сохранён")
		return
	}
	server, err := r.hideMyServerByID(ctx, stored.AccessCode, args.HideMyServerID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	text := fmt.Sprintf("Выпустить HideMy.name AmneziaWG 2.0 .conf для %s?\n\nПосле скачивания я поставлю импорт туннеля в очередь роутера.", server.Name)
	tok := r.putPendingConfirm(q, user.ID, "hmn_dl_confirm", args.HideMyCodeID+":"+args.HideMyServerID)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "Да, выпустить", CallbackData: fmt.Sprintf("hmn_dl_confirm:%d:_panel_:%s:%s:%s", user.ID, args.HideMyCodeID, args.HideMyServerID, tok)},
	}, {
		{Text: "Назад", CallbackData: fmt.Sprintf("hmn_open:%d:_panel_:%s", user.ID, args.HideMyCodeID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleHideMyDownloadConfirm(ctx context.Context, q *tg.CallbackQuery, args Args) {
	if r.cmdSink == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "command queue не подключена")
		return
	}
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	if !r.consumePendingConfirm(ctx, q, args, args.HideMyCodeID+":"+args.HideMyServerID) {
		return
	}
	stored, ok := r.hideMyStoredCode(user.ID, args.HideMyCodeID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "код не сохранён")
		return
	}
	server, err := r.hideMyServerByID(ctx, stored.AccessCode, args.HideMyServerID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	conf, err := r.downloadHideMyConfig(ctx, stored.AccessCode, server.IP)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	docWarn := ""
	filename := "hidemy_" + safeConfigSlug(server.Name) + ".conf"
	if err := r.sendConfigDocument(ctx, q.Message.Chat.ID, q.Message.MessageThreadID, filename, conf, "HideMy.name AmneziaWG 2.0 "+server.Name); err != nil {
		docWarn = "\n\nФайл в чат не отправился: " + shortToast(err)
	}
	cmd := wire.Command{
		ID:     defaultCmdID(),
		Action: "tunnel_import",
		Args: map[string]any{
			"conf":    base64.StdEncoding.EncodeToString(conf),
			"name":    "hidemy_" + args.HideMyServerID,
			"replace": true,
			"backend": "nativeWG",
		},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID, Action: "tunnel_import"}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "конфиг выгружен, импорт в очереди")
	text, kb := hideMyImportQueuedView(user.ID, args.HideMyCodeID, docWarn)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

func (r *Router) hideMyServerByID(ctx context.Context, accessCode, serverID string) (hidemy.Server, error) {
	client := hidemy.New(r.cfg.HideMyBaseURL)
	servers, err := client.ServerList(ctx, accessCode)
	if err != nil {
		return hidemy.Server{}, err
	}
	server, ok := hidemy.FindServer(servers, serverID)
	if !ok {
		return hidemy.Server{}, fmt.Errorf("hidemy server not found")
	}
	return server, nil
}

func (r *Router) downloadHideMyConfig(ctx context.Context, accessCode, serverIP string) ([]byte, error) {
	client := hidemy.New(r.cfg.HideMyBaseURL)
	conf, err := client.DownloadAmneziaWG20Config(ctx, accessCode, serverIP)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(conf), "[Interface]") || !strings.Contains(string(conf), "[Peer]") {
		return nil, errors.New("downloaded config does not look like WireGuard conf")
	}
	return conf, nil
}
