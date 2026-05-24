package callbacks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/amnezia"
	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const amneziaCountryPageSize = 10

func (r *Router) handleAmneziaKeyMessage(ctx context.Context, m *tg.Message, kind string, user *db.User) bool {
	key := strings.TrimSpace(m.Text)
	if !strings.HasPrefix(key, "vpn://") {
		return false
	}
	if kind != "per_router" || user == nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"Amnezia Premium key нужно прислать в топик конкретного роутера.", "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return true
	}
	client := amnezia.New(r.cfg.AmneziaBaseURL)
	if err := client.Login(ctx, key); err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"Не удалось проверить Amnezia Premium key: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return true
	}
	info, err := client.AccountInfo(ctx)
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"Ключ принят, но кабинет не ответил: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return true
	}
	stored, err := r.addAmneziaKey(user.ID, key)
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			"Не удалось сохранить Amnezia Premium key: "+err.Error(), "", nil, r.cfg.UI.KeyboardForTopic(kind))
		return true
	}
	if m.MessageID != 0 {
		_ = r.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
	}
	r.sendAmneziaAccount(ctx, m.Chat.ID, m.MessageThreadID, nil, user, stored, info)
	return true
}

func (r *Router) sendAmneziaPremiumPanel(ctx context.Context, chatID int64, threadID *int64, replyTo *int64, user *db.User) {
	keys, err := r.listAmneziaKeys(user.ID)
	if err != nil {
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, chatID, threadID, "Не удалось прочитать Amnezia key: "+err.Error(), "", replyTo, r.cfg.UI.KeyboardForTopic("per_router"))
		return
	}
	text, kb := amneziaKeyListView(user, keys)
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, chatID, threadID, text, "", replyTo, &kb)
}

func (r *Router) fetchAmneziaAccount(ctx context.Context, key string) (*amnezia.AccountInfo, error) {
	client := amnezia.New(r.cfg.AmneziaBaseURL)
	if err := client.Login(ctx, key); err != nil {
		return nil, err
	}
	return client.AccountInfo(ctx)
}

func (r *Router) sendAmneziaAccount(ctx context.Context, chatID int64, threadID *int64, replyTo *int64, user *db.User, key amneziaStoredKey, info *amnezia.AccountInfo) {
	text := amnezia.FormatAccountSummary(user.Nickname+" / "+key.Label, info)
	kb := amneziaKeyboard(user.ID, key.ID, info)
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, chatID, threadID, text, "", replyTo, &kb)
}

func amneziaKeyListView(user *db.User, keys amneziaRouterKeys) (string, tg.InlineKeyboardMarkup) {
	text := "Amnezia Premium - " + user.Nickname + "\n\n"
	if len(keys.Keys) == 0 {
		text += "Ключей пока нет.\n\nПришли сюда vpn:// ключ Amnezia Premium: я проверю его, сохраню за этим топиком и удалю сообщение с ключом."
		return text, tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	}
	text += fmt.Sprintf("Сохранено ключей: %d\nОткрой ключ, затем нажми \"Выгрузить .conf\" и выбери страну. Пришли ещё один vpn:// ключ, чтобы добавить его в этот топик.", len(keys.Keys))
	rows := make([][]tg.InlineKeyboardButton, 0, len(keys.Keys)+1)
	for _, key := range keys.Keys {
		label := key.Label
		if key.ID == keys.ActiveID {
			label += " *"
		}
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text:         "Открыть " + label,
			CallbackData: fmt.Sprintf("amz_open:%d:_panel_:%s", user.ID, key.ID),
		}, {
			Text:         "Удалить",
			CallbackData: fmt.Sprintf("amz_delete:%d:_panel_:%s", user.ID, key.ID),
		}})
	}
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text:         "Обновить список",
		CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_", user.ID),
	}})
	return text, tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func amneziaKeyboard(userID int64, keyID string, info *amnezia.AccountInfo) tg.InlineKeyboardMarkup {
	rows := [][]tg.InlineKeyboardButton{{
		{Text: "Обновить", CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_:%s", userID, keyID)},
		{Text: "К списку", CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_", userID)},
	}}
	if info != nil && len(info.AvailableCountries) > 0 {
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text:         "Выгрузить .conf",
			CallbackData: fmt.Sprintf("amz_countries:%d:_panel_:%s:0", userID, keyID),
		}})
	}
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text:         "Удалить ключ",
		CallbackData: fmt.Sprintf("amz_delete:%d:_panel_:%s", userID, keyID),
	}})
	return tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func amneziaCountriesView(user *db.User, stored amneziaStoredKey, info *amnezia.AccountInfo, page int) (string, tg.InlineKeyboardMarkup) {
	rows := [][]tg.InlineKeyboardButton{}
	if info == nil || len(info.AvailableCountries) == 0 {
		return "Amnezia Premium: нет доступных стран для .conf", tg.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	countries := append([]amnezia.Country(nil), info.AvailableCountries...)
	sort.Slice(countries, func(i, j int) bool {
		return strings.ToLower(countries[i].Code) < strings.ToLower(countries[j].Code)
	})
	if page < 0 {
		page = 0
	}
	pages := (len(countries) + amneziaCountryPageSize - 1) / amneziaCountryPageSize
	if pages == 0 {
		pages = 1
	}
	if page >= pages {
		page = pages - 1
	}
	issued := amneziaIssuedCountrySet(info)
	start := page * amneziaCountryPageSize
	end := start + amneziaCountryPageSize
	if end > len(countries) {
		end = len(countries)
	}
	for _, country := range countries[start:end] {
		label := amneziaCountryLabel(country)
		if issued[strings.ToLower(country.Code)] {
			label += " (есть)"
		}
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text:         label,
			CallbackData: fmt.Sprintf("amz_dl:%d:_panel_:%s:%s", user.ID, stored.ID, strings.ToLower(country.Code)),
		}})
	}
	nav := []tg.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, tg.InlineKeyboardButton{Text: "Назад", CallbackData: fmt.Sprintf("amz_countries:%d:_panel_:%s:%d", user.ID, stored.ID, page-1)})
	}
	if page+1 < pages {
		nav = append(nav, tg.InlineKeyboardButton{Text: "Дальше", CallbackData: fmt.Sprintf("amz_countries:%d:_panel_:%s:%d", user.ID, stored.ID, page+1)})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text:         "К кабинету",
		CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_:%s", user.ID, stored.ID),
	}})
	text := fmt.Sprintf("Amnezia Premium - %s / %s\n\nВыбери страну для .conf. Пометка \"есть\" значит country config уже выпускался и будет выгружен повторно.\nСтраница: %d/%d", user.Nickname, stored.Label, page+1, pages)
	return text, tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func amneziaCountryButton(userID int64, c amnezia.Country) tg.InlineKeyboardButton {
	return amneziaCountryButtonForKey(userID, "active", c)
}

