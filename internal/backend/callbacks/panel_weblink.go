package callbacks

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// panelWebLink выдаёт админу личную ссылку на веб-управление.
//
// Кнопка живёт внутри уже существующего хаба /panel: новой команды /admin не
// заводим, поэтому реестр команд, справка и её пин остаются как были.
//
// Честное ограничение: /panel рассчитан на тему роутера в группе, значит из
// лички ссылку так не получить -- там эту работу делает кнопка меню
// мини-аппа.
//
// Выдача не переписывается здесь заново: она живёт в пакете backend, и вторая
// копия срока, лимита и хеширования разошлась бы с первой в первый же месяц.
func (r *Router) panelWebLink(ctx context.Context, q *tg.CallbackQuery) {
	// Свой гейт, а не общий панельный: общий (router.go:356) пропускает
	// всех, когда AdminUserID не настроен вовсе, а ссылка на управление
	// всем парком такой открытой двери не переживёт.
	if r.cfg.AdminUserID == 0 || q.From.ID != r.cfg.AdminUserID {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, backend.WebLinkCopyAdminOnly)
		slog.Warn("веб-ссылка: отказ не-админу", "from", q.From.ID)
		return
	}
	grant, err := backend.IssueWebLink(r.d, q.From.ID, r.cfg.PublicBaseURL, slog.Default())
	switch {
	case errors.Is(err, backend.ErrWebLinkNoPublicBase):
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, backend.WebLinkCopyNoPublicBase)
		return
	case err != nil:
		slog.Warn("веб-ссылка: выдать не удалось", "err", err)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось выдать ссылку")
		return
	}

	var b strings.Builder
	b.WriteString("🌐 Открыть в браузере\n\n")
	b.WriteString(grant.URL)
	b.WriteString("\n\n")
	// Срок и лимит говорит сам ответ: человек решает, пересылать ли ссылку,
	// в ту самую секунду, когда её видит.
	b.WriteString(grant.Notice)
	b.WriteString("\n")
	b.WriteString(grant.LimitNotice)

	kb := panelResultKb()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, b.String(), "", &kb); err != nil {
		slog.Warn("panel weblink edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}
