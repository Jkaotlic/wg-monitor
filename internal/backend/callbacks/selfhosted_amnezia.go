package callbacks

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

type pendingSelfHostedAmnezia struct {
	AdminUserID     int64
	ChatID          int64
	ThreadID        *int64
	PromptMessageID int64
	Mode            string
	InstanceID      string
	ExpiresAt       time.Time
}

type pendingSelfHostedAmneziaStore struct {
	mu sync.Mutex
	m  map[int64]*pendingSelfHostedAmnezia
}

func newPendingSelfHostedAmneziaStore() *pendingSelfHostedAmneziaStore {
	return &pendingSelfHostedAmneziaStore{m: make(map[int64]*pendingSelfHostedAmnezia)}
}

func (s *pendingSelfHostedAmneziaStore) put(p *pendingSelfHostedAmnezia) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.AdminUserID] = p
}

func (s *pendingSelfHostedAmneziaStore) get(adminID int64) (*pendingSelfHostedAmnezia, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[adminID]
	if !ok {
		return nil, false
	}
	if time.Now().After(p.ExpiresAt) {
		delete(s.m, adminID)
		return nil, false
	}
	return p, true
}

func (s *pendingSelfHostedAmneziaStore) clear(adminID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, adminID)
}

func (r *Router) addSelfHostedAmneziaRow(kb *tg.InlineKeyboardMarkup, userID int64, admin bool) {
	if kb == nil {
		return
	}
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		slog.Warn("self-hosted Amnezia store read failed", "err", err)
		if admin {
			kb.InlineKeyboard = append(kb.InlineKeyboard, []tg.InlineKeyboardButton{{
				Text:         "⚙ Self-hosted Amnezia",
				CallbackData: fmt.Sprintf("amz_selfhosted_manage:%d:_panel_", userID),
			}})
		}
		return
	}
	for _, inst := range store.EnabledInstances() {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []tg.InlineKeyboardButton{{
			Text:         "Self-hosted " + inst.Label + " .conf",
			CallbackData: fmt.Sprintf("amz_selfhosted_issue:%d:_panel_:%s", userID, inst.ID),
		}})
	}
	if admin {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []tg.InlineKeyboardButton{{
			Text:         "⚙ Self-hosted Amnezia",
			CallbackData: fmt.Sprintf("amz_selfhosted_manage:%d:_panel_", userID),
		}})
	}
}

func (r *Router) handleSelfHostedAmneziaManage(ctx context.Context, q *tg.CallbackQuery, args Args) {
	text, kb := r.selfHostedAmneziaManageView(args.UserID)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleSelfHostedAmneziaStartAdd(ctx context.Context, q *tg.CallbackQuery, args Args) {
	r.pendingSelfHostedAmnezia.put(&pendingSelfHostedAmnezia{
		AdminUserID:     q.From.ID,
		ChatID:          q.Message.Chat.ID,
		ThreadID:        cloneInt64Ptr(q.Message.MessageThreadID),
		PromptMessageID: q.Message.MessageID,
		Mode:            "add",
		ExpiresAt:       time.Now().Add(5 * time.Minute),
	})
	text := "➕ Добавить self-hosted Amnezia VPS\n\nОтветь одним сообщением key=value:\nid=home ssh_host=1.2.3.4 ssh_user=root ssh_password=... endpoint_host=vpn.example.com endpoint_port=47567 label=Home-VPS\n\nЕсли backend уже на этом VPS, можно старым коротким форматом:\nhome vpn.example.com 47567 Home VPS\n\n/cancel — отмена. Жду 5 минут."
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "Отмена", CallbackData: fmt.Sprintf("amz_selfhosted_cancel:%d:_panel_", args.UserID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "жду параметры")
}

func (r *Router) handleSelfHostedAmneziaStartEdit(ctx context.Context, q *tg.CallbackQuery, args Args) {
	inst, _, ok := r.selfHostedAmneziaStoredInstance(args.SelfHostedAmneziaID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "self-hosted VPS не найден")
		return
	}
	r.pendingSelfHostedAmnezia.put(&pendingSelfHostedAmnezia{
		AdminUserID:     q.From.ID,
		ChatID:          q.Message.Chat.ID,
		ThreadID:        cloneInt64Ptr(q.Message.MessageThreadID),
		PromptMessageID: q.Message.MessageID,
		Mode:            "edit",
		InstanceID:      inst.ID,
		ExpiresAt:       time.Now().Add(5 * time.Minute),
	})
	text := fmt.Sprintf("✏️ Изменить self-hosted VPS %s\n\nОтветь одним сообщением key=value:\nhost=vpn.example.com port=47567 label=Home-VPS dns=1.1.1.1,8.8.8.8 ssh_host=1.2.3.4 ssh_port=22 ssh_user=root ssh_password=...\n\nМожно менять отдельные поля: host, port, label, dns, ssh_host, ssh_port, ssh_user, ssh_password, container, interface, config_path, clients_path, server_public_key_path, preshared_key_path.\n\n/cancel — отмена. Жду 5 минут.", inst.ID)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "Отмена", CallbackData: fmt.Sprintf("amz_selfhosted_cancel:%d:_panel_", args.UserID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "жду изменения")
}

