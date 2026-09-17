package callbacks

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Кнопки бота, ушедшие в приложение: панели VPN-туннелей, маршрутов и служб
// (цикл 4), всё, кроме «Тише на час», из-под уведомлений и панели PingCheck,
// диагностики, справки и нижнего меню (цикл 5). Сообщения со старыми кнопками
// висят в чатах месяцами, и нажатие на них обязано отвечать словами, а не
// «неизвестная кнопка»: человек иначе решит, что сломался бот.

const movedToAppToast = "Это теперь в приложении"

// movedToAppActions -- действия удалённых кнопок (callback_data до первого «:»).
var movedToAppActions = map[string]bool{
	// цикл 4: VPN-туннели, маршруты, службы.
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
	// цикл 5: всё, кроме silence и nmute.
	"ack": true, "mute": true, "history": true,
	"diag_now": true, "diag_raw": true, "diag_test": true, "diag_back": true,
	"pingcheck_now": true, "pingcheck_open": true, "pingcheck_toggle": true,
	"force_recheck": true, "router_doctor": true,
	"check_via_tunnel": true, "check_direct": true,
	"close_panel": true, "compat_btn": true,
}

// movedToAppPrefix -- справка «ℹ Помощь» под панелями: panel:0:help:<экран>.
// Остальные panel:* -- кнопки хаба, удалённого в цикле 2, -- «неизвестная кнопка».
const movedToAppPrefix = "panel:0:help:"

func isMovedToAppCallback(data string) bool {
	action, _, _ := strings.Cut(data, ":")
	return movedToAppActions[action] || strings.HasPrefix(data, movedToAppPrefix)
}

const (
	confMovedText     = "VPN-туннели из файла теперь загружаются в приложении: откройте роутер → «VPN-туннели» → «Загрузить конфиг .conf». Файл удалён, чтобы приватный ключ не висел в переписке."
	confMovedKeptText = "VPN-туннели из файла теперь загружаются в приложении: откройте роутер → «VPN-туннели» → «Загрузить конфиг .conf». Удалите это сообщение с файлом сами — у бота не получилось."
)

// handleConfDocument -- true, если сообщение было файлом .conf и обработано.
// Бот конфиги не импортирует, но в .conf лежит приватный ключ, и файл не
// должен висеть в переписке: бот удаляет его и говорит, куда идти. Только в
// личке -- HandleMessage зовёт сторожа уже после проверки. Прочие документы
// бот не трогает.
func (r *Router) handleConfDocument(ctx context.Context, m *tg.Message) bool {
	if m.Document == nil || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(m.Document.FileName)), ".conf") {
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
	if url := tg.MiniAppURL(r.cfg.PublicBaseURL); url != "" {
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{tg.OpenInAppButton(url)}}}
		if _, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, nil, text, "", nil, &kb); err != nil {
			slog.Warn(".conf в чате: ответ не отправлен", "chat", m.Chat.ID, "err", err)
		}
		return true
	}
	if _, err := r.tg.SendMessage(ctx, m.Chat.ID, nil, text, "", nil); err != nil {
		slog.Warn(".conf в чате: ответ не отправлен", "chat", m.Chat.ID, "err", err)
	}
	return true
}
