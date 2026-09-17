package callbacks

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Ответ на /start (цикл 5, решение 2): кто бы ни написал боту, он узнаёт, что
// это и куда нажать. Тому, у кого нет доступа ни к одному роутеру, -- его
// Telegram ID: без него администратор доступ не выдаст, а сам человек свой
// номер нигде не видит.
const (
	startCardText    = "wg-monitor — состояние ваших роутеров и починка в приложении.\n\nСюда приходят уведомления о поломках; всё управление — в приложении."
	startNoURLText   = "\n\nАдрес приложения ещё не настроен — спросите администратора."
	startOpenAppText = "Открыть приложение"
)

// startNoAccessText -- приписка человеку без доступа. HTML: номер в <code>,
// чтобы его копировали нажатием.
func startNoAccessText(telegramUserID int64) string {
	return "\n\nВаш Telegram ID: <code>" + strconv.FormatInt(telegramUserID, 10) +
		"</code> — передайте его администратору, чтобы он выдал доступ."
}

func (r *Router) handleStart(ctx context.Context, m *tg.Message) {
	text := startCardText
	appURL := tg.MiniAppURL(r.cfg.PublicBaseURL)
	if appURL == "" {
		text += startNoURLText
	}
	if !r.startHasAccess(m.From.ID) {
		text += startNoAccessText(m.From.ID)
	}
	if appURL == "" {
		if _, err := r.tg.SendMessage(ctx, m.Chat.ID, nil, text, "HTML", nil); err != nil {
			slog.Warn("/start: ответ не отправлен", "chat", m.Chat.ID, "err", err)
		}
		return
	}
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: startOpenAppText, WebApp: &tg.WebAppInfo{URL: appURL}},
	}}}
	if _, err := r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, nil, text, "HTML", nil, &kb); err != nil {
		slog.Warn("/start: ответ не отправлен", "chat", m.Chat.ID, "err", err)
	}
}

// startHasAccess -- админ или владелец/оператор хотя бы одного роутера.
// Ошибка базы -- «доступа не видно»: лишний номер в ответе безвреден.
func (r *Router) startHasAccess(telegramUserID int64) bool {
	if r.cfg.AdminUserID != 0 && telegramUserID == r.cfg.AdminUserID {
		return true
	}
	has, err := r.d.Users().HasAnyOperatorOrOwnerBinding(telegramUserID)
	if err != nil {
		slog.Warn("/start: доступ не прочитан", "from", telegramUserID, "err", err)
		return false
	}
	return has
}