func (r *Router) handleSelfHostedAmneziaToggle(ctx context.Context, q *tg.CallbackQuery, args Args) {
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	if !store.SetEnabled(args.SelfHostedAmneziaID, args.SelfHostedAmneziaEnabled) {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "self-hosted VPS не найден")
		return
	}
	if err := r.saveSelfHostedAmneziaStore(store); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	r.handleSelfHostedAmneziaManage(ctx, q, args)
}

func (r *Router) handleSelfHostedAmneziaDelete(ctx context.Context, q *tg.CallbackQuery, args Args) {
	inst, _, ok := r.selfHostedAmneziaStoredInstance(args.SelfHostedAmneziaID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "self-hosted VPS не найден")
		return
	}
	label := inst.Label
	if strings.TrimSpace(label) == "" {
		label = inst.ID
	}
	text := fmt.Sprintf("Удалить self-hosted Amnezia VPS %s?\n\nЭто уберёт провайдера из кабинета бота. Уже выпущенные конфиги, клиенты и сам VPS не изменятся.", label)
	tok := r.putPendingConfirm(q, args.UserID, "amz_selfhosted_delete_confirm", inst.ID)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "✅ Да, удалить", CallbackData: fmt.Sprintf("amz_selfhosted_delete_confirm:%d:_panel_:%s:%s", args.UserID, inst.ID, tok)},
	}, {
		{Text: "↩ Назад", CallbackData: fmt.Sprintf("amz_selfhosted_manage:%d:_panel_", args.UserID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleSelfHostedAmneziaDeleteConfirm(ctx context.Context, q *tg.CallbackQuery, args Args) {
	confirm, ok := r.takePendingConfirm(ctx, q, args, args.SelfHostedAmneziaID)
	if !ok {
		return
	}
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		r.restorePendingConfirm(confirm)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	if !store.Delete(args.SelfHostedAmneziaID) {
		r.restorePendingConfirm(confirm)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "self-hosted VPS не найден")
		return
	}
	if err := r.saveSelfHostedAmneziaStore(store); err != nil {
		r.restorePendingConfirm(confirm)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	r.handleSelfHostedAmneziaManage(ctx, q, args)
}

func (r *Router) handleSelfHostedAmneziaCancel(ctx context.Context, q *tg.CallbackQuery, args Args) {
	r.pendingSelfHostedAmnezia.clear(q.From.ID)
	r.handleSelfHostedAmneziaManage(ctx, q, args)
}

func (r *Router) selfHostedAmneziaManageView(userID int64) (string, tg.InlineKeyboardMarkup) {
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		text := "⚙ Self-hosted Amnezia\n\nНе удалось прочитать storage: " + err.Error()
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "➕ Добавить VPS", CallbackData: fmt.Sprintf("amz_selfhosted_add:%d:_panel_", userID)}},
			{{Text: "Назад", CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_", userID)}},
		}}
		return text, kb
	}
	var b strings.Builder
	b.WriteString("⚙ Self-hosted Amnezia\n\n")
	rows := make([][]tg.InlineKeyboardButton, 0, len(store.Instances)*2+2)
	if len(store.Instances) == 0 {
		b.WriteString("VPS ещё не добавлены. Нажми «Добавить VPS» и пришли host/port.")
	} else {
		b.WriteString("VPS:\n")
	}
	for _, inst := range store.Instances {
		state := "off"
		nextState := "1"
		toggleText := "Включить"
		if inst.Enabled {
			state = "on"
			nextState = "0"
			toggleText = "Выключить"
		}
		fmt.Fprintf(&b, "  • %s [%s] %s:%d", inst.ID, state, inst.EndpointHost, inst.EndpointPort)
		if strings.TrimSpace(inst.Label) != "" && inst.Label != inst.ID {
			fmt.Fprintf(&b, " — %s", inst.Label)
		}
		b.WriteByte('\n')
		row := []tg.InlineKeyboardButton{{
			Text:         "✏️ " + inst.ID,
			CallbackData: fmt.Sprintf("amz_selfhosted_edit:%d:_panel_:%s", userID, inst.ID),
		}, {
			Text:         toggleText,
			CallbackData: fmt.Sprintf("amz_selfhosted_toggle:%d:_panel_:%s:%s", userID, inst.ID, nextState),
		}}
		if inst.Enabled {
			row = append(row, tg.InlineKeyboardButton{
				Text:         ".conf",
				CallbackData: fmt.Sprintf("amz_selfhosted_issue:%d:_panel_:%s", userID, inst.ID),
			})
		}
		row = append(row, tg.InlineKeyboardButton{
			Text:         "Удалить",
			CallbackData: fmt.Sprintf("amz_selfhosted_delete:%d:_panel_:%s", userID, inst.ID),
		})
		rows = append(rows, row)
	}
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text:         "➕ Добавить VPS",
		CallbackData: fmt.Sprintf("amz_selfhosted_add:%d:_panel_", userID),
	}})
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text:         "Назад",
		CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_", userID),
	}})
	return b.String(), tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (r *Router) handlePendingSelfHostedAmneziaMessage(ctx context.Context, m *tg.Message) bool {
	if r.pendingSelfHostedAmnezia == nil {
		return false
	}
	p, ok := r.pendingSelfHostedAmnezia.get(m.From.ID)
	if !ok || p.ChatID != m.Chat.ID || !sameThread(p.ThreadID, m.MessageThreadID) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(m.Text), "/cancel") || strings.EqualFold(strings.TrimSpace(m.Text), "cancel") {
		r.pendingSelfHostedAmnezia.clear(m.From.ID)
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "Self-hosted Amnezia: отменено.", "", nil)
		return true
	}
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "Не удалось прочитать storage: "+err.Error(), "", nil)
		return true
	}
	switch p.Mode {
	case "add":
		inst, err := parseSelfHostedAddMessage(m.Text)
		if err != nil {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, err.Error(), "", nil)
			return true
		}
		if err := store.Upsert(inst); err != nil {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "Не удалось сохранить VPS: "+err.Error(), "", nil)
			return true
		}
	case "edit":
		inst, ok := store.Get(p.InstanceID)
		if !ok {
			r.pendingSelfHostedAmnezia.clear(m.From.ID)
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "Self-hosted VPS уже не найден, отменяю.", "", nil)
			return true
		}
		if err := applySelfHostedKVLine(&inst, m.Text); err != nil {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, err.Error(), "", nil)
			return true
		}
		if err := store.Upsert(inst); err != nil {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "Не удалось обновить VPS: "+err.Error(), "", nil)
			return true
		}
	default:
		return false
	}
	if err := r.saveSelfHostedAmneziaStore(store); err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "Не удалось записать storage: "+err.Error(), "", nil)
		return true
	}
	r.pendingSelfHostedAmnezia.clear(m.From.ID)
	text, kb := r.selfHostedAmneziaManageView(0)
	if _, user := r.resolveTopicKind(m.Chat.ID, m.MessageThreadID); user != nil {
		text, kb = r.selfHostedAmneziaManageView(user.ID)
	}
	if p.PromptMessageID != 0 {
		_ = r.tg.EditMessageText(ctx, p.ChatID, p.PromptMessageID, text, "", &kb)
	}
	_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, "Self-hosted Amnezia: сохранено.", "", nil)
	return true
}

