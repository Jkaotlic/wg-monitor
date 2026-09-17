package callbacks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

const testAppBase = "https://wgmon.example.com"

func dm(from int64, text string) *tg.Message {
	return &tg.Message{MessageID: 10, Chat: tg.Chat{ID: from}, From: tg.User{ID: from}, Text: text}
}

func startReply(t *testing.T, f *fakeRouterTG) (text, mode string, kb *tg.InlineKeyboardMarkup) {
	t.Helper()
	switch {
	case len(f.rkSends) == 1 && len(f.sentMsgs) == 0:
		k, _ := f.rkSends[0].markup.(*tg.InlineKeyboardMarkup)
		return f.rkSends[0].text, f.rkSends[0].mode, k
	case len(f.sentMsgs) == 1 && len(f.rkSends) == 0:
		return f.sentMsgs[0], f.sentModes[0], nil
	}
	t.Fatalf("ждали один ответ на /start: sent=%q rk=%+v", f.sentMsgs, f.rkSends)
	return "", "", nil
}

// /start владельцу: карточка и кнопка web_app, номера нет -- доступ есть.
func TestStart_WithAccessShowsCardAndAppButton(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().SetTelegramUserID(uid, 100); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	NewRouter(d, f, Config{AdminUserID: 42, PublicBaseURL: testAppBase}).HandleMessage(context.Background(), dm(100, "/start"))
	text, mode, kb := startReply(t, f)
	if !strings.Contains(text, "wg-monitor — состояние ваших роутеров и починка в приложении") || strings.Contains(text, "Telegram ID") {
		t.Fatalf("текст: %q", text)
	}
	if mode != "HTML" {
		t.Fatalf("parse mode %q", mode)
	}
	if kb == nil || len(kb.InlineKeyboard) != 1 || kb.InlineKeyboard[0][0].Text != "Открыть приложение" ||
		kb.InlineKeyboard[0][0].WebApp == nil || kb.InlineKeyboard[0][0].WebApp.URL != testAppBase+"/miniapp/" {
		t.Fatalf("кнопка: %+v", kb)
	}
}

// Оператор и админ -- тоже «доступ есть».
func TestStart_OperatorAndAdminSeeNoID(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.RouterOperators().Add(uid, 200, 42); err != nil {
		t.Fatal(err)
	}
	for _, from := range []int64{200, 42} {
		f := &fakeRouterTG{}
		NewRouter(d, f, Config{AdminUserID: 42, PublicBaseURL: testAppBase}).HandleMessage(context.Background(), dm(from, "/start"))
		if text, _, _ := startReply(t, f); strings.Contains(text, "Telegram ID") {
			t.Fatalf("from=%d: номер при доступе: %q", from, text)
		}
	}
}

// Без доступа -- номер человека кодом и кому его передать.
func TestStart_NoAccessShowsTelegramID(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	NewRouter(d, f, Config{AdminUserID: 42, PublicBaseURL: testAppBase}).HandleMessage(context.Background(), dm(777001, "/start"))
	text, _, kb := startReply(t, f)
	if !strings.Contains(text, "Ваш Telegram ID: <code>777001</code> — передайте его администратору, чтобы он выдал доступ.") {
		t.Fatalf("текст: %q", text)
	}
	if kb == nil {
		t.Fatal("кнопка приложения пропала")
	}
}

// Без https-адреса -- тот же ответ текстом, без кнопки.
func TestStart_WithoutHTTPSNoButton(t *testing.T) {
	d, _ := newTestDB(t)
	for _, base := range []string{"", "http://wgmon.example.com"} {
		f := &fakeRouterTG{}
		NewRouter(d, f, Config{AdminUserID: 42, PublicBaseURL: base}).HandleMessage(context.Background(), dm(777001, "/start"))
		text, _, kb := startReply(t, f)
		if kb != nil || !strings.Contains(text, "Адрес приложения ещё не настроен") || !strings.Contains(text, "<code>777001</code>") {
			t.Fatalf("base=%q: text=%q kb=%+v", base, text, kb)
		}
	}
}

