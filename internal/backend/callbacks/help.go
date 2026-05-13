package callbacks

import (
	"context"
	"log/slog"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

const helpCommonBody = `ℹ Помощь по боту

📛 Алерты:
✅ — всё работает; 🟡 — soft (одна проверка не прошла, ждём подтверждения);
🔴 — hard (проблема устойчива, эскалирована); 📵 — роутер недоступен.

🎛 Кнопки в топике (per_router):
📊 Что происходит? — короткий smart-reply (текущее состояние + советы).
🎛 Туннели — снапшот по туннелям, кнопки enable/disable.
🛣 Маршруты — DNS/Static маршруты + rebind между туннелями.
🛠 Обслуживание — version_audit, restart awg-mgr/hrneo/router, прошивка.
🌍 Через тоннель? / 🇷🇺 Напрямую? — проверка связности.
⬆ Обновить пакеты — opkg update + upgrade на роутере.

🔘 Inline-кнопки под HARD-алертом:
⏸ Тише 1ч/4ч/24ч, ✅ Понял, 🔇 Тихо до утра, 📋 История за 24ч,
🔁 Перезапуск туннеля, 📊 Диагностика, ▶ Тест связи.`

const helpAdminBody = `

🛡 Админ-команды:
/panel — главный хаб (🛠 Обслуживание · 🛣 Маршруты · 📊 Status · 🪄 Оживить топики · 👥 Доступ).
/this_is <nickname> — привязать текущий топик к роутеру.
/ensure_topics — создать недостающие топики.
/recreate_topic — пересоздать топик текущего роутера.
/topic_help — alias на /help (старая команда).`

const helpOperatorBody = `

👤 Ты — оператор. Можешь всё то же, что владелец роутера (см. список выше), кроме админских команд (ensure_topics, this_is и т.п.).`

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