func (r *Router) handleSelfHostedAmneziaIssue(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	inst, _, err := r.selfHostedAmneziaInstance(args.SelfHostedAmneziaID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "self-hosted Amnezia не настроен")
		return
	}
	text := fmt.Sprintf("Выпустить self-hosted Amnezia .conf для %s через %s?\n\nЯ создам нового клиента на VPS, отправлю .conf в этот топик и поставлю импорт туннеля в очередь роутера.", user.Nickname, inst.Label)
	tok := r.putPendingConfirm(q, user.ID, "amz_selfhosted_confirm", inst.ID)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "Да, выпустить", CallbackData: fmt.Sprintf("amz_selfhosted_confirm:%d:_panel_:%s:%s", user.ID, inst.ID, tok)},
	}, {
		{Text: "Назад", CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_", user.ID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func selfHostedAmneziaImportQueuedView(userID int64, label, issuedName, address, docWarn string) (string, tg.InlineKeyboardMarkup) {
	text := fmt.Sprintf("Self-hosted Amnezia config выпущен через %s для %s (%s). Импорт туннеля поставлен в очередь роутера.", label, issuedName, address)
	if strings.TrimSpace(docWarn) != "" {
		text += docWarn
	}
	text += "\n\nСейчас агент импортирует конфиг, запускает туннель и ждёт status/handshake. Проверка может занять до 10 секунд; финальное сообщение заменит эту карточку."
	text += "\n\nДальше:\n1. Нажми «Проверить туннели» и дождись живого списка.\n2. Если правила были на старом туннеле, открой «Маршруты / перенос»."
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🎛 Проверить туннели", CallbackData: fmt.Sprintf("tunnels_refresh:%d:_panel_", userID)},
	}, {
		{Text: "🛣 Маршруты / перенос", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
	}, {
		{Text: "🔐 К Amnezia", CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_", userID)},
	}}}
	return text, kb
}

