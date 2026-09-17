package callbacks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Ключи и коды теперь вводятся в приложении. Секрет, присланный в чат по
// старой привычке, не должен висеть в переписке: бот его удаляет и говорит,
// куда идти (цикл 3, решение 10).
func TestCabinetSecretInChatIsDeletedAndAnswered(t *testing.T) {
	cases := []struct {
		name         string
		chatID, from int64
		text         string
		wantDelete   bool
		wantButton   bool
	}{
		{"ключ в личке", 777, 777, "vpn://SECRET-KEY-MUST-NOT-LEAK", true, true},
		{"ключ в группе", -100, 42, "vpn://SECRET-KEY-MUST-NOT-LEAK", true, false},
		{"код в личке", 777, 777, "123456789012345", true, true},
		// Цифры в группе -- не трогать: это бывают телефоны и Telegram ID
		// (решение контроллера цикла 3). Код удаляется только в личке.
		{"цифры в группе", -100, 42, "123456789012345", false, false},
		{"телефон в группе от владельца", -100, 200, "79161234567", false, false},
		{"пароль SSH в личке админа", 42, 42, "id=dacha ssh_host=203.0.113.7 ssh_password=SECRET-PASS", true, true},
		{"пароль SSH в группе", -100, 42, "id=dacha ssh_password=SECRET-PASS", true, false},
		{"ключ в чужой группе", -555, 42, "vpn://SECRET-KEY", false, false},
		{"обычный текст", 777, 777, "привет", false, false},
		{"короткое число", 777, 777, "12345", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newTestDB(t)
			f := &fakeRouterTGFull{}
			r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42, PublicBaseURL: "https://wgmon.example.com"})
			r.HandleMessage(context.Background(), &tg.Message{MessageID: 55, Chat: tg.Chat{ID: tc.chatID}, From: tg.User{ID: tc.from}, Text: tc.text})

			gotDelete := len(f.deleted) == 1 && f.deleted[0].msgID == 55 && f.deleted[0].chatID == tc.chatID
			if gotDelete != tc.wantDelete {
				t.Fatalf("удаление: %+v, ждали %v", f.deleted, tc.wantDelete)
			}
			if !tc.wantDelete {
				if len(f.sentMsgs)+len(f.rkSends) != 0 {
					t.Fatalf("лишний ответ: %q %+v", f.sentMsgs, f.rkSends)
				}
				return
			}
			var text string
			if tc.wantButton {
				if len(f.rkSends) != 1 || len(f.sentMsgs) != 0 {
					t.Fatalf("ждали один ответ с кнопкой: %+v %q", f.rkSends, f.sentMsgs)
				}
				kb, ok := f.rkSends[0].markup.(*tg.InlineKeyboardMarkup)
				if !ok || len(kb.InlineKeyboard) != 1 || kb.InlineKeyboard[0][0].WebApp == nil || kb.InlineKeyboard[0][0].WebApp.URL != "https://wgmon.example.com/miniapp/" {
					t.Fatalf("кнопка приложения: %+v", f.rkSends[0].markup)
				}
				text = f.rkSends[0].text
			} else {
				// web_app-кнопку Telegram разрешает только в личке.
				if len(f.sentMsgs) != 1 || len(f.rkSends) != 0 {
					t.Fatalf("в группе -- текст без кнопки: %+v %q", f.rkSends, f.sentMsgs)
				}
				text = f.sentMsgs[0]
			}
			if !strings.Contains(text, "в приложении") || strings.Contains(text, "SECRET") {
				t.Fatalf("ответ: %q", text)
			}
		})
	}
}

func TestCabinetSecretGuardWhenDeleteFailsOrNoAppURL(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{deleteErr: errors.New("message can't be deleted")}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	r.HandleMessage(context.Background(), &tg.Message{MessageID: 56, Chat: tg.Chat{ID: 777}, From: tg.User{ID: 777}, Text: "vpn://SECRET-KEY"})
	if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], "Удалите") || len(f.rkSends) != 0 {
		t.Fatalf("без адреса приложения и без удаления: %q %+v", f.sentMsgs, f.rkSends)
	}
}

// Команды и кнопки кабинетов из бота удалены: ни админу, ни владельцу бот на
// них больше ничего не отвечает.
func TestRemovedCabinetCommandsAndButtonsAreSilent(t *testing.T) {
	for _, text := range []string{"/amnezia", "/hidemy", "/selfhosted", "/selfhosted add id=home", "/cancel", "🔐 Amnezia Premium", "🔑 HideMy.name", "Amnezia Premium", "HideMy.name"} {
		for _, from := range []int64{42, 200} {
			d, uid := newTestDB(t)
			if err := d.Users().UpdateThreadID(uid, 55); err != nil {
				t.Fatal(err)
			}
			if err := d.Users().SetTelegramUserID(uid, 200); err != nil {
				t.Fatal(err)
			}
			f := &fakeRouterTGFull{}
			r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
			tid := int64(55)
			r.HandleMessage(context.Background(), &tg.Message{MessageID: 9, Chat: tg.Chat{ID: -100}, From: tg.User{ID: from}, MessageThreadID: &tid, Text: text})
			if len(f.sentMsgs)+len(f.rkSends) != 0 {
				t.Errorf("%q от %d: бот ответил %q %+v", text, from, f.sentMsgs, f.rkSends)
			}
		}
	}
}

// Старая кнопка кабинета в чате -- «неизвестная кнопка», а не молчание.
func TestOldCabinetButtonAnswersUnknown(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	r.HandleCallback(context.Background(), &tg.CallbackQuery{ID: "cb", From: tg.User{ID: 42}, Data: "amz_refresh:" + itoa(uid) + ":_panel_",
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 7}})
	if len(f.answers) != 1 || f.answers[0] != "неизвестная кнопка" || len(f.edits) != 0 {
		t.Fatalf("answers=%q edits=%q", f.answers, f.edits)
	}
}
