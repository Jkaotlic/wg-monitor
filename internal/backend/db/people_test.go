package db

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestDBForPeople(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "people.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func peopleByID(t *testing.T, d *DB) map[int64]Person {
	t.Helper()
	list, err := d.People().List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := map[int64]Person{}
	for _, p := range list {
		out[p.TelegramUserID] = p
	}
	return out
}

var peopleT0 = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

func TestPeople_SeenInsertsRow(t *testing.T) {
	d := newTestDBForPeople(t)
	if err := d.People().Seen(4101, "Тест", "Тестов", "test_one", PersonSourceMiniapp, peopleT0); err != nil {
		t.Fatalf("seen: %v", err)
	}
	p, ok := peopleByID(t, d)[4101]
	if !ok {
		t.Fatal("строка не появилась")
	}
	if p.FirstName != "Тест" || p.LastName != "Тестов" || p.Username != "test_one" || p.Source != PersonSourceMiniapp {
		t.Fatalf("поля = %+v", p)
	}
	if p.LastSeenAt == nil || !p.LastSeenAt.Equal(peopleT0) {
		t.Fatalf("last_seen_at = %v, want %v", p.LastSeenAt, peopleT0)
	}
	if !p.FirstSeenAt.Equal(peopleT0) {
		t.Fatalf("first_seen_at = %v, want %v", p.FirstSeenAt, peopleT0)
	}
	if p.NameCheckedAt != nil {
		t.Fatalf("name_checked_at = %v, want nil", p.NameCheckedAt)
	}
}

func TestPeople_SeenIgnoresZeroID(t *testing.T) {
	d := newTestDBForPeople(t)
	if err := d.People().Seen(0, "Никто", "", "", PersonSourceBot, peopleT0); err != nil {
		t.Fatalf("seen: %v", err)
	}
	if n := len(peopleByID(t, d)); n != 0 {
		t.Fatalf("строк %d, want 0", n)
	}
}

func TestPeople_SeenThrottlesLastSeen(t *testing.T) {
	d := newTestDBForPeople(t)
	r := d.People()
	if err := r.Seen(4102, "Пробный", "", "probe", PersonSourceBot, peopleT0); err != nil {
		t.Fatal(err)
	}
	// Через 5 минут с теми же именами -- записи нет, время прежнее.
	if err := r.Seen(4102, "Пробный", "", "probe", PersonSourceBot, peopleT0.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := peopleByID(t, d)[4102].LastSeenAt; got == nil || !got.Equal(peopleT0) {
		t.Fatalf("через 5 мин last_seen_at = %v, want %v (троттлинг)", got, peopleT0)
	}
	// Через 11 минут -- обновилось.
	t11 := peopleT0.Add(11 * time.Minute)
	if err := r.Seen(4102, "Пробный", "", "probe", PersonSourceBot, t11); err != nil {
		t.Fatal(err)
	}
	p := peopleByID(t, d)[4102]
	if p.LastSeenAt == nil || !p.LastSeenAt.Equal(t11) {
		t.Fatalf("через 11 мин last_seen_at = %v, want %v", p.LastSeenAt, t11)
	}
	if !p.FirstSeenAt.Equal(peopleT0) {
		t.Fatalf("first_seen_at сдвинулся: %v", p.FirstSeenAt)
	}
}

func TestPeople_SeenUpdatesNamesInsideThrottle(t *testing.T) {
	d := newTestDBForPeople(t)
	r := d.People()
	if err := r.Seen(4103, "Старое", "", "old_nick", PersonSourceBot, peopleT0); err != nil {
		t.Fatal(err)
	}
	if err := r.Seen(4103, "Новое", "Имя", "new_nick", PersonSourceMiniapp, peopleT0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	p := peopleByID(t, d)[4103]
	if p.FirstName != "Новое" || p.LastName != "Имя" || p.Username != "new_nick" || p.Source != PersonSourceMiniapp {
		t.Fatalf("имена не обновились: %+v", p)
	}
}

func TestPeople_SetNameFromTelegramKeepsLastSeenEmpty(t *testing.T) {
	d := newTestDBForPeople(t)
	if err := d.People().SetNameFromTelegram(4104, "Дотянутый", "Человек", "pulled", peopleT0); err != nil {
		t.Fatal(err)
	}
	p := peopleByID(t, d)[4104]
	if p.FirstName != "Дотянутый" || p.LastName != "Человек" || p.Username != "pulled" || p.Source != PersonSourceTelegram {
		t.Fatalf("поля = %+v", p)
	}
	if p.LastSeenAt != nil {
		t.Fatalf("last_seen_at = %v, want nil: getChat не значит «видели»", p.LastSeenAt)
	}
	if p.NameCheckedAt == nil || !p.NameCheckedAt.Equal(peopleT0) {
		t.Fatalf("name_checked_at = %v", p.NameCheckedAt)
	}
}

func TestPeople_SetNameFromTelegramKeepsExistingLastSeen(t *testing.T) {
	d := newTestDBForPeople(t)
	r := d.People()
	if err := r.Seen(4105, "", "", "", PersonSourceBot, peopleT0); err != nil {
		t.Fatal(err)
	}
	if err := r.SetNameFromTelegram(4105, "Позже", "", "later", peopleT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	p := peopleByID(t, d)[4105]
	if p.FirstName != "Позже" || p.LastSeenAt == nil || !p.LastSeenAt.Equal(peopleT0) {
		t.Fatalf("поля = %+v", p)
	}
}

func TestPeople_MarkNameCheckedDoesNotClobberNames(t *testing.T) {
	d := newTestDBForPeople(t)
	r := d.People()
	if err := r.MarkNameChecked(4106, peopleT0); err != nil {
		t.Fatal(err)
	}
	p := peopleByID(t, d)[4106]
	if p.NameCheckedAt == nil || !p.NameCheckedAt.Equal(peopleT0) || p.FirstName != "" || p.LastSeenAt != nil {
		t.Fatalf("новая строка = %+v", p)
	}
	if err := r.Seen(4107, "Есть", "", "has_name", PersonSourceMiniapp, peopleT0); err != nil {
		t.Fatal(err)
	}
	later := peopleT0.Add(time.Hour)
	if err := r.MarkNameChecked(4107, later); err != nil {
		t.Fatal(err)
	}
	p = peopleByID(t, d)[4107]
	if p.FirstName != "Есть" || p.Username != "has_name" || p.Source != PersonSourceMiniapp {
		t.Fatalf("имя затёрто: %+v", p)
	}
	if p.NameCheckedAt == nil || !p.NameCheckedAt.Equal(later) {
		t.Fatalf("name_checked_at = %v", p.NameCheckedAt)
	}
}

func TestRouterOperators_ListAll(t *testing.T) {
	d, routerA, routerB := newTestDBForOps(t)
	ops := d.RouterOperators()
	if err := ops.Add(routerA, 5001, 9); err != nil {
		t.Fatal(err)
	}
	if err := ops.Add(routerB, 5002, 9); err != nil {
		t.Fatal(err)
	}
	if err := ops.Add(routerB, 5001, 9); err != nil {
		t.Fatal(err)
	}
	all, err := ops.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("строк %d, want 3: %+v", len(all), all)
	}
	seen := map[[2]int64]bool{}
	for _, op := range all {
		seen[[2]int64{op.UserID, op.TelegramUserID}] = true
	}
	for _, want := range [][2]int64{{routerA, 5001}, {routerB, 5002}, {routerB, 5001}} {
		if !seen[want] {
			t.Fatalf("нет пары %v в %+v", want, all)
		}
	}
}
