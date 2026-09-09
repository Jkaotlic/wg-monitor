// internal/backend/notify/recipients.go
package notify

import (
	"fmt"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// RecipientsFor -- кому идут уведомления про этот роутер: владельцу и
// операторам, минус заглушившие.
//
// Один источник правды на весь бэкенд. До переезда в личку каждый отправитель
// считал адрес темы сам (EffectiveTelegramChatID + telegram_thread_id), и
// «кому это видно» было размазано по шести местам.
//
// Пустой срез -- не ошибка, а состояние «слать некому»: владелец не привязан
// и операторов нет. Раньше такую тревогу принимала тема группы; после
// переезда её обязан показать дашборд, иначе тревога исчезает молча.
func RecipientsFor(d *db.DB, routerUserID int64) ([]int64, error) {
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

	out := make([]int64, 0, len(ops)+1)
	seen := make(map[int64]bool, len(ops)+1)
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
	return out, nil
}