func (r *Router) handleSelfHostedAmneziaConfirm(ctx context.Context, q *tg.CallbackQuery, args Args) {
	if r.cmdSink == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "command queue не подключена")
		return
	}
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	confirm, ok := r.takePendingConfirm(ctx, q, args, args.SelfHostedAmneziaID)
	if !ok {
		return
	}
	inst, cfg, err := r.selfHostedAmneziaInstance(args.SelfHostedAmneziaID)
	if err != nil {
		r.restorePendingConfirm(confirm)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "self-hosted Amnezia не настроен")
		return
	}
	clientName := "wgmon-" + user.Nickname + "-" + time.Now().Format("20060102-150405")
	issued, err := selfhostedamnezia.Issue(ctx, cfg, clientName)
	if err != nil {
		r.restorePendingConfirm(confirm)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	filename := "selfhosted_" + safeConfigSlug(inst.ID) + "_" + safeConfigSlug(user.Nickname) + ".conf"
	docWarn := ""
	if err := r.sendConfigDocument(ctx, q.Message.Chat.ID, q.Message.MessageThreadID, filename, issued.Config, "Self-hosted Amnezia "+inst.Label+" "+user.Nickname); err != nil {
		docWarn = "\n\nФайл в чат не отправился: " + shortToast(err)
	}
	tunnelName := safeSelfHostedTunnelName(inst.ID + "_" + user.Nickname)
	if tunnelName == "" {
		tunnelName = "selfhosted"
	}
	cmd := wire.Command{
		ID:     defaultCmdID(),
		Action: "tunnel_import",
		Args: map[string]any{
			"conf":    base64.StdEncoding.EncodeToString(issued.Config),
			"name":    tunnelName,
			"replace": true,
			"backend": "nativewg",
		},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID, Action: "tunnel_import"}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	msg, kb := selfHostedAmneziaImportQueuedView(user.ID, inst.Label, issued.Name, issued.Address, docWarn)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "конфиг выпущен, импорт в очереди")
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, msg, "", &kb)
}

