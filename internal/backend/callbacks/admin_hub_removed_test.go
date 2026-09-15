package callbacks

import (
	"context"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Хаб /panel уехал в приложение (цикл 2). Команда больше ничего не открывает
// ни в личке админа, ни в группе -- только говорит, куда переехала панель.
func TestAdminSlashPanelNoLongerOpensHub(t *testing.T) {
	for _, chat := range []int64{12345, -100} {
		d, _ := newTestDB(t)
		f := &fakeRouterTGFull{}
		r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

		r.HandleMessage(context.Background(), &tg.Message{
			MessageID: 61, Chat: tg.Chat{ID: chat}, From: tg.User{ID: 12345}, Text: "/panel",
		})

		for _, s := range f.rkSends {
			if strings.Contains(s.text, "Панель управления") {
				t.Fatalf("chat=%d: /panel всё ещё открывает хаб: %q", chat, s.text)
			}
		}
		for _, s := range f.sentMsgs {
			if strings.Contains(s, "Панель управления") {
				t.Fatalf("chat=%d: /panel всё ещё открывает хаб: %q", chat, s)
			}
		}
		// Мышечная память админа: вместо тишины -- короткий ответ, куда
		// переехала панель, и никакой клавиатуры (final review, ledger #38).
		f.mu.Lock()
		sent, markups, rk := append([]string(nil), f.sentMsgs...), len(f.sentMarkups), len(f.rkSends)
		f.mu.Unlock()
		const want = "Панель переехала в приложение: «Парк» в меню бота."
		if len(sent) != 1 || sent[0] != want {
			t.Fatalf("chat=%d: ответ на /panel = %q, ждали один %q", chat, sent, want)
		}
		if markups != 0 || rk != 0 {
			t.Fatalf("chat=%d: /panel прислал клавиатуру: markups=%d rk=%d", chat, markups, rk)
		}
	}
}

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
			r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})

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
