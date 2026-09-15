// internal/backend/notify/recipients.go
package notify

import (
	"fmt"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// RecipientsFor -- кому идут уведомления про этот роутер: владельцу,
// операторам и админу, минус заглушившие.
//
// Один источник правды на весь бэкенд. До переезда в личку каждый отправитель
// считал адрес темы сам (EffectiveTelegramChatID + telegram_thread_id), и
// «кому это видно» было размазано по шести местам.
//
// Админ идёт последним и через тот же отсев: админ-владелец получает одно
// сообщение, а выключивший роутер админ -- ни одного. Решение оператора
// 15.09: админу по умолчанию всё по всем роутерам, выключение -- по роутеру.
// adminID == 0 (админ не настроен) -- список прежний.
//
// Пустой срез -- не ошибка, а состояние «слать некому»: владелец не привязан,
// операторов нет и админа нет. Такую тревогу обязан показать дашборд, иначе
// она исчезает молча.
func RecipientsFor(d *db.DB, routerUserID int64, adminID int64) ([]int64, error) {
	return collectRecipients(d, routerUserID, adminID)
}

// OwnerAndOperators -- люди роутера без админа. Нужна сводке «роутеры без
// получателей»: админ теперь слышит всех, и посчитай его сводка -- список был
// бы пуст всегда. Выключатель действует так же, как в RecipientsFor: владелец,
// который выключил уведомления, их не услышит, значит у роутера нет адресата.
func OwnerAndOperators(d *db.DB, routerUserID int64) ([]int64, error) {
	return collectRecipients(d, routerUserID, 0)
}

func collectRecipients(d *db.DB, routerUserID int64, adminID int64) ([]int64, error) {
	u, err := d.Users().GetByID(routerUserID)
	if err != nil {
		return nil, fmt.Errorf("recipients: %w", err)
	}
	muted, err := d.NotifyMutes().MutedBy(routerUserID)
	if err != nil {
		return nil, fmt.Errorf("recipients: mutes: %w", err)
	}
	ops, err := d.RouterOperators().List(routerUserID)
	if err != nil {
		return nil, fmt.Errorf("recipients: operators: %w", err)
	}

	out := make([]int64, 0, len(ops)+2)
	seen := make(map[int64]bool, len(ops)+2)
	add := func(id int64) {
		if id == 0 || seen[id] || muted[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	if u != nil && u.TelegramUserID != nil {
		add(*u.TelegramUserID)
	}
	for _, op := range ops {
		add(op.TelegramUserID)
	}
	add(adminID)
	return out, nil
}
