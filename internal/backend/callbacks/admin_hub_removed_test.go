package callbacks

import (
	"context"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Кнопки хаба и доступов остались в старых сообщениях. Нажатие отвечает
// «неизвестная кнопка» и ничего не делает -- ни правок, ни очереди.
func TestOldHubAndAccessButtonsAreUnknown(t *testing.T) {
	for _, data := range []string{
		"panel:0:home", "panel:0:kind:tunnels", "panel:7:push:status", "panel:0:doctor_all",
		"panel:0:update_all_do", "panel:0:awaken_do", "panel:0:weblink", "panel:0:mobile",
		"access:0:home", "access:0:router:1", "access:0:remove_op_confirm:1:777",
	} {
		t.Run(data, func(t *testing.T) {
			d, _ := newTestDB(t)
			f := &fakeRouterTGFull{}
			r := NewRouter(d, f, Config{AdminUserID: 12345})

			r.HandleCallback(context.Background(), &tg.CallbackQuery{
				ID: "cb-old", From: tg.User{ID: 12345}, Data: data,
				Message: tg.Message{Chat: tg.Chat{ID: 12345}, MessageID: 9},
			})

			if len(f.answers) != 1 || f.answers[0] != "неизвестная кнопка" {
				t.Fatalf("ответы %q, ждали «неизвестная кнопка»", f.answers)
			}
			if len(f.edits) != 0 || len(f.rkSends) != 0 || len(f.sentMsgs) != 0 {
				t.Fatalf("старая кнопка что-то сделала: edits=%q rk=%d sent=%q", f.edits, len(f.rkSends), f.sentMsgs)
			}
		})
	}
}
