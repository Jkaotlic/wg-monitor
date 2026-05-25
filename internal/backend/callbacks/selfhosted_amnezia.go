package callbacks

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func (r *Router) addSelfHostedAmneziaRow(kb *tg.InlineKeyboardMarkup, userID int64) {
	if kb == nil {
		return
	}
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		slog.Warn("self-hosted Amnezia store read failed", "err", err)
		return
	}
	for _, inst := range store.EnabledInstances() {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []tg.InlineKeyboardButton{{
			Text:         "Self-hosted " + inst.Label + " .conf",
			CallbackData: fmt.Sprintf("amz_selfhosted_issue:%d:_panel_:%s", userID, inst.ID),
		}})
	}
}

func (r *Router) handleSelfHostedAmneziaIssue(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "router not found")
		return
	}
	inst, _, err := r.selfHostedAmneziaInstance(args.SelfHostedAmneziaID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "self-hosted Amnezia не настроен")
		return
	}
	text := fmt.Sprintf("Выпустить self-hosted Amnezia .conf для %s через %s?\n\nЯ создам нового клиента на VPS, отправлю .conf в этот топик и поставлю импорт туннеля в очередь роутера.", user.Nickname, inst.Label)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "Да, выпустить", CallbackData: fmt.Sprintf("amz_selfhosted_confirm:%d:_panel_:%s", user.ID, inst.ID)},
	}, {
		{Text: "Назад", CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_", user.ID)},
	}}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleSelfHostedAmneziaConfirm(ctx context.Context, q *tg.CallbackQuery, args Args) {
	if r.cmdSink == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "command queue не подключена")
		return
	}
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "router not found")
		return
	}
	inst, cfg, err := r.selfHostedAmneziaInstance(args.SelfHostedAmneziaID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "self-hosted Amnezia не настроен")
		return
	}
	clientName := "wgmon-" + user.Nickname + "-" + time.Now().Format("20060102-150405")
	issued, err := selfhostedamnezia.Issue(ctx, cfg, clientName)
	if err != nil {
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
		},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID, Action: "tunnel_import"}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, shortToast(err))
		return
	}
	msg := fmt.Sprintf("Self-hosted Amnezia config выпущен через %s для %s (%s). Импорт туннеля поставлен в очередь роутера.", inst.Label, issued.Name, issued.Address)
	if strings.TrimSpace(docWarn) != "" {
		msg += docWarn
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "конфиг выпущен, импорт в очереди")
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, msg, "", &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "К Amnezia", CallbackData: fmt.Sprintf("amz_refresh:%d:_panel_", user.ID)},
	}}})
}

func (r *Router) loadSelfHostedAmneziaStore() (selfhostedamnezia.Store, error) {
	path := r.cfg.SelfHostedAmnezia.StorePathOrDefault()
	return selfhostedamnezia.LoadStore(path, r.cfg.SelfHostedAmnezia)
}

func (r *Router) saveSelfHostedAmneziaStore(store selfhostedamnezia.Store) error {
	return selfhostedamnezia.SaveStore(r.cfg.SelfHostedAmnezia.StorePathOrDefault(), store)
}

func (r *Router) selfHostedAmneziaInstance(id string) (selfhostedamnezia.Instance, selfhostedamnezia.Config, error) {
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		return selfhostedamnezia.Instance{}, selfhostedamnezia.Config{}, err
	}
	inst, ok := store.Get(id)
	if !ok || !inst.Enabled {
		return selfhostedamnezia.Instance{}, selfhostedamnezia.Config{}, fmt.Errorf("self-hosted Amnezia instance not found")
	}
	cfg := r.cfg.SelfHostedAmnezia.ProviderConfig(inst)
	if !cfg.Ready() {
		return selfhostedamnezia.Instance{}, selfhostedamnezia.Config{}, fmt.Errorf("self-hosted Amnezia instance is not ready")
	}
	return inst, cfg, nil
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
