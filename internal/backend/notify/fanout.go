// internal/backend/notify/fanout.go
package notify

import (
	"context"
	"log/slog"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Sender -- та часть tg.Client, которая нужна рассылке. Узкий интерфейс, а не
// весь клиент: тестам не нужен HTTPS-сервер, чтобы проверить веер.
type Sender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

// Fanout рассылает одно уведомление всем, кому оно адресовано.
type Fanout struct {
	d      *db.DB
	s      Sender
	logger *slog.Logger
}

func NewFanout(d *db.DB, s Sender, logger *slog.Logger) *Fanout {
	return &Fanout{d: d, s: s, logger: logger}
}

// Send доставляет текст каждому получателю роутера и возвращает, скольким
// дошло.
//
// Ошибка доставки одному не отменяет остальных: до переезда адрес был один
// (тема группы), и падение отправки означало «никто не узнал». Теперь
// адресатов несколько, и чужая закрытая личка не имеет права глушить чужую
// тревогу.
//
// Ошибку возвращает только невозможность СОСТАВИТЬ список получателей -- это
// поломка базы, а не Telegram.
func (f *Fanout) Send(ctx context.Context, routerUserID int64, text, parseMode string) (int, error) {
	targets, err := RecipientsFor(f.d, routerUserID)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, chatID := range targets {
		if _, err := f.s.SendMessage(ctx, chatID, nil, text, parseMode, nil); err != nil {
			f.noteFailure(chatID, routerUserID, err)
			continue
		}
		delivered++
		f.noteSuccess(chatID)
	}
	return delivered, nil
}

// noteFailure -- общая обработка неудачной доставки.
func (f *Fanout) noteFailure(chatID, routerUserID int64, err error) {
	if tg.IsCantInitiateChat(err) {
		if markErr := f.d.Unreachable().Mark(chatID, err.Error()); markErr != nil && f.logger != nil {
			f.logger.Warn("не удалось пометить недоступного", "telegram_user_id", chatID, "err", markErr)
		}
	}
	if f.logger != nil {
		f.logger.Warn("уведомление не доставлено",
			"telegram_user_id", chatID, "router_user_id", routerUserID, "err", err)
	}
}

// noteSuccess снимает отметку недоступности: человек подхватился.
func (f *Fanout) noteSuccess(chatID int64) {
	if err := f.d.Unreachable().Clear(chatID); err != nil && f.logger != nil {
		f.logger.Warn("не удалось снять отметку недоступности", "telegram_user_id", chatID, "err", err)
	}
}