func amneziaCountryButtonForKey(userID int64, keyID string, c amnezia.Country) tg.InlineKeyboardButton {
	label := amneziaCountryLabel(c)
	if keyID == "" {
		keyID = "active"
	}
	return tg.InlineKeyboardButton{
		Text:         "Выпустить " + label,
		CallbackData: fmt.Sprintf("amz_dl:%d:_panel_:%s:%s", userID, keyID, strings.ToLower(c.Code)),
	}
}

func amneziaCountryLabel(c amnezia.Country) string {
	label := strings.ToUpper(c.Code)
	if c.Name != "" {
		label = c.Name
	}
	return label
}

func amneziaIssuedCountrySet(info *amnezia.AccountInfo) map[string]bool {
	issued := map[string]bool{}
	if info == nil {
		return issued
	}
	for _, item := range info.IssuedConfigs {
		if item.SourceType == "country_config" {
			issued[strings.ToLower(item.CountryCode)] = true
		}
	}
	return issued
}

func (r *Router) handleAmneziaRefresh(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	if args.AmneziaKeyID == "" {
		keys, err := r.listAmneziaKeys(user.ID)
		if err != nil {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ошибка ключей")
			return
		}
		text, kb := amneziaKeyListView(user, keys)
		_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обновлено")
		return
	}
	key, _ := r.getAmneziaKeyByID(user.ID, args.AmneziaKeyID)
	if key == "" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ключ не сохранён")
		return
	}
	info, err := r.fetchAmneziaAccount(ctx, key)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ошибка Amnezia")
		return
	}
	stored, _ := r.amneziaStoredKey(user.ID, args.AmneziaKeyID)
	kb := amneziaKeyboard(user.ID, args.AmneziaKeyID, info)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, amnezia.FormatAccountSummary(user.Nickname+" / "+stored.Label, info), "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обновлено")
}

