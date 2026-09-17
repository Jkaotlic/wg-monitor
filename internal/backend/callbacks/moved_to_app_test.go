package callbacks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Старые кнопки панелей туннелей, маршрутов и перезапуска служб висят в
// чатах: нажатие отвечает «Это теперь в приложении», ничего не ставит в
// очередь и сообщение не правит.
func TestMovedToAppButtonsAnswerToast(t *testing.T) {
	d, uid := newTestDB(t)
	for _, data := range []string{
		fmt.Sprintf("tunnels_refresh:%d:_panel_", uid),
		fmt.Sprintf("tunnel_restart:%d:tunnel_awg11:Wireguard3", uid),
		fmt.Sprintf("tunnel_enable:%d:tunnel_awg11:Wireguard3", uid),
		fmt.Sprintf("tunnel_disable:%d:tunnel_awg11:Wireguard3", uid),
		fmt.Sprintf("tunnel_delete_ask:%d:_panel_::awg11", uid),
		fmt.Sprintf("tunnel_delete:%d:tunnel_awg11:Wireguard3:awg11", uid),
		fmt.Sprintf("restart_tunnel:%d:_panel_", uid),
		fmt.Sprintf("tunnel_import_add:%d:_panel_:a1b2c3d4", uid),
		fmt.Sprintf("routes_open:%d:_panel_", uid),
		fmt.Sprintf("routes_confirm:%d:awg11:awg12:a1b2c3d4", uid),
		fmt.Sprintf("routes_del_confirm:%d:_panel_:a1b2c3d4:b1b2c3d4", uid),
		"routes_close:0:_panel_",
		fmt.Sprintf("maint_restart:%d:hrneo", uid),
		fmt.Sprintf("maint_confirm:%d:hrneo_stop:a1b2c3d4", uid),
		"panel:0:help:tunnels",
		"panel:0:help:routes",
		"compat_btn:0:tunnels",
		"compat_btn:0:routes",
	} {
		f := &fakeRouterTG{}
		sink := &fakeEnqueuer{}
		r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
		r.HandleCallback(context.Background(), &tg.CallbackQuery{ID: "cb", From: tg.User{ID: 12345}, Data: data,
			Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 7}})
		if len(f.answers) != 1 || f.answers[0] != "Это теперь в приложении" || len(f.edits) != 0 || len(sink.calls) != 0 {
			t.Errorf("%s: answers=%q edits=%q calls=%+v", data, f.answers, f.edits, sink.calls)
		}
	}
	// Живые кнопки не задеты.
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 12345})
	r.HandleCallback(context.Background(), &tg.CallbackQuery{ID: "cb", From: tg.User{ID: 12345}, Data: "panel:0:help:pingcheck",
		Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 7}})
	if len(f.edits) != 1 {
		t.Fatalf("справка PingCheck сломалась: answers=%q edits=%d", f.answers, len(f.edits))
	}
}

