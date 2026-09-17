package callbacks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// .conf, присланный боту в личку, -- приватный ключ в переписке. Бот его
// удаляет и отвечает, куда идти, с кнопкой приложения. В группе -- молчание
// (цикл 5): TestGroupMessagesAreSilent.
func TestConfDocumentIsDeletedAndAnswered(t *testing.T) {
	cases := []struct {
		name       string
		file       string
		wantDelete bool
	}{
		{"конфиг", "home.conf", true},
		{"конфиг заглавными", "NL.CONF", true},
		{"не конфиг", "report.pdf", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newTestDB(t)
			f := &fakeRouterTG{}
			r := NewRouter(d, f, Config{AdminUserID: 42, PublicBaseURL: "https://wgmon.example.com"})
			r.HandleMessage(context.Background(), &tg.Message{MessageID: 55, Chat: tg.Chat{ID: 777}, From: tg.User{ID: 777},
				Document: &tg.Document{FileID: "f", FileName: tc.file, FileSize: 400}})
			if !tc.wantDelete {
				if !f.silent() {
					t.Fatalf("лишний ответ: %q %+v %+v", f.sentMsgs, f.rkSends, f.deleted)
				}
				return
			}
			if len(f.deleted) != 1 || f.deleted[0].msgID != 55 {
				t.Fatalf("удаление: %+v", f.deleted)
			}
			if len(f.rkSends) != 1 {
				t.Fatalf("ждали ответ с кнопкой: %+v %q", f.rkSends, f.sentMsgs)
			}
			kb, ok := f.rkSends[0].markup.(*tg.InlineKeyboardMarkup)
			if !ok || kb.InlineKeyboard[0][0].WebApp == nil || kb.InlineKeyboard[0][0].WebApp.URL != "https://wgmon.example.com/miniapp/" {
				t.Fatalf("кнопка: %+v", f.rkSends[0].markup)
			}
			if text := f.rkSends[0].text; !strings.Contains(text, "в приложении") || !strings.Contains(text, "Файл удалён") {
				t.Fatalf("ответ: %q", text)
			}
		})
	}
}

func TestConfDocumentDeleteFails(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{deleteErr: errors.New("message can't be deleted")}
	r := NewRouter(d, f, Config{AdminUserID: 42})
	r.HandleMessage(context.Background(), &tg.Message{MessageID: 56, Chat: tg.Chat{ID: 777}, From: tg.User{ID: 777},
		Document: &tg.Document{FileID: "f", FileName: "home.conf"}})
	if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], "Удалите") || len(f.rkSends) != 0 {
		t.Fatalf("без адреса приложения и без удаления: %q %+v", f.sentMsgs, f.rkSends)
	}
}
