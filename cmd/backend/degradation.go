package main

import (
	"context"
	"time"
)

// Фоновая горутина бэкенда умерла -- об этом обязан узнать человек: /healthz
// продолжает отвечать 200, и молчание бота читается как «всё тихо».
//
// Адресат -- личка админа (цикл 5: группы больше нет). Текст -- обычный, без
// разметки: имя компонента и ошибка Go приходят как есть, а в MarkdownV2
// любая скобка или дефис в них рвали бы отправку целиком, и сообщение о
// поломке терялось бы из-за собственной разметки.

type degradationSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

func degradationText(component string, err error) string {
	return "🛑 Бэкенд: остановился «" + component + "».\n\n" + err.Error() + "\n\nНужен перезапуск сервиса."
}

// notifyDegradation пишет админу в личку. adminUserID == 0 (админ не задан) --
// писать некому. Ошибку отправки возвращает вызывающему для журнала.
func notifyDegradation(sender degradationSender, adminUserID int64, component string, err error) error {
	if err == nil || adminUserID == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, sendErr := sender.SendMessage(ctx, adminUserID, nil, degradationText(component, err), "", nil)
	return sendErr
}
