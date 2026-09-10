package callbacks

import (
	"context"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func TestHelp_AdminGetsFullBody(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 42}, Text: "/help"}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 help reply, got %d", len(f.sentMsgs))
	}
	body := f.sentMsgs[0]
	for _, want := range []string{"Алерты", "Кнопки в топике", "Админ-команды", "/panel", "Amnezia Premium", "HideMy.name", ".conf"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin /help missing %q in body:\n%s", want, body)
		}
	}
}

func TestHelp_OperatorGetsOperatorBody(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	tid := int64(55)
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 200}, MessageThreadID: &tid, Text: "/help"}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 help reply, got %d", len(f.sentMsgs))
	}
	body := f.sentMsgs[0]
	if strings.Contains(body, "/panel —") || strings.Contains(body, "Админ-команды") {
		t.Errorf("operator help must NOT include admin section:\n%s", body)
	}
	for _, want := range []string{"Кнопки в топике", "очередь", "/menu", "/amnezia", "/hidemy", "Amnezia Premium", "HideMy.name"} {
		if !strings.Contains(body, want) {
			t.Errorf("operator help missing %q:\n%s", want, body)
		}
	}
}

func TestHelp_StrangerDoesNotSeeAdminBody(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 999}, Text: "/help"}
	r.HandleMessage(context.Background(), msg)

	for _, m := range f.sentMsgs {
		if strings.Contains(m, "Админ-команды") {
			t.Errorf("stranger must not see admin body, got: %s", m)
		}
	}
}

func TestTopicHelpBody_PerRouterMatchesVisibleReplyKeyboard(t *testing.T) {
	body := topicHelpBody("per_router")
	if strings.Contains(body, "🆘 Помощь") {
		t.Fatalf("per-router topic help must not mention the removed reply button:\n%s", body)
	}
	for _, want := range []string{
		"📊 Что происходит?",
		"🩺 Проверка",
		"🎛 Туннели",
		"🛣 Маршруты",
		"🔐 Amnezia Premium",
		"🔑 HideMy.name",
		"🌍 Через туннель?",
		"🇷🇺 Напрямую?",
		"🛠 Обслуживание",
		"⬆ Обновить пакеты",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("per-router topic help missing visible button %q:\n%s", want, body)
		}
	}
}

func TestTopicHelpBody_SummaryMatchesVisibleReplyKeyboard(t *testing.T) {
	for _, kind := range []string{"summary", "systemic"} {
		body := topicHelpBody(kind)
		if strings.Contains(body, "🆘 Помощь") {
			t.Fatalf("%s topic help must not mention the removed reply button:\n%s", kind, body)
		}
		for _, want := range []string{"📊 Здоровье флота", "📋 Список юзеров"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s topic help missing visible button %q:\n%s", kind, want, body)
			}
		}
	}
}

// Чтобы дать человеку доступ к роутеру, нужен его числовой номер в Telegram.
// Сам он этот номер нигде не видит, а личные сообщения от посторонних бот до
// сих пор отбрасывал молча — узнать его было негде, и добавить оператора мог
// только тот, кто умеет доставать id окольными путями.
func TestMyID_AnswersStrangerInDM(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})

	const stranger = int64(777001)
	// Личка постороннего: ни доступа к роутерам, ни прав администратора.
	msg := &tg.Message{Chat: tg.Chat{ID: stranger}, From: tg.User{ID: stranger}, Text: "/myid"}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("бот обязан ответить на /myid кому угодно, ответов: %d", len(f.sentMsgs))
	}
	if !strings.Contains(f.sentMsgs[0], "777001") {
		t.Fatalf("в ответе нет самого номера:\n%s", f.sentMsgs[0])
	}
}

// Команда сообщает номер ТОЛЬКО тому, кто её послал: чужой id она не выдаёт
// ни при каких аргументах.
func TestMyID_TellsOnlyOwnNumber(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})

	const stranger = int64(777002)
	msg := &tg.Message{Chat: tg.Chat{ID: stranger}, From: tg.User{ID: stranger}, Text: "/myid 42"}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("ответов: %d", len(f.sentMsgs))
	}
	if strings.Contains(f.sentMsgs[0], "42\n") || strings.Contains(f.sentMsgs[0], " 42 ") {
		t.Fatalf("команда выдала чужой номер:\n%s", f.sentMsgs[0])
	}
	if !strings.Contains(f.sentMsgs[0], "777002") {
		t.Fatalf("в ответе нет номера отправителя:\n%s", f.sentMsgs[0])
	}
}
