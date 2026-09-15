package callbacks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Кнопка «Не писать мне про этот роутер» под уведомлением админу.
//
// Решение оператора 15.09: админу по умолчанию приходит всё по всем роутерам,
// выключение -- по роутеру и личное. Выключает она только уведомления: доступ
// к экранам роутера от неё не зависит, вернуть -- в приложении, экран «Парк».
const (
	adminMuteDoneFmt   = "Больше не пишу про «%s». Вернуть — в приложении, «Парк»."
	adminMuteAdminOnly = "Эта кнопка только для админа."
	adminMuteNoRouter  = "Этого роутера больше нет."
	adminMuteFailed    = "Не получилось выключить — попробуйте ещё раз."
)

// isAdminMuteCallback -- данные этой кнопки. Разбираются до Parse: у кнопки
// два поля ("nmute:<router_id>"), а Parse требует минимум три.
func isAdminMuteCallback(data string) bool {
	return strings.HasPrefix(data, "nmute:")
}

func (r *Router) handleAdminMuteCallback(ctx context.Context, q *tg.CallbackQuery) {
	// Свой гейт, а не isAdminTG: тот пускает всех, когда админ не настроен.
	if r.cfg.AdminUserID == 0 || q.From.ID != r.cfg.AdminUserID {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, adminMuteAdminOnly)
		slog.Warn("nmute: отказ не-админу", "from", q.From.ID, "data", q.Data)
		return
	}
	routerID, ok := notify.ParseAdminMuteCallback(q.Data)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "неизвестная кнопка")
		slog.Warn("nmute: испорченные данные", "data", q.Data)
		return
	}
	u, err := r.d.Users().GetByID(routerID)
	if errors.Is(err, db.ErrUserNotFound) || (err == nil && u == nil) {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, adminMuteNoRouter)
		return
	}
	if err != nil {
		slog.Warn("nmute: роутер не прочитан", "router_id", routerID, "err", err)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, adminMuteFailed)
		return
	}
	if err := r.d.NotifyMutes().SetMuted(q.From.ID, routerID, true); err != nil {
		slog.Warn("nmute: выключатель не записан", "router_id", routerID, "err", err)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, adminMuteFailed)
		return
	}
	slog.Info("nmute: админ выключил уведомления по роутеру", "router_id", routerID, "nickname", u.Nickname)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, fmt.Sprintf(adminMuteDoneFmt, u.Nickname))
}