// .conf, присланный боту, -- приватный ключ в переписке. Бот его удаляет и
// отвечает, куда идти: в личке -- с кнопкой приложения, в группе -- текстом.
func TestConfDocumentIsDeletedAndAnswered(t *testing.T) {
	cases := []struct {
		name         string
		chatID, from int64
		file         string
		wantDelete   bool
		wantButton   bool
	}{
		{"конфиг в личке", 777, 777, "home.conf", true, true},
		{"конфиг в группе", -100, 42, "NL.CONF", true, false},
		{"конфиг в чужой группе", -555, 42, "home.conf", false, false},
		{"не конфиг", 777, 777, "report.pdf", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newTestDB(t)
			f := &fakeRouterTGFull{}
			r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42, PublicBaseURL: "https://wgmon.example.com"})
			tid := int64(55)
			r.HandleMessage(context.Background(), &tg.Message{MessageID: 55, Chat: tg.Chat{ID: tc.chatID}, From: tg.User{ID: tc.from},
				MessageThreadID: &tid, Document: &tg.Document{FileID: "f", FileName: tc.file, FileSize: 400}})
			gotDelete := len(f.deleted) == 1 && f.deleted[0].msgID == 55
			if gotDelete != tc.wantDelete {
				t.Fatalf("удаление: %+v", f.deleted)
			}
			if !tc.wantDelete {
				if len(f.sentMsgs)+len(f.rkSends) != 0 {
					t.Fatalf("лишний ответ: %q %+v", f.sentMsgs, f.rkSends)
				}
				return
			}
			var text string
			if tc.wantButton {
				if len(f.rkSends) != 1 {
					t.Fatalf("ждали ответ с кнопкой: %+v %q", f.rkSends, f.sentMsgs)
				}
				kb, ok := f.rkSends[0].markup.(*tg.InlineKeyboardMarkup)
				if !ok || kb.InlineKeyboard[0][0].WebApp == nil || kb.InlineKeyboard[0][0].WebApp.URL != "https://wgmon.example.com/miniapp/" {
					t.Fatalf("кнопка: %+v", f.rkSends[0].markup)
				}
				text = f.rkSends[0].text
			} else {
				if len(f.sentMsgs) != 1 || len(f.rkSends) != 0 {
					t.Fatalf("в группе -- текст без кнопки: %+v %q", f.rkSends, f.sentMsgs)
				}
				text = f.sentMsgs[0]
			}
			if !strings.Contains(text, "в приложении") || !strings.Contains(text, "Файл удалён") {
				t.Fatalf("ответ: %q", text)
			}
		})
	}
}

func TestConfDocumentDeleteFails(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{deleteErr: errors.New("message can't be deleted")}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	r.HandleMessage(context.Background(), &tg.Message{MessageID: 56, Chat: tg.Chat{ID: 777}, From: tg.User{ID: 777},
		Document: &tg.Document{FileID: "f", FileName: "home.conf"}})
	if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], "Удалите") || len(f.rkSends) != 0 {
		t.Fatalf("без адреса приложения и без удаления: %q %+v", f.sentMsgs, f.rkSends)
	}
}

// /tunnels, /routes и «explain» ушли: ни админу, ни оператору бот на них
// больше ничего не отвечает. Кнопки старой нижней клавиатуры отвечают
// подсказкой -- TestOldReplyKeyboardTunnelsRoutesAnswerMovedToApp.
func TestRemovedTunnelsRoutesCommandsAreSilent(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 12345)
	tid := int64(55)
	for _, from := range []int64{12345, 200} {
		for _, text := range []string{"/tunnels", "/routes", "explain example.com", "vpn-new"} {
			f := &fakeRouterTG{}
			sink := &fakeEnqueuer{}
			r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345})
			r.HandleMessage(context.Background(), &tg.Message{MessageID: 99, Chat: tg.Chat{ID: -100}, From: tg.User{ID: from}, MessageThreadID: &tid, Text: text})
			if len(f.sentMsgs) != 0 || len(f.sentMarkups) != 0 || len(f.edits) != 0 || len(sink.calls) != 0 {
				t.Errorf("from=%d %q: бот ответил (msgs=%v markups=%d calls=%+v)", from, text, f.sentMsgs, len(f.sentMarkups), sink.calls)
			}
		}
	}
}

// /status живёт без кэша маршрутов: туннели -- из событий за три минуты,
// кнопок удалённых панелей в ответе нет.
func TestStatusWorksWithoutRoutesCache(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 11); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_ = d.Events().Insert(uid, "tunnel_awg11", "ok", `{"tunnel_name":"amnezia","interface":"nwg0","handshake_age_sec":250,"ping_check_status":"dead","ping_check_fail_count":2,"ping_check_fail_threshold":5}`, now)
	_ = d.Events().Insert(uid, "tunnel_gone", "ok", `{"tunnel_name":"gone"}`, now.Add(-10*time.Minute))
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345})
	tid := int64(11)
	r.HandleMessage(context.Background(), &tg.Message{MessageID: 42, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345}, MessageThreadID: &tid, Text: "/status"})
	if len(f.rkSends) != 1 {
		t.Fatalf("ждали один ответ, got %d", len(f.rkSends))
	}
	body := f.rkSends[0].text
	if !strings.Contains(body, "amnezia") || strings.Contains(body, "gone") {
		t.Fatalf("ответ:\n%s", body)
	}
	if kb, ok := f.rkSends[0].markup.(*tg.InlineKeyboardMarkup); ok {
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if isMovedToAppCallback(b.CallbackData) || b.WebApp != nil {
					t.Fatalf("кнопка удалённой панели или приложения в группе: %+v", b)
				}
			}
		}
	}
}

