package callbacks

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Панели VPN-туннелей и маршрутов, перезапуск служб из них и приём .conf
// документом ушли из бота в приложение (цикл 4). Сообщения со старыми
// кнопками висят в чатах месяцами, и нажатие на них обязано отвечать
// словами, а не «неизвестная кнопка»: человек иначе решит, что сломался бот.

const movedToAppToast = "Это теперь в приложении"

// movedToAppActions -- действия удалённых кнопок (callback_data до первого «:»).
var movedToAppActions = map[string]bool{
	"restart_tunnel": true, "tunnel_enable": true, "tunnel_disable": true, "tunnel_restart": true,
	"tunnel_delete_ask": true, "tunnel_delete": true, "tunnels_refresh": true,
	"tunnel_import_replace": true, "tunnel_import_add": true,
	"routes_open": true, "routes_refresh": true, "routes_rebind": true, "routes_pick": true,
	"routes_confirm": true, "routes_rollback": true, "routes_back": true, "routes_close": true,
	"routes_add": true, "routes_add_type": true, "routes_add_tunnel": true,
	"routes_tpl_load": true, "routes_tpl_pick": true, "routes_tpl_page": true,
	"routes_add_confirm": true, "routes_add_cancel": true,
	"routes_del": true, "routes_del_confirm": true, "routes_del_cancel": true,
	"routes_hrneo": true, "routes_hrneo_doctor": true, "routes_snapshot": true,
	"maint_restart": true, "maint_confirm": true,
}

// movedToAppData -- удалённые кнопки, чьё действие живо для других экранов:
// справка и кнопки нижнего меню «🎛 Туннели» / «🛣 Маршруты» в режиме
// инлайн-клавиатуры.
var movedToAppData = map[string]bool{
	"panel:0:help:tunnels": true,
	"panel:0:help:routes":  true,
	"compat_btn:0:tunnels": true,
	"compat_btn:0:routes":  true,
}

func isMovedToAppCallback(data string) bool {
	action, _, _ := strings.Cut(data, ":")
	return movedToAppActions[action] || movedToAppData[data]
}

const (
	confMovedText     = "VPN-туннели из файла теперь загружаются в приложении: откройте роутер → «VPN-туннели» → «Загрузить конфиг .conf». Файл удалён, чтобы приватный ключ не висел в переписке."
	confMovedKeptText = "VPN-туннели из файла теперь загружаются в приложении: откройте роутер → «VPN-туннели» → «Загрузить конфиг .conf». Удалите это сообщение с файлом сами — у бота не получилось."
)

// handleConfDocument -- true, если сообщение было файлом .conf и обработано.
// Бот больше не импортирует конфиги, но в .conf лежит приватный ключ, и файл
// не должен висеть в переписке: бот удаляет его и говорит, куда идти. Как
// сторож секретов кабинетов: в личке с любым человеком и в разрешённых
// группах; в чужих группах молчит. Прочие документы бот не трогает.
func (r *Router) handleConfDocument(ctx context.Context, m *tg.Message) bool {
	if m.Document == nil || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(m.Document.FileName)), ".conf") {
		return false
	}
	private := m.Chat.ID == m.From.ID
	if !private && !r.chatAllowed(m.Chat.ID) {
		return false
	}
	deleted := false
	if m.MessageID != 0 {
		if err := r.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
			slog.Warn(".conf в чате: удалить не удалось", "chat", m.Chat.ID, "err", err)
		} else {
			deleted = true
		}
	}
	slog.Info(".conf в чате", "chat", m.Chat.ID, "from", m.From.ID, "size", m.Document.FileSize, "deleted", deleted)
	text := confMovedText
	if !deleted {
		text = confMovedKeptText
	}
	// Кнопку web_app Telegram разрешает только в личке.
	if url := tg.MiniAppURL(r.cfg.PublicBaseURL); private && url != "" {
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{tg.OpenInAppButton(url)}}}
		if _, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, &kb); err != nil {
			slog.Warn(".conf в чате: ответ не отправлен", "chat", m.Chat.ID, "err", err)
		}
		return true
	}
	if _, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil); err != nil {
		slog.Warn(".conf в чате: ответ не отправлен", "chat", m.Chat.ID, "err", err)
	}
	return true
}
