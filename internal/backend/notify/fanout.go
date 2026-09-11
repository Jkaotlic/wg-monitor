// internal/backend/notify/fanout.go
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Sender -- та часть tg.Client, которая нужна рассылке. Узкий интерфейс, а не
// весь клиент: тестам не нужен HTTPS-сервер, чтобы проверить веер.
type Sender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

// ErrNoneDelivered -- получатели были, но не дошло никому. Отличается от
// «слать некому» (получателей ноль) намеренно: первое означает, что Telegram
// или сеть подвели и тревогу надо повторить, второе -- что повторять её
// некому и напоминания будут молотить впустую.
//
// Получатели, до которых не достучаться, пока человек сам не откроет дверь
// (tg.IsUnreachableChat), считаются как «слать некому»: повтор на каждом
// обходе их не вернёт. 11.09.2026 сторож ровно так слал «роутер не на связи»
// в «chat not found» на каждом обходе -- 1112 ошибок из 1112 обходов. Такие
// люди видны оператору в сводке дашборда (notify.unreachable), а отметку
// снимает первая удачная доставка -- в срок обычного напоминания.
var ErrNoneDelivered = errors.New("уведомление не доставлено ни одному получателю")

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
	var lastErr error
	for _, chatID := range targets {
		if _, err := f.s.SendMessage(ctx, chatID, nil, text, parseMode, nil); err != nil {
			if !f.noteFailure(chatID, routerUserID, err) {
				lastErr = err
			}
			continue
		}
		delivered++
		f.noteSuccess(chatID)
	}
	return f.result(delivered, len(targets), lastErr)
}

// result -- общий вердикт рассылки. lastErr -- последняя ошибка, которую есть
// смысл повторять; недоступные получатели в неё не попадают. Поэтому «не дошло
// никому, и все были недоступны» -- это ноль без ошибки, то есть «слать
// некому», а не ErrNoneDelivered.
//
// Исходную ошибку Telegram оборачиваем внутрь: по ней вызывающий разбирает,
// был ли это лимит частоты, и решает, когда повторить.
func (f *Fanout) result(delivered, targets int, lastErr error) (int, error) {
	if delivered == 0 && lastErr != nil {
		return 0, fmt.Errorf("%w: получателей %d: %w", ErrNoneDelivered, targets, lastErr)
	}
	return delivered, nil
}

// SendKeyboard рассылает текст с кнопками, НЕ запоминая сообщения. Нужен
// напоминаниям: «починилось» обязано отвечать на корневую тревогу, а не на
// последнее напоминание.
func (f *Fanout) SendKeyboard(ctx context.Context, routerUserID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) (int, error) {
	targets, err := RecipientsFor(f.d, routerUserID)
	if err != nil {
		return 0, err
	}
	ks, hasKeyboard := f.s.(KeyboardSender)
	delivered := 0
	var lastErr error
	for _, chatID := range targets {
		var sendErr error
		if hasKeyboard && kb != nil {
			_, sendErr = ks.SendMessageWithKeyboard(ctx, chatID, nil, text, parseMode, nil, kb)
		} else {
			_, sendErr = f.s.SendMessage(ctx, chatID, nil, text, parseMode, nil)
		}
		if sendErr != nil {
			if !f.noteFailure(chatID, routerUserID, sendErr) {
				lastErr = sendErr
			}
			continue
		}
		delivered++
		f.noteSuccess(chatID)
	}
	return f.result(delivered, len(targets), lastErr)
}

// noteFailure -- общая обработка неудачной доставки. Возвращает true, если
// получатель недоступен до тех пор, пока сам не откроет дверь: такую ошибку
// повторять бессмысленно, и в вердикт рассылки она не идёт.
func (f *Fanout) noteFailure(chatID, routerUserID int64, err error) bool {
	unreachable := tg.IsUnreachableChat(err)
	if unreachable {
		if markErr := f.d.Unreachable().Mark(chatID, err.Error()); markErr != nil && f.logger != nil {
			f.logger.Warn("не удалось пометить недоступного", "telegram_user_id", chatID, "err", markErr)
		}
	}
	if f.logger != nil {
		f.logger.Warn("уведомление не доставлено",
			"telegram_user_id", chatID, "router_user_id", routerUserID, "err", err)
	}
	return unreachable
}

// noteSuccess снимает отметку недоступности: человек подхватился.
func (f *Fanout) noteSuccess(chatID int64) {
	if err := f.d.Unreachable().Clear(chatID); err != nil && f.logger != nil {
		f.logger.Warn("не удалось снять отметку недоступности", "telegram_user_id", chatID, "err", err)
	}
}

