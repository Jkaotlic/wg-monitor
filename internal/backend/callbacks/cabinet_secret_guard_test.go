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
		{"код в личке", 777, 777, "123456789012345", true, true},
		{"пароль SSH в личке админа", 42, 42, "id=dacha ssh_host=203.0.113.7 ssh_password=SECRET-PASS", true, true},
		// Цикл 5: в группах бот ничего не делает, сторож -- тоже.
		{"ключ в группе", -100, 42, "vpn://SECRET-KEY-MUST-NOT-LEAK", false, false},
		{"пароль SSH в группе", -100, 42, "id=dacha ssh_password=SECRET-PASS", false, false},
		// /start отвечает после сторожа -- секрет рядом с ней всё равно удаляется.
		{"ключ рядом с /start", 777, 777, "/start vpn://SECRET-KEY-MUST-NOT-LEAK", true, true},
		{"обычный текст", 777, 777, "привет", false, false},
		{"короткое число", 777, 777, "12345", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newTestDB(t)
			f := &fakeRouterTGFull{}
			r := NewRouter(d, f, Config{AdminUserID: 42, PublicBaseURL: "https://wgmon.example.com"})
			r.HandleMessage(context.Background(), &tg.Message{MessageID: 55, Chat: tg.Chat{ID: tc.chatID}, From: tg.User{ID: tc.from}, Text: tc.text})

			gotDelete := len(f.deleted) == 1 && f.deleted[0].msgID == 55 && f.deleted[0].chatID == tc.chatID
			if gotDelete != tc.wantDelete {
				t.Fatalf("удаление: %+v, ждали %v", f.deleted, tc.wantDelete)
			}
			if !tc.wantDelete {
				// Ответ на /start -- не ответ сторожа: он проверяется своими тестами.
				if !strings.HasPrefix(strings.TrimSpace(tc.text), "/start") && len(f.sentMsgs)+len(f.rkSends) != 0 {
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
				if len(f.sentMsgs) != 1 || len(f.rkSends) != 0 {
					t.Fatalf("ждали текст без кнопки: %+v %q", f.rkSends, f.sentMsgs)
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
	r := NewRouter(d, f, Config{AdminUserID: 42})
	r.HandleMessage(context.Background(), &tg.Message{MessageID: 56, Chat: tg.Chat{ID: 777}, From: tg.User{ID: 777}, Text: "vpn://SECRET-KEY"})
	if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], "Удалите") || len(f.rkSends) != 0 {
		t.Fatalf("без адреса приложения и без удаления: %q %+v", f.sentMsgs, f.rkSends)
	}
}

// Старая кнопка кабинета в чате -- «неизвестная кнопка», а не молчание.
func TestOldCabinetButtonAnswersUnknown(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{AdminUserID: 42})
	r.HandleCallback(context.Background(), &tg.CallbackQuery{ID: "cb", From: tg.User{ID: 42}, Data: "amz_refresh:" + itoa(uid) + ":_panel_",
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 7}})
	if len(f.answers) != 1 || f.answers[0] != "неизвестная кнопка" || len(f.edits) != 0 {
		t.Fatalf("answers=%q edits=%q", f.answers, f.edits)
	}
}

// Секрет в подписи к файлу или фото виден в чате так же, как в тексте.
func TestCabinetSecretInCaptionIsDeleted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chatID  int64
		caption string
	}{
		{"ключ в подписи к файлу", 777, "vpn://SECRET-KEY-IN-CAPTION"},
		{"код в подписи", 777, "123456789012345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newTestDB(t)
			f := &fakeRouterTGFull{}
			r := NewRouter(d, f, Config{AdminUserID: 42})
			from := tc.chatID
			r.HandleMessage(context.Background(), &tg.Message{MessageID: 57, Chat: tg.Chat{ID: tc.chatID}, From: tg.User{ID: from},
				Caption: tc.caption, Document: &tg.Document{FileID: "f1", FileName: "x.conf"}})
			if len(f.deleted) != 1 || f.deleted[0].msgID != 57 {
				t.Fatalf("подпись с секретом не удалена: %+v", f.deleted)
			}
		})
	}
}
