package notify

import (
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func newDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("router-a", "tok-a", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	return d, id
}

func TestRecipientsFor_OwnerAndOperators(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(router, 1002, 1001); err != nil {
		t.Fatal(err)
	}

	got, err := RecipientsFor(d, router, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 1001 {
		t.Fatalf("получатели=%v, ждали владельца 1001 первым и оператора 1002", got)
	}
}

func TestRecipientsFor_SkipsMuted(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(router, 1002, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.NotifyMutes().SetMuted(1002, router, true); err != nil {
		t.Fatal(err)
	}

	got, err := RecipientsFor(d, router, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 1001 {
		t.Fatalf("получатели=%v, заглушивший 1002 не должен попасть в список", got)
	}
}

// Владелец не привязан и операторов нет -- слать некому. Не ошибка, а
// состояние, которое обязано быть видимым в сводке дашборда.
func TestRecipientsFor_NobodyToNotify(t *testing.T) {
	d, router := newDB(t)

	got, err := RecipientsFor(d, router, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("получатели=%v, ждали пусто", got)
	}
}

// Один и тот же человек может быть и владельцем, и оператором. Уведомление
// приходит один раз, а не двумя одинаковыми сообщениями подряд.
func TestRecipientsFor_NoDuplicates(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(router, 1001, 1001); err != nil {
		t.Fatal(err)
	}

	got, err := RecipientsFor(d, router, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("получатели=%v, ждали одного", got)
	}
}

// adminTG -- номер админа в тестах. Не совпадает ни с одним владельцем и
// оператором, кроме тех случаев, где совпадение и есть предмет теста.
const adminTG int64 = 9000

// Решение оператора 15.09: админ получает уведомления по всем роутерам, в том
// числе по чужим, где он не владелец и не оператор.
func TestRecipientsFor_AdminGetsForeignRouter(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(router, 1002, 1001); err != nil {
		t.Fatal(err)
	}

	got, err := RecipientsFor(d, router, adminTG)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{1001, 1002, adminTG}
	if len(got) != len(want) {
		t.Fatalf("получатели=%v, ждали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("получатели=%v, ждали %v: админ идёт последним", got, want)
		}
	}
}

// Админ сам владелец роутера -- одно сообщение, а не два одинаковых подряд.
func TestRecipientsFor_AdminOwnerGetsOneMessage(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, adminTG); err != nil {
		t.Fatal(err)
	}

	got, err := RecipientsFor(d, router, adminTG)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != adminTG {
		t.Fatalf("получатели=%v, ждали одного админа", got)
	}
}

// Выключил роутер -- не получает ничего про него. Это вторая половина
// решения оператора: «отключить уведомления в личку ... от определённого
// роутера».
func TestRecipientsFor_MutedAdminGetsNothing(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.NotifyMutes().SetMuted(adminTG, router, true); err != nil {
		t.Fatal(err)
	}

	got, err := RecipientsFor(d, router, adminTG)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 1001 {
		t.Fatalf("получатели=%v, выключивший админ не должен попасть в список", got)
	}
}

// Админ не настроен -- поведение ровно прежнее: пустой роутер остаётся пустым.
func TestRecipientsFor_ZeroAdminIsOldBehaviour(t *testing.T) {
	d, router := newDB(t)

	got, err := RecipientsFor(d, router, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("получатели=%v, при adminID=0 ждали пусто", got)
	}
}

// Роутер без владельца и операторов всё равно слышен админу.
func TestRecipientsFor_OrphanRouterReachesAdmin(t *testing.T) {
	d, router := newDB(t)

	got, err := RecipientsFor(d, router, adminTG)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != adminTG {
		t.Fatalf("получатели=%v, ждали админа", got)
	}
}

// Сводка «роутеры без получателей» считает людей роутера, а не админа: иначе
// при настроенном админе список пуст всегда. Выключатель при этом действует:
// владелец, который выключил уведомления, их не услышит -- роутер без адресата.
func TestOwnerAndOperators_NeverAdminButHonoursMute(t *testing.T) {
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.NotifyMutes().SetMuted(1001, router, true); err != nil {
		t.Fatal(err)
	}

	got, err := OwnerAndOperators(d, router)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("получатели=%v, ждали пусто: админа здесь нет, владелец выключил", got)
	}
}
