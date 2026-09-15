package callbacks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func muteQuery(from, chat int64, data string) *tg.CallbackQuery {
	return &tg.CallbackQuery{
		ID:      "cb-nmute",
		From:    tg.User{ID: from},
		Data:    data,
		Message: tg.Message{Chat: tg.Chat{ID: chat}, MessageID: 5},
	}
}

// Админ жмёт кнопку под уведомлением в своей личке -- роутер выключен для
// него, ответ называет роутер и говорит, где вернуть. Повторное нажатие --
// тот же ответ, без ошибки.
func TestAdminMuteCallback_AdminMutesRouterIdempotently(t *testing.T) {
	d, uid := newTestDB(t) // роутер vasya
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	want := "Больше не пишу про «vasya». Вернуть — в приложении, «Парк»."

	for press := 1; press <= 2; press++ {
		r.HandleCallback(context.Background(), muteQuery(42, 42, fmt.Sprintf("nmute:%d", uid)))
		muted, err := d.NotifyMutes().IsMuted(42, uid)
		if err != nil {
			t.Fatal(err)
		}
		if !muted {
			t.Fatalf("нажатие %d: роутер не выключен для админа", press)
		}
		if len(f.answers) != press || f.answers[press-1] != want {
			t.Fatalf("нажатие %d: ответы %q, ждали %q", press, f.answers, want)
		}
	}
	if len(f.edits) != 0 {
		t.Fatalf("кнопка не должна переписывать сообщение: %v", f.edits)
	}
}

// Не-админ (здесь -- владелец этого же роутера) кнопку не проходит: ни своей,
// ни админской строки выключения не появляется.
func TestAdminMuteCallback_RejectsNonAdmin(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().SetTelegramUserID(uid, 100); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})

	r.HandleCallback(context.Background(), muteQuery(100, 100, fmt.Sprintf("nmute:%d", uid)))

	for _, who := range []int64{100, 42} {
		if muted, _ := d.NotifyMutes().IsMuted(who, uid); muted {
			t.Fatalf("не-админ выключил уведомления для %d", who)
		}
	}
	if len(f.answers) != 1 || f.answers[0] != adminMuteAdminOnly {
		t.Fatalf("ответы %q, ждали %q", f.answers, adminMuteAdminOnly)
	}
}

// Админ не настроен -- кнопку не проходит никто. isAdminTG тут не годится: он
// пускает всех при AdminUserID == 0.
func TestAdminMuteCallback_NoAdminConfiguredRejectsEveryone(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100})

	r.HandleCallback(context.Background(), muteQuery(42, 42, fmt.Sprintf("nmute:%d", uid)))

	if muted, _ := d.NotifyMutes().IsMuted(42, uid); muted {
		t.Fatal("при ненастроенном админе кнопка сработала")
	}
	if len(f.answers) != 1 || f.answers[0] != adminMuteAdminOnly {
		t.Fatalf("ответы %q, ждали %q", f.answers, adminMuteAdminOnly)
	}
}

// Роутер удалили, а кнопка осталась в старом сообщении.
func TestAdminMuteCallback_UnknownRouter(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})

	r.HandleCallback(context.Background(), muteQuery(42, 42, "nmute:99999"))

	if len(f.answers) != 1 || f.answers[0] != adminMuteNoRouter {
		t.Fatalf("ответы %q, ждали %q", f.answers, adminMuteNoRouter)
	}
}

// Испорченные данные -- обычный ответ, как у остальных тостов кнопки: с
// заглавной буквы и точкой (B8).
func TestAdminMuteCallback_MalformedData(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})

	r.HandleCallback(context.Background(), muteQuery(42, 42, "nmute:abc"))

	if len(f.answers) != 1 || f.answers[0] != adminMuteUnknown {
		t.Fatalf("ответы %q, ждали %q", f.answers, adminMuteUnknown)
	}
}

// B8: у Telegram тост callback-ответа ограничен 200 символами
// (tg/client.go:225). Ник роутера попадает в тост как есть -- длинный ник
// обязан быть обрезан так, чтобы весь текст уложился в лимит, а не просто
// уйти как есть и получить отказ Bot API.
func TestAdminMuteCallback_LongNicknameToastFitsTelegramLimit(t *testing.T) {
	d, _ := newTestDB(t)
	longNick := strings.Repeat("оченьдлинноеимяроутера", 20) // далеко за 200 символов
	uid, err := d.Users().Insert(longNick, "rawtoken2", "1.1.1.2", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})

	r.HandleCallback(context.Background(), muteQuery(42, 42, fmt.Sprintf("nmute:%d", uid)))

	if len(f.answers) != 1 {
		t.Fatalf("ответов %d, ждали 1: %q", len(f.answers), f.answers)
	}
	got := f.answers[0]
	if n := utf8.RuneCountInString(got); n > 200 {
		t.Fatalf("тост длиной %d символов превышает лимит Telegram (200): %q", n, got)
	}
	if !strings.HasPrefix(got, "Больше не пишу про «оченьдлинноеимяроутера") {
		t.Fatalf("тост не называет обрезанный ник: %q", got)
	}
}
