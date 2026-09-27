package notify

import "context"

// AdminOnly -- весть только админу, мимо получателей роутера (REV-02: отказ
// авто-оживления владельцу ни к чему). Кнопки выключения ей не нужны: роутер
// не выбирается, это служебное сообщение.
type AdminOnly struct {
	s       Sender
	adminID int64
}

func NewAdminOnly(s Sender, adminID int64) *AdminOnly { return &AdminOnly{s: s, adminID: adminID} }

// SendAdmin шлёт текст админу; админ не настроен -- ничего и без ошибки.
func (a *AdminOnly) SendAdmin(ctx context.Context, text string) error {
	if a == nil || a.s == nil || a.adminID == 0 {
		return nil
	}
	_, err := a.s.SendMessage(ctx, a.adminID, nil, text, "", nil)
	return err
}