// /start с параметром и с именем бота -- тот же ответ.
func TestStart_PayloadAndBotSuffix(t *testing.T) {
	d, _ := newTestDB(t)
	for _, cmd := range []string{"/start router-7", "/start@wgmon_bot", "  /start  "} {
		f := &fakeRouterTG{}
		NewRouter(d, f, Config{AdminUserID: 42, PublicBaseURL: testAppBase}).HandleMessage(context.Background(), dm(777001, cmd))
		if text, _, _ := startReply(t, f); !strings.Contains(text, "wg-monitor") {
			t.Fatalf("%q: %q", cmd, text)
		}
	}
}

// В группе бот ничего не делает: ни /start, ни команды, ни секреты, ни .conf.
func TestGroupMessagesAreSilent(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().SetTelegramUserID(uid, 100)
	tid := int64(55)
	for _, m := range []*tg.Message{
		{MessageID: 9, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 42}, Text: "/start"},
		{MessageID: 9, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 42}, MessageThreadID: &tid, Text: "/status"},
		{MessageID: 9, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 100}, MessageThreadID: &tid, Text: "📊 Что происходит?"},
		{MessageID: 9, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 42}, Text: "vpn://SECRET-KEY-MUST-NOT-LEAK"},
		{MessageID: 9, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 42}, Document: &tg.Document{FileID: "f", FileName: "home.conf"}},
	} {
		f := &fakeRouterTG{}
		NewRouter(d, f, Config{AdminUserID: 42, PublicBaseURL: testAppBase}).HandleMessage(context.Background(), m)
		if !f.silent() {
			t.Errorf("%q в группе: бот ответил sent=%q rk=%+v deleted=%+v", m.Text, f.sentMsgs, f.rkSends, f.deleted)
		}
	}
}

// В личке на всё, кроме /start и секретов, бот молчит -- и админу, и владельцу.
func TestPrivateMessagesOtherThanStartAreSilent(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().SetTelegramUserID(uid, 100)
	for _, from := range []int64{42, 100, 777001} {
		for _, text := range []string{
			"/status", "/check", "/via", "/direct", "/help", "/menu", "/keyboard", "/myid", "/panel",
			"/ensure_topics", "/this_is vasya", "/tunnels", "📊 Что происходит?", "🎛 Туннели", "привет", "",
		} {
			f := &fakeRouterTG{}
			NewRouter(d, f, Config{AdminUserID: 42, PublicBaseURL: testAppBase}).HandleMessage(context.Background(), dm(from, text))
			if !f.silent() {
				t.Errorf("from=%d %q: бот ответил sent=%q rk=%+v", from, text, f.sentMsgs, f.rkSends)
			}
		}
	}
}

func silenceQuery(from, chat, uid int64) *tg.CallbackQuery {
	return &tg.CallbackQuery{
		ID: "cb-s", From: tg.User{ID: from}, Data: fmt.Sprintf("silence:%d:awg_handshake:1h", uid),
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: chat}, Text: "🔴 тревога"},
	}
}

// Решение 3: право «Тише на час» -- по роли на роутере, а не по привязке
// владельца. Оператор роутера без владельца глушит тревогу из своей лички.
func TestSilence_OperatorOfOwnerlessRouter(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.RouterOperators().Add(uid, 200, 42); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	NewRouter(d, f, Config{AdminUserID: 42}).HandleCallback(context.Background(), silenceQuery(200, 200, uid))
	st, err := d.State().Get(uid, "awg_handshake")
	if err != nil {
		t.Fatal(err)
	}
	if st.SilencedUntil == nil {
		t.Fatalf("оператор не заглушил: answers=%q", f.answers)
	}
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "Уведомления скрыты") {
		t.Fatalf("правка: %q", f.edits)
	}
}

func TestSilence_OwnerAndAdminAllowed(t *testing.T) {
	for _, from := range []int64{100, 42} {
		d, uid := newTestDB(t)
		_ = d.Users().SetTelegramUserID(uid, 100)
		f := &fakeRouterTG{}
		NewRouter(d, f, Config{AdminUserID: 42}).HandleCallback(context.Background(), silenceQuery(from, from, uid))
		st, _ := d.State().Get(uid, "awg_handshake")
		if st.SilencedUntil == nil {
			t.Fatalf("from=%d: не заглушено, answers=%q", from, f.answers)
		}
		until := time.Until(*st.SilencedUntil)
		if until < 50*time.Minute || until > 70*time.Minute {
			t.Fatalf("from=%d: срок %v, ждали час", from, until)
		}
	}
}

