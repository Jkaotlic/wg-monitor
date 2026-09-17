package callbacks

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/hidemy"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Ключи и коды кабинетов бот больше не принимает (цикл 3, решение 10): они
// вводятся полем приложения по HTTPS. Но привычка остаётся, и присланный в
// чат секрет не должен висеть в переписке. Бот его удаляет и отвечает, куда
// идти. Текст сообщения в журнал не пишется -- только вид секрета.

const (
	cabinetMovedText     = "Ключи, коды и пароли теперь вводятся в приложении: откройте роутер → «Кабинет». Сообщение удалено, чтобы секрет не висел в переписке."
	cabinetMovedKeptText = "Ключи, коды и пароли теперь вводятся в приложении: откройте роутер → «Кабинет». Удалите это сообщение сами — у бота не получилось."
	cabinetOpenAppButton = "📱 Открыть приложение"
)

// cabinetSecretKind -- похоже ли сообщение на секрет кабинета: ключ vpn://,
// цифровой код HideMy (как его принимал бот) или пароль SSH своего сервера
// в формате старого диалога key=value.
func cabinetSecretKind(text string) string {
	t := strings.TrimSpace(text)
	low := strings.ToLower(t)
	switch {
	case strings.Contains(low, "vpn://"):
		return "amnezia_key"
	case hidemy.ValidAccessCode(t):
		return "hidemy_code"
	case strings.Contains(low, "ssh_password="):
		return "ssh_password"
	}
	return ""
}

// handleCabinetSecretMessage -- true, если сообщение было секретом и
// обработано. Ключ vpn:// и ssh_password= ловятся в личке с ботом (любого
// человека) и в разрешённых группах; в чужих группах бот молчит. Цифровой
// код -- только в личке: в группе 10-20 цифр -- это и телефон, и Telegram ID,
// и удалять такое сообщение нельзя (решение контроллера цикла 3).
func (r *Router) handleCabinetSecretMessage(ctx context.Context, m *tg.Message) bool {
	kind := cabinetSecretKind(m.Text)
	if kind == "" {
		// Подпись к файлу или фото висит в чате так же, как текст.
		kind = cabinetSecretKind(m.Caption)
	}
	if kind == "" {
		return false
	}
	private := m.Chat.ID == m.From.ID
	if kind == "hidemy_code" && !private {
		return false
	}
	if !private && !r.chatAllowed(m.Chat.ID) {
		return false
	}
	deleted := false
	if m.MessageID != 0 {
		if err := r.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID); err != nil {
			slog.Warn("секрет кабинета в чате: удалить не удалось", "chat", m.Chat.ID, "kind", kind, "err", err)
		} else {
			deleted = true
		}
	}
	slog.Info("секрет кабинета в чате", "chat", m.Chat.ID, "from", m.From.ID, "kind", kind, "deleted", deleted)
	text := cabinetMovedText
	if !deleted {
		text = cabinetMovedKeptText
	}
	// Кнопку web_app Telegram разрешает только в личке.
	if url := tg.MiniAppURL(r.cfg.PublicBaseURL); private && url != "" {
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
			{Text: cabinetOpenAppButton, WebApp: &tg.WebAppInfo{URL: url}},
		}}}
		if _, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil, &kb); err != nil {
			slog.Warn("секрет кабинета в чате: ответ не отправлен", "chat", m.Chat.ID, "err", err)
		}
		return true
	}
	if _, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil); err != nil {
		slog.Warn("секрет кабинета в чате: ответ не отправлен", "chat", m.Chat.ID, "err", err)
	}
	return true
}
