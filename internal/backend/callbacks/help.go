package callbacks

import (
	"context"
	"log/slog"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

const helpCommonBody = `ℹ Помощь по боту

📛 Алерты:
✅ работает; 🟡 подозрение; 🔴 подтверждённый сбой; 📵 роутер offline.
Сначала жми 📊 Что происходит? или 🩺 Проверка. Рестарты и импорт .conf запускай после диагностики.

🎛 Кнопки в топике роутера:
📊 Что происходит? — короткая сводка и следующий безопасный шаг.
🩺 Проверка — doctor изнутри роутера, ничего не меняет.
🎛 Туннели — состояние, включить/выключить, рестарт awg-manager.
🛣 Маршруты — DNS/Static правила, перенос, добавление и удаление правил.
📡 PingCheck — watchdog ping, ручная проверка, включить/выключить по туннелю.
🛠 Обслуживание — версии, рестарты hrneo/awg-manager/router, прошивка, opkg.
🌍 Через тоннель? / 🇷🇺 Напрямую? — проверка связности.

🔐 Premium:
Amnezia Premium — пришли vpn:// ключ в топик роутера, открой кабинет, выбери "Выгрузить .conf" и страну.
HideMy.name — пришли цифровой access code в топик роутера, открой кабинет, выбери сервер.
Бот хранит несколько ключей/кодов на один топик, удаляет сообщение с secret и отправляет AmneziaWG 2.0 .conf документом. Импорт туннеля ставится в очередь роутера.

⚙ Очередь:
Если кнопка меняет состояние, команда уходит в очередь агента. Жди результат в этом же топике и не жми повторно без ошибки.`

const helpAdminBody = `

🛡 Админ-команды:
/panel — главный хаб: флот, статусы, doctor, туннели, маршруты, PingCheck, обслуживание, доступы и справочник оператора.
/this_is <nickname> — привязать текущий топик к роутеру.
/ensure_topics — создать недостающие топики.
/recreate_topic — пересоздать топик текущего роутера.
/topic_help — alias на /help.

Флот:
🔎 Аудит всех, 🩺 Все роутеры и ⬆ Обновить все ставят команды в queue по каждому роутеру. self_update считается успешным только после heartbeat с новой версией.`

const helpOperatorBody = `

👤 Ты — оператор.
Работай только в топиках роутеров, куда тебя добавили. Можно смотреть статус, запускать проверки, управлять туннелями/маршрутами, обслуживанием и Premium-кабинетами этого топика.

Команды флота, топики, /panel и управление доступами доступны только главному админу.`

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
