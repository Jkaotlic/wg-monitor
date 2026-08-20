package db

import (
	"testing"
	"time"
)

func TestEventsInsertAndLatest(t *testing.T) {
	d := newTestDB(t)
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	now := time.Now().UTC()
	if err := d.Events().Insert(uid, "awg_handshake", "ok", `{"x":1}`, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "awg_handshake", "fail", `{"x":2}`, now); err != nil {
		t.Fatal(err)
	}

	got, err := d.Events().LatestPerUser(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsZero() {
		t.Fatal("got zero timestamp")
	}
	if got.Before(now.Add(-time.Second)) {
		t.Fatalf("got %v want close to %v", got, now)
	}
}

func TestEventsPruneBefore(t *testing.T) {
	d := newTestDB(t)
	tok := "2222222222222222222222222222222222222222222222222222222222222222"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	old := time.Now().Add(-30 * 24 * time.Hour)
	fresh := time.Now()
	d.Events().Insert(uid, "x", "ok", "", old)
	d.Events().Insert(uid, "x", "ok", "", fresh)

	n, err := d.Events().PruneBefore(time.Now().Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
}

func TestListAllSince(t *testing.T) {
	d := newTestDB(t)
	tok := "3333333333333333333333333333333333333333333333333333333333333333"
	uid, _ := d.Users().Insert("home", tok, "1.1.1.1", "awg0")

	now := time.Now().UTC()
	rows := []struct {
		check  string
		status string
		age    time.Duration
	}{
		{"tunnel_awg12", "fail", 30 * time.Minute},
		{"dns", "ok", 20 * time.Minute},
		{"tunnel_awg12", "ok", 10 * time.Minute},
		{"old", "ok", 48 * time.Hour},
	}
	for _, ev := range rows {
		if err := d.Events().Insert(uid, ev.check, ev.status, "{}", now.Add(-ev.age)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := d.Events().ListAllSince(uid, now.Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatalf("ListAllSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (событие старше окна не берётся)", len(got))
	}
	// Свежие первыми: экран читается сверху вниз, как лента.
	if !got[0].TS.After(got[1].TS) || !got[1].TS.After(got[2].TS) {
		t.Errorf("порядок не по убыванию времени: %v", got)
	}
	// Лента идёт по всем проверкам, а не по одной: ListSince этого не умеет.
	if got[0].CheckName != "tunnel_awg12" || got[1].CheckName != "dns" {
		t.Errorf("получили %q, %q; ожидали tunnel_awg12, dns", got[0].CheckName, got[1].CheckName)
	}
}

func TestListAllSinceLimit(t *testing.T) {
	d := newTestDB(t)
	tok := "4444444444444444444444444444444444444444444444444444444444444444"
	uid, _ := d.Users().Insert("home", tok, "1.1.1.1", "awg0")

	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		if err := d.Events().Insert(uid, "dns", "ok", "{}", now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := d.Events().ListAllSince(uid, now.Add(-time.Hour), 3)
	if err != nil {
		t.Fatalf("ListAllSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// Обрезаются самые старые, а не самые свежие.
	if !got[0].TS.After(got[2].TS) {
		t.Errorf("обрезали не с того конца: %v", got)
	}
}

func TestListAllSinceEmptyIsNotError(t *testing.T) {
	d := newTestDB(t)
	tok := "5555555555555555555555555555555555555555555555555555555555555555"
	uid, _ := d.Users().Insert("home", tok, "1.1.1.1", "awg0")

	got, err := d.Events().ListAllSince(uid, time.Now().UTC().Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("ListAllSince: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

// Скорость этой выборки -- не абстракция: на ней открывается список роутеров,
// и прежний запрос (просмотр всех событий роутера за период) держал экран 12
// секунд на живой базе. Тест закрепляет обе гарантии разом: ответ остаётся
// прежним по смыслу и не зависит от того, сколько событий накопилось.
func TestLatestEventsByPrefixSince_OneRowPerCheckRegardlessOfHistory(t *testing.T) {
	d := newTestDB(t)
	uid, err := d.Users().Insert("r", "2222222222222222222222222222222222222222222222222222222222222222", "1.1.1.1", "awg1")
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Add(-2 * time.Hour)
	// История: много старых строк на каждую проверку плюс одна свежая.
	for i := 0; i < 200; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		for _, name := range []string{"dns", "hydraroute", "tunnel_awg10", "tunnels"} {
			if err := d.Events().Insert(uid, name, "ok", "{}", ts); err != nil {
				t.Fatal(err)
			}
		}
	}
	fresh := time.Now().UTC()
	if err := d.Events().Insert(uid, "dns", "fail", `{"note":"свежая"}`, fresh); err != nil {
		t.Fatal(err)
	}

	rows, err := d.Events().LatestEventsByPrefixSince(uid, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("ожидалась одна строка на проверку, получено %d: %+v", len(rows), rows)
	}
	byName := map[string]EventRow{}
	for _, r := range rows {
		byName[r.CheckName] = r
	}
	if byName["dns"].Status != "fail" || byName["dns"].DetailsJSON != `{"note":"свежая"}` {
		t.Fatalf("dns = %+v, ждали самую свежую строку", byName["dns"])
	}

	// Префикс по-прежнему различает tunnel_ и tunnels: подчёркивание в LIKE
	// экранируется.
	tunnels, err := d.Events().LatestEventsByPrefixSince(uid, "tunnel_", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tunnels) != 1 || tunnels[0].CheckName != "tunnel_awg10" {
		t.Fatalf("префикс tunnel_ = %+v", tunnels)
	}

	// Фильтр свежести отсекает проверки, о которых давно не отчитывались.
	recent, err := d.Events().LatestEventsByPrefixSince(uid, "", fresh.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].CheckName != "dns" {
		t.Fatalf("фильтр свежести = %+v", recent)
	}
}