func TestParse_RemovedTunnelsRoutesCallbacksAreUnknown(t *testing.T) {
	for _, data := range []string{
		"restart_tunnel:42:_panel_", "tunnel_restart:42:tunnel_awg13:Wireguard3", "tunnel_enable:42:tunnel_awg13:Wireguard3",
		"tunnel_delete:42:_panel_::awg13", "tunnels_refresh:42:_panel_", "tunnel_import_add:42:_panel_:a1b2c3d4",
		"routes_open:42:_panel_", "routes_close:0:_panel_", "routes_confirm:42:a:b:a1b2c3d4",
		"maint_restart:42:hrneo", "maint_confirm:42:hrneo:a1b2c3d4",
	} {
		if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "unknown action") {
			t.Errorf("%s: err=%v", data, err)
		}
	}
	for _, data := range []string{"panel:0:help:tunnels", "panel:0:help:routes"} {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s разбирается, а экрана справки больше нет", data)
		}
	}
	if _, err := Parse("close_panel:0:_panel_"); err != nil {
		t.Fatalf("close_panel нужен справке: %v", err)
	}
}

// Ревью цикла 4: у людей осталась старая нижняя клавиатура с «🎛 Туннели» и
// «🛣 Маршруты». Нажатие -- короткий ответ «Это теперь в приложении»: в личке с
// кнопкой приложения, в разрешённой группе -- текстом, в чужой -- молчание.
// Роутеру ничего не уходит.
func TestOldReplyKeyboardTunnelsRoutesAnswerMovedToApp(t *testing.T) {
	d, _ := newTestDB(t)
	cases := []struct {
		name         string
		chatID, from int64
		text         string
		want         string // "button" | "text" | ""
	}{
		{"туннели в группе", -100, 200, "🎛 Туннели", "text"},
		{"маршруты в группе", -100, 12345, "🛣 Маршруты", "text"},
		{"без эмодзи", -100, 200, " маршруты ", "text"},
		{"в личке", 777, 777, "Туннели", "button"},
		{"чужая группа", -555, 200, "🎛 Туннели", ""},
		{"обычная переписка", -100, 200, "туннели опять лежат", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRouterTGFull{}
			sink := &fakeEnqueuer{}
			r := NewRouterWithSink(d, f, sink, Config{ChatID: -100, AdminUserID: 12345, PublicBaseURL: "https://wgmon.example.com"})
			tid := int64(55)
			r.HandleMessage(context.Background(), &tg.Message{MessageID: 9, Chat: tg.Chat{ID: tc.chatID}, From: tg.User{ID: tc.from}, MessageThreadID: &tid, Text: tc.text})
			if len(sink.calls) != 0 {
				t.Fatalf("роутеру ушло: %+v", sink.calls)
			}
			switch tc.want {
			case "":
				if len(f.sentMsgs)+len(f.rkSends) != 0 {
					t.Fatalf("лишний ответ: %q %+v", f.sentMsgs, f.rkSends)
				}
			case "text":
				if len(f.sentMsgs) != 1 || f.sentMsgs[0] != movedToAppToast || len(f.rkSends) != 0 {
					t.Fatalf("ответ в группе: %q %+v", f.sentMsgs, f.rkSends)
				}
			case "button":
				if len(f.rkSends) != 1 || f.rkSends[0].text != movedToAppToast {
					t.Fatalf("ответ в личке: %q %+v", f.sentMsgs, f.rkSends)
				}
				kb, ok := f.rkSends[0].markup.(*tg.InlineKeyboardMarkup)
				if !ok || kb.InlineKeyboard[0][0].WebApp == nil || kb.InlineKeyboard[0][0].WebApp.URL != "https://wgmon.example.com/miniapp/" {
					t.Fatalf("кнопка: %+v", f.rkSends[0].markup)
				}
			}
		})
	}
}