// KeyboardSender -- отправка с кнопками. Отдельным интерфейсом, потому что
// уведомления без кнопок (отчёт о починке, результат деплоя) обходятся
// Sender'ом и не должны тащить лишнюю зависимость в свои тесты.
type KeyboardSender interface {
	Sender
	SendMessageWithKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup *tg.InlineKeyboardMarkup) (int64, error)
}

// SendTracked рассылает тревогу и запоминает, кому какое сообщение ушло.
// Дальше «восстановилось» отвечает каждому на его собственное сообщение, а
// мини-апп дописывает в него статус.
//
// Если отправитель не умеет кнопок, уведомление уходит без них: потерять
// кнопку лучше, чем потерять тревогу.
func (f *Fanout) SendTracked(ctx context.Context, routerUserID int64, checkName, text, parseMode string, kb *tg.InlineKeyboardMarkup) (int, error) {
	targets, err := RecipientsFor(f.d, routerUserID)
	if err != nil {
		return 0, err
	}
	ks, hasKeyboard := f.s.(KeyboardSender)
	delivered := 0
	var lastErr error
	for _, chatID := range targets {
		var mid int64
		var sendErr error
		if hasKeyboard && kb != nil {
			mid, sendErr = ks.SendMessageWithKeyboard(ctx, chatID, nil, text, parseMode, nil, kb)
		} else {
			mid, sendErr = f.s.SendMessage(ctx, chatID, nil, text, parseMode, nil)
		}
		if sendErr != nil {
			if !f.noteFailure(chatID, routerUserID, sendErr) {
				lastErr = sendErr
			}
			continue
		}
		delivered++
		if err := f.d.AlertMessages().Put(routerUserID, checkName, chatID, mid); err != nil && f.logger != nil {
			f.logger.Warn("не удалось запомнить сообщение тревоги",
				"telegram_user_id", chatID, "check", checkName, "err", err)
		}
		f.noteSuccess(chatID)
	}
	return f.result(delivered, len(targets), lastErr)
}

// ReplyToEach отвечает каждому получателю на его собственное сообщение о
// поломке. Кому сообщения не досталось (был недоступен, подключился позже),
// получает обычное сообщение без привязки -- лучше без ветки переписки, чем
// вообще без «починилось».
func (f *Fanout) ReplyToEach(ctx context.Context, routerUserID int64, checkName, text, parseMode string) error {
	targets, err := RecipientsFor(f.d, routerUserID)
	if err != nil {
		return err
	}
	msgs, err := f.d.AlertMessages().List(routerUserID, checkName)
	if err != nil {
		return err
	}
	for _, chatID := range targets {
		var replyTo *int64
		if mid, ok := msgs[chatID]; ok {
			replyTo = &mid
		}
		if _, err := f.s.SendMessage(ctx, chatID, nil, text, parseMode, replyTo); err != nil {
			f.noteFailure(chatID, routerUserID, err)
			continue
		}
		f.noteSuccess(chatID)
	}
	return nil
}

// ReplyKeyboardSender -- отправка с нижней клавиатурой. Ею пользуется отчёт о
// пробуждении мобильного роутера: там кнопки не под сообщением, а панелью.
type ReplyKeyboardSender interface {
	Sender
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
}

// SendWithReplyKeyboard рассылает текст с нижней клавиатурой. Если отправитель
// её не умеет, уведомление уходит без панели: потерять кнопки лучше, чем
// потерять сообщение.
func (f *Fanout) SendWithReplyKeyboard(ctx context.Context, routerUserID int64, text, parseMode string, markup any) (int, error) {
	targets, err := RecipientsFor(f.d, routerUserID)
	if err != nil {
		return 0, err
	}
	rks, hasKeyboard := f.s.(ReplyKeyboardSender)
	delivered := 0
	var lastErr error
	for _, chatID := range targets {
		var sendErr error
		if hasKeyboard && markup != nil {
			_, sendErr = rks.SendMessageWithReplyKeyboard(ctx, chatID, nil, text, parseMode, nil, markup)
		} else {
			_, sendErr = f.s.SendMessage(ctx, chatID, nil, text, parseMode, nil)
		}
		if sendErr != nil {
			if !f.noteFailure(chatID, routerUserID, sendErr) {
				lastErr = sendErr
			}
			continue
		}
		delivered++
		f.noteSuccess(chatID)
	}
	return f.result(delivered, len(targets), lastErr)
}
