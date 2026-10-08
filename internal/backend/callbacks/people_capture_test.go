package callbacks

import (
	"context"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func personByID(t *testing.T, d *db.DB, id int64) (db.Person, bool) {
	t.Helper()
	list, err := d.People().List()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.TelegramUserID == id {
			return p, true
		}
	}
	return db.Person{}, false
}

// /start от человека без доступа -- он появляется в справочнике по имени.
func TestPeopleCapture_StartFromStranger(t *testing.T) {
	d, _ := newTestDB(t)
	m := dm(777002, "/start")
	m.From = tg.User{ID: 777002, FirstName: "Вымысел", LastName: "Тестовый", Username: "fiction_user"}
	NewRouter(d, &fakeRouterTG{}, Config{AdminUserID: 42, PublicBaseURL: testAppBase}).HandleMessage(context.Background(), m)
	p, ok := personByID(t, d, 777002)
	if !ok {
		t.Fatal("человек не записан")
	}
	if p.FirstName != "Вымысел" || p.LastName != "Тестовый" || p.Username != "fiction_user" || p.Source != db.PersonSourceBot || p.LastSeenAt == nil {
		t.Fatalf("строка = %+v", p)
	}
}

// Любое личное сообщение, не только /start.
func TestPeopleCapture_AnyPrivateMessage(t *testing.T) {
	d, _ := newTestDB(t)
	m := dm(777003, "привет")
	m.From.FirstName = "Просто"
	NewRouter(d, &fakeRouterTG{}, Config{AdminUserID: 42}).HandleMessage(context.Background(), m)
	if p, ok := personByID(t, d, 777003); !ok || p.FirstName != "Просто" {
		t.Fatalf("строка = %+v ok=%v", p, ok)
	}
}

// В группе люди не записываются: это не обращение к боту.
func TestPeopleCapture_GroupMessageIgnored(t *testing.T) {
	d, _ := newTestDB(t)
	m := &tg.Message{MessageID: 1, Chat: tg.Chat{ID: -100500}, From: tg.User{ID: 777004, FirstName: "Группа"}, Text: "/start"}
	NewRouter(d, &fakeRouterTG{}, Config{AdminUserID: 42}).HandleMessage(context.Background(), m)
	if _, ok := personByID(t, d, 777004); ok {
		t.Fatal("сообщение из группы записало человека")
	}
}

// Нажатие кнопки в личке -- тоже «человек показался».
func TestPeopleCapture_PrivateCallback(t *testing.T) {
	d, _ := newTestDB(t)
	q := &tg.CallbackQuery{ID: "cb", From: tg.User{ID: 777005, FirstName: "Кнопка", Username: "button_user"},
		Message: tg.Message{MessageID: 3, Chat: tg.Chat{ID: 777005}}, Data: "мусор"}
	NewRouter(d, &fakeRouterTG{}, Config{AdminUserID: 42}).HandleCallback(context.Background(), q)
	if p, ok := personByID(t, d, 777005); !ok || p.Username != "button_user" {
		t.Fatalf("строка = %+v ok=%v", p, ok)
	}
}
