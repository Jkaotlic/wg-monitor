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

	got, err := RecipientsFor(d, router)
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

	got, err := RecipientsFor(d, router)
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

	got, err := RecipientsFor(d, router)
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

	got, err := RecipientsFor(d, router)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("получатели=%v, ждали одного", got)
	}
}