func TestSilence_StrangerAndUnknownRouterRejected(t *testing.T) {
	d, uid := newTestDB(t)
	_ = d.Users().SetTelegramUserID(uid, 100)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{AdminUserID: 42})
	r.HandleCallback(context.Background(), silenceQuery(777001, 777001, uid))
	r.HandleCallback(context.Background(), silenceQuery(42, 42, 99999))
	if st, _ := d.State().Get(uid, "awg_handshake"); st.SilencedUntil != nil {
		t.Fatal("посторонний заглушил тревогу")
	}
	if len(f.answers) != 2 || f.answers[0] != "это не твой роутер" || f.answers[1] != "Ошибка: роутер не найден" || len(f.edits) != 0 {
		t.Fatalf("answers=%q edits=%q", f.answers, f.edits)
	}
}

// Старые кнопки из уже отправленных сообщений -- тост, ни правок, ни записей.
func TestOldBotButtonsAnswerMovedToApp(t *testing.T) {
	d, uid := newTestDB(t)
	for _, data := range []string{
		fmt.Sprintf("ack:%d:awg_handshake", uid),
		fmt.Sprintf("mute:%d:awg_handshake", uid),
		fmt.Sprintf("history:%d:awg_handshake", uid),
		fmt.Sprintf("diag_now:%d:_menu", uid),
		fmt.Sprintf("diag_raw:%d:_panel_:a1b2c3d4", uid),
		fmt.Sprintf("diag_test:%d:a1b2c3d4:mtu", uid),
		fmt.Sprintf("diag_back:%d:_panel_:a1b2c3d4", uid),
		fmt.Sprintf("pingcheck_now:%d:_menu", uid),
		fmt.Sprintf("pingcheck_open:%d:_panel_", uid),
		fmt.Sprintf("pingcheck_toggle:%d:awg10:Wireguard0:1", uid),
		fmt.Sprintf("force_recheck:%d:agent_heartbeat", uid),
		fmt.Sprintf("router_doctor:%d:_menu", uid),
		fmt.Sprintf("check_via_tunnel:%d:_menu", uid),
		fmt.Sprintf("check_direct:%d:_menu", uid),
		fmt.Sprintf("close_panel:%d:_panel_", uid),
		"compat_btn:0:smart_reply",
		"panel:0:help:pingcheck", "panel:0:help:diag", "panel:0:help:tunnels",
		fmt.Sprintf("tunnels_refresh:%d:_panel_", uid),
		fmt.Sprintf("maint_confirm:%d:hrneo_stop:a1b2c3d4", uid),
	} {
		for _, chat := range []int64{42, -100} {
			f := &fakeRouterTG{}
			NewRouter(d, f, Config{AdminUserID: 42}).HandleCallback(context.Background(), &tg.CallbackQuery{
				ID: "cb", From: tg.User{ID: 42}, Data: data, Message: tg.Message{Chat: tg.Chat{ID: chat}, MessageID: 7},
			})
			if len(f.answers) != 1 || f.answers[0] != movedToAppToast || len(f.edits)+len(f.sentMsgs)+len(f.rkSends) != 0 {
				t.Errorf("%s (chat %d): answers=%q edits=%q", data, chat, f.answers, f.edits)
			}
		}
	}
	if st, _ := d.State().Get(uid, "awg_handshake"); st.Acked || st.AckedUntil != nil {
		t.Fatalf("старая кнопка изменила состояние: %+v", st)
	}
}

func TestParse_OnlySilence(t *testing.T) {
	a, err := Parse("silence:7:awg_handshake:4h")
	if err != nil || a.Action != "silence" || a.UserID != 7 || a.CheckName != "awg_handshake" || a.TTL != 4*time.Hour {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	for _, data := range []string{
		"silence:7:awg_handshake", "silence:7:awg_handshake:2h", "silence:x:awg_handshake:1h", "silence:0:awg_handshake:1h",
		"silence:7::1h", "ack:7:awg_handshake", "diag_now:7:_menu", "panel:0:home", "nmute:7", "",
	} {
		if _, err := Parse(data); err == nil {
			t.Errorf("%q разобрано", data)
		}
	}
}
