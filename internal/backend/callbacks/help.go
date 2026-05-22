package callbacks

import (
	"context"
	"log/slog"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

const helpCommonBody = `ℹ Помощь по боту

📛 Алерты:
✅ — всё работает; 🟡 — подозрение на сбой (одна проверка не прошла, ждём подтверждения);
🔴 — устойчивый сбой; 📵 — роутер недоступен.

🎛 Кнопки в топике роутера:
📊 Что происходит? — короткая сводка: текущее состояние, что упало и что делать.
🎛 Туннели — снимок по туннелям, кнопки включить/выключить.
🛣 Маршруты — DNS/Static маршруты + перенос правил между туннелями.
🛠 Обслуживание — версии, перезапуск awg-manager/hrneo/router, прошивка.
🩺 Проверка — безопасная проверка изнутри роутера: awg-manager, туннели, PingCheck, процессы и маршрут.
🌍 Через тоннель? / 🇷🇺 Напрямую? — проверка связности.
⬆ Обновить пакеты — opkg update + upgrade на роутере.

🔘 Inline-кнопки под HARD-алертом:
⏸ Тише 1ч/4ч/24ч, ✅ Понял, 🔇 Тихо до утра, 📋 История за 24ч,
🔁 Перезапуск туннеля, 📊 Диагностика, ▶ Тест связи.`

const helpAdminBody = `

🛡 Админ-команды:
/panel — главный хаб (📊 Статус · 🩺 Проверка · 🎛 Туннели · 🛣 Маршруты · 📡 PingCheck · 🛠 Обслуживание · 👥 Доступ).
/this_is <nickname> — привязать текущий топик к роутеру.
/ensure_topics — создать недостающие топики.
/recreate_topic — пересоздать топик текущего роутера.
/topic_help — alias на /help (старая команда).`

const helpOperatorBody = `

👤 Ты — оператор. Можешь работать только в топиках роутеров, куда тебя добавили:
📊 смотреть статус, 🩺 запускать безопасную проверку, 🌍/🇷🇺 проверять маршруты, 🎛 управлять туннелями, 🛣 смотреть маршруты и 🛠 открывать обслуживание.

Команды управления флотом, топиками и доступами доступны только главному администратору.`

// handleHelpCommand dispatches /help according to role:
//
//   - admin (cfg.AdminUserID match)              → common + admin sections
//   - operator (any owner-or-operator binding)   → common + operator section
//   - stranger                                   → common only (no extras)
//
// Sent as a single plain-text message. Failures are logged but never raised
// — /help is non-critical and must never block the message pipeline.
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
