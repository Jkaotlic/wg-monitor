package callbacks

import (
	"context"
	"log/slog"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

const helpCommonBody = `ℹ Помощь по боту

📛 Алерты:
✅ работает; 🟡 подозрение; 🔴 подтверждённый сбой; 📵 роутер offline.
Сначала жми 📊 Что происходит? или 🩺 Проверка. VPN-туннели, их перезапуск, импорт .conf и маршруты — в приложении.

🎛 Кнопки в топике роутера:
📊 Что происходит? — короткая сводка и следующий безопасный шаг.
🩺 Проверка — doctor изнутри роутера, ничего не меняет.
📡 PingCheck — watchdog ping, ручная проверка, включить/выключить по туннелю.
🌍 Через туннель? / 🇷🇺 Напрямую? — проверка связности.

⌨ Команды:
/menu — заново показать видимое меню в текущем топике.

🔐 Кабинеты VPN:
Amnezia Premium, HideMy.name и свои VPN-серверы — в приложении: роутер → «Кабинет». Ключи и коды вводятся только там; ключ vpn://, присланный в чат, бот удаляет.

⚙ Очередь:
Если кнопка меняет состояние, команда уходит в очередь агента. Жди результат в этом же топике и не жми повторно без ошибки.`

const helpAdminBody = `

🛡 Админ-команды:
/this_is <nickname> — привязать текущий топик к роутеру.
/ensure_topics — создать недостающие топики.
/recreate_topic — пересоздать топик текущего роутера.
/topic_help — alias на /help.

Парк, обновления агентов, массовые проверки и доступы — в приложении, экран «Парк».
Уведомления всех роутеров приходят вам в личку; под каждым — «🔕 Не писать мне про этот роутер». Вернуть — в приложении, «Парк».`

const helpOperatorBody = `

👤 Ты — оператор.
Работай только в топиках роутеров, куда тебя добавили. Можно смотреть статус, запускать проверки, а VPN-туннели, маршруты и кабинеты VPN — в приложении.

Команды флота, топики и управление доступами доступны только главному админу — в приложении.`

// handleHelpCommand dispatches /help according to role:
//
//   - admin (cfg.AdminUserID match)              → common + admin sections
//   - operator (any owner-or-operator binding)   → common + operator section
//   - stranger                                   → common only (no extras)
//
// Sent as a single plain-text message. Failures are logged but never raised.
func (r *Router) handleHelpCommand(ctx context.Context, m *tg.Message) {
	body := helpCommonBody
	switch r.helpRole(m.From.ID) {
	case "admin":
		body += helpAdminBody
	case "operator":
		body += helpOperatorBody
	}
	if _, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, body, "", nil); err != nil {
		slog.Warn("/help send failed", "err", err, "from", m.From.ID)
	}
}

// helpRole classifies a TG user id into "admin" / "operator" / "none".
func (r *Router) helpRole(userID int64) string {
	if r.cfg.AdminUserID != 0 && userID == r.cfg.AdminUserID {
		return "admin"
	}
	has, err := r.d.Users().HasAnyOperatorOrOwnerBinding(userID)
	if err == nil && has {
		return "operator"
	}
	return "none"
}