func (r *Router) loadSelfHostedAmneziaStore() (selfhostedamnezia.Store, error) {
	path := r.cfg.SelfHostedAmnezia.StorePathOrDefault()
	return selfhostedamnezia.LoadStore(path, r.cfg.SelfHostedAmnezia)
}

func (r *Router) saveSelfHostedAmneziaStore(store selfhostedamnezia.Store) error {
	return selfhostedamnezia.SaveStore(r.cfg.SelfHostedAmnezia.StorePathOrDefault(), store)
}

func (r *Router) selfHostedAmneziaInstance(id string) (selfhostedamnezia.Instance, selfhostedamnezia.Config, error) {
	inst, _, ok := r.selfHostedAmneziaStoredInstance(id)
	if !ok || !inst.Enabled {
		return selfhostedamnezia.Instance{}, selfhostedamnezia.Config{}, fmt.Errorf("self-hosted Amnezia instance not found")
	}
	cfg := r.cfg.SelfHostedAmnezia.ProviderConfig(inst)
	if !cfg.Ready() {
		return selfhostedamnezia.Instance{}, selfhostedamnezia.Config{}, fmt.Errorf("self-hosted Amnezia instance is not ready")
	}
	return inst, cfg, nil
}

func (r *Router) selfHostedAmneziaStoredInstance(id string) (selfhostedamnezia.Instance, selfhostedamnezia.Store, bool) {
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		return selfhostedamnezia.Instance{}, selfhostedamnezia.Store{}, false
	}
	inst, ok := store.Get(id)
	return inst, store, ok
}

func parseSelfHostedAddMessage(text string) (selfhostedamnezia.Instance, error) {
	fields := selfHostedKVFields(text)
	if len(fields) > 0 && strings.Contains(fields[0], "=") {
		var inst selfhostedamnezia.Instance
		if err := applySelfHostedKVLine(&inst, text); err != nil {
			return selfhostedamnezia.Instance{}, err
		}
		if strings.TrimSpace(inst.ID) == "" {
			return selfhostedamnezia.Instance{}, fmt.Errorf("id is required")
		}
		return inst, nil
	}
	if len(fields) < 3 {
		return selfhostedamnezia.Instance{}, fmt.Errorf("Формат: id=home ssh_host=1.2.3.4 ssh_user=root ssh_password=... endpoint_host=vpn.example.com endpoint_port=47567 label=Home-VPS\nИли коротко: home vpn.example.com 47567 Home VPS")
	}
	port, err := strconv.Atoi(fields[2])
	if err != nil {
		return selfhostedamnezia.Instance{}, fmt.Errorf("endpoint_port должен быть числом")
	}
	return selfhostedamnezia.Instance{
		ID:           fields[0],
		EndpointHost: fields[1],
		EndpointPort: port,
		Label:        strings.Join(fields[3:], " "),
	}, nil
}

func applySelfHostedKVLine(inst *selfhostedamnezia.Instance, text string) error {
	fields := selfHostedKVFields(text)
	if len(fields) == 0 {
		return fmt.Errorf("Пришли key=value, например: host=vpn.example.com port=47567 label=Home")
	}
	for _, raw := range fields {
		key, val, ok := strings.Cut(raw, "=")
		if !ok {
			return fmt.Errorf("Ожидал key=value, получил: %s", raw)
		}
		if err := applySelfHostedField(inst, strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(val)); err != nil {
			return err
		}
	}
	return nil
}

func selfHostedKVFields(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if !strings.ContainsAny(text, "\r\n") {
		return strings.Fields(text)
	}
	var fields []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) != "" && !strings.ContainsAny(strings.TrimSpace(key), " \t") && !strings.Contains(val, "=") {
			fields = append(fields, line)
			continue
		}
		fields = append(fields, strings.Fields(line)...)
	}
	return fields
}

func cloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func safeSelfHostedTunnelName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" || out[0] < 'a' || out[0] > 'z' {
		out = "selfhosted-" + out
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