func (r *Router) handleAmneziaOpen(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	key, _ := r.getAmneziaKeyByID(user.ID, args.AmneziaKeyID)
	if key == "" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ключ не сохранён")
		return
	}
	info, err := r.fetchAmneziaAccount(ctx, key)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ошибка Amnezia")
		return
	}
	stored, _ := r.amneziaStoredKey(user.ID, args.AmneziaKeyID)
	kb := amneziaKeyboard(user.ID, args.AmneziaKeyID, info)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, amnezia.FormatAccountSummary(user.Nickname+" / "+stored.Label, info), "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleAmneziaCountries(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	key, _ := r.getAmneziaKeyByID(user.ID, args.AmneziaKeyID)
	if key == "" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ключ не сохранён")
		return
	}
	info, err := r.fetchAmneziaAccount(ctx, key)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ошибка Amnezia")
		return
	}
	stored, _ := r.amneziaStoredKey(user.ID, args.AmneziaKeyID)
	text, kb := amneziaCountriesView(user, stored, info, args.AmneziaPage)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleAmneziaDeleteAsk(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	stored, ok := r.amneziaStoredKey(user.ID, args.AmneziaKeyID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ключ не найден")
		return
	}
	text := fmt.Sprintf("Удалить %s из Amnezia Premium этого топика?\n\nСам Premium key в Amnezia не перевыпускается, я удалю только сохранение в боте.", stored.Label)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "Да, удалить", CallbackData: fmt.Sprintf("amz_delete_confirm:%d:_panel_:%s", user.ID, args.AmneziaKeyID)},
	}, {
		{Text: "Назад", CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_", user.ID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleAmneziaDeleteConfirm(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	if err := r.deleteAmneziaKey(user.ID, args.AmneziaKeyID); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	keys, err := r.listAmneziaKeys(user.ID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ошибка ключей")
		return
	}
	text, kb := amneziaKeyListView(user, keys)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ключ удалён")
}

func (r *Router) handleAmneziaDownloadAsk(ctx context.Context, q *tg.CallbackQuery, args Args) {
	if args.AmneziaKeyID == "" {
		stored, ok := r.amneziaStoredKey(args.UserID, "")
		if !ok {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ключ не сохранён")
			return
		}
		args.AmneziaKeyID = stored.ID
	}
	key, _ := r.getAmneziaKeyByID(args.UserID, args.AmneziaKeyID)
	info, err := r.fetchAmneziaAccount(ctx, key)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ошибка Amnezia")
		return
	}
	issued := amneziaIssuedCountrySet(info)[strings.ToLower(args.AmneziaCountryCode)]
	if !issued && info.ActiveDeviceCount >= info.MaxDeviceCount {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "свободных слотов нет")
		return
	}
	text := fmt.Sprintf("Выгрузить Amnezia .conf для %s?\n\nФайл придёт в этот топик, а импорт туннеля встанет в очередь роутера.", strings.ToUpper(args.AmneziaCountryCode))
	if !issued {
		text += "\n\nЭто новый country config, он может занять слот подписки."
	}
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "Да, выпустить", CallbackData: fmt.Sprintf("amz_dl_confirm:%d:_panel_:%s:%s", args.UserID, args.AmneziaKeyID, args.AmneziaCountryCode)},
	}, {
		{Text: "Назад", CallbackData: fmt.Sprintf("amz_countries:%d:_panel_:%s:0", args.UserID, args.AmneziaKeyID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleAmneziaDownloadConfirm(ctx context.Context, q *tg.CallbackQuery, args Args) {
	if r.cmdSink == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "command queue не подключена")
		return
	}
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	key, _ := r.getAmneziaKeyByID(user.ID, args.AmneziaKeyID)
	if key == "" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ключ не сохранён")
		return
	}
	if args.AmneziaKeyID == "" {
		if stored, ok := r.amneziaStoredKey(user.ID, ""); ok {
			args.AmneziaKeyID = stored.ID
		}
	}
	conf, err := r.downloadAmneziaConfig(ctx, key, args.AmneziaCountryCode)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	docWarn := ""
	if err := r.sendConfigDocument(ctx, q.Message.Chat.ID, q.Message.MessageThreadID, "amnezia_"+safeConfigSlug(args.AmneziaCountryCode)+".conf", conf, "Amnezia Premium "+strings.ToUpper(args.AmneziaCountryCode)); err != nil {
		docWarn = "\n\nФайл в чат не отправился: " + shortToast(err)
	}
	cmd := wire.Command{
		ID:     defaultCmdID(),
		Action: "tunnel_import",
		Args: map[string]any{
			"conf":    base64.StdEncoding.EncodeToString(conf),
			"name":    "amnezia_" + args.AmneziaCountryCode,
			"replace": false,
		},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID, Action: "tunnel_import"}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "конфиг выгружен, импорт в очереди")
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "Amnezia config выгружен в топик. Импорт туннеля поставлен в очередь роутера."+docWarn, "", &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "К странам", CallbackData: fmt.Sprintf("amz_countries:%d:_panel_:%s:0", user.ID, args.AmneziaKeyID)},
	}}})
}

func (r *Router) downloadAmneziaConfig(ctx context.Context, key, country string) ([]byte, error) {
	client := amnezia.New(r.cfg.AmneziaBaseURL)
	if err := client.Login(ctx, key); err != nil {
		return nil, err
	}
	conf, err := client.DownloadConfig(ctx, country)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(conf), "[Interface]") {
		return nil, errors.New("downloaded config does not look like WireGuard conf")
	}
	return conf, nil
}

func shortToast(err error) string {
	msg := err.Error()
	if len(msg) > 180 {
		return msg[:180]
	}
	return msg
}
