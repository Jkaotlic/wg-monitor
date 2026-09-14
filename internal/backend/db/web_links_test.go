package db

import (
	"testing"
	"time"
)

// Гранты на вход в веб-управление. Таблица хранит только sha256 значения,
// поэтому в тестах «хеш» -- любая строка: смысл проверок в сроке, адресате,
// лимите живых и в том, что предъявление не гасит ссылку.

const (
	hashA = "aaaa000000000000000000000000000000000000000000000000000000000000"
	hashB = "bbbb000000000000000000000000000000000000000000000000000000000000"
	hashC = "cccc000000000000000000000000000000000000000000000000000000000000"
	hashD = "dddd000000000000000000000000000000000000000000000000000000000000"
)

func webLinkUseCount(t *testing.T, d *DB, hash string) (int, string) {
	t.Helper()
	var count int
	var remote *string
	err := d.SQL().QueryRow(
		`SELECT use_count, last_used_remote FROM web_links WHERE token_hash = ?`, hash).Scan(&count, &remote)
	if err != nil {
		t.Fatalf("read web_links row %s: %v", hash[:8], err)
	}
	if remote == nil {
		return count, ""
	}
	return count, *remote
}

func TestWebLinksIssuedGrantIsRedeemableWithinTTL(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if err := d.WebLinks().Issue(hashA, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue: %v", err)
	}

	rows, err := d.WebLinks().Redeem(hashA, now.Add(time.Minute), "198.51.100.7")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if rows != 1 {
		t.Fatalf("redeem rows = %d, want 1", rows)
	}
	count, remote := webLinkUseCount(t, d, hashA)
	if count != 1 {
		t.Errorf("use_count = %d, want 1", count)
	}
	if remote != "198.51.100.7" {
		t.Errorf("last_used_remote = %q, want адрес предъявителя", remote)
	}
}

// Многоразовость -- решение оператора: одноразовость снята сознательно, и её
// роль взяли на себя журнал обмена, лимит живых и точечный отзыв. Значит
// второе и третье предъявление обязаны срабатывать, а счётчик -- расти.
func TestWebLinksRedeemDoesNotBurnTheGrant(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if err := d.WebLinks().Issue(hashA, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue: %v", err)
	}
	for i := 1; i <= 3; i++ {
		rows, err := d.WebLinks().Redeem(hashA, now.Add(time.Duration(i)*time.Minute), "198.51.100.7")
		if err != nil {
			t.Fatalf("redeem %d: %v", i, err)
		}
		if rows != 1 {
			t.Fatalf("redeem %d: rows = %d, want 1 (ссылка многоразовая)", i, rows)
		}
	}
	if count, _ := webLinkUseCount(t, d, hashA); count != 3 {
		t.Errorf("use_count = %d, want 3 -- каждый обмен обязан оставить улику", count)
	}
}

func TestWebLinksExpiredGrantIsDead(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if err := d.WebLinks().Issue(hashA, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue: %v", err)
	}

	rows, err := d.WebLinks().Redeem(hashA, now.Add(12*time.Hour+time.Second), "198.51.100.7")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if rows != 0 {
		t.Fatalf("redeem rows = %d, want 0 -- срок вышел", rows)
	}
	live, err := d.WebLinks().ActiveFor(999, now.Add(12*time.Hour+time.Second))
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("живых грантов = %v, want пусто", live)
	}
}

// ActiveFor с нулевым временем -- «все гранты этого человека, включая
// просроченные». На этом свойстве стоит различение «просрочена» и «такой
// ссылки нет» в журнале обмена.
func TestWebLinksActiveForZeroTimeReturnsExpiredToo(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if err := d.WebLinks().Issue(hashA, 999, now.Add(-time.Hour)); err != nil {
		t.Fatalf("issue: %v", err)
	}
	live, err := d.WebLinks().ActiveFor(999, now)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("живых = %v, want пусто", live)
	}
	all, err := d.WebLinks().ActiveFor(999, time.Time{})
	if err != nil {
		t.Fatalf("active(zero): %v", err)
	}
	if len(all) != 1 || all[0] != hashA {
		t.Fatalf("все гранты = %v, want [%s]", all, hashA)
	}
}

func TestWebLinksActiveForIsScopedToOnePerson(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if err := d.WebLinks().Issue(hashA, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue admin: %v", err)
	}
	if err := d.WebLinks().Issue(hashB, 100, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue owner: %v", err)
	}
	live, err := d.WebLinks().ActiveFor(999, now)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(live) != 1 || live[0] != hashA {
		t.Fatalf("живые гранты админа = %v, want [%s]", live, hashA)
	}
}

// Связку ключей ко всему парку накопить нельзя: живых грантов на человека --
// три, и выдача четвёртого гасит самый старый.
func TestWebLinksFourthIssueEvictsOldest(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	for _, h := range []string{hashA, hashB, hashC, hashD} {
		if err := d.WebLinks().Issue(h, 999, now.Add(12*time.Hour)); err != nil {
			t.Fatalf("issue %s: %v", h[:8], err)
		}
	}
	live, err := d.WebLinks().ActiveFor(999, now)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(live) != WebLinkMaxLive {
		t.Fatalf("живых грантов = %d (%v), want %d", len(live), live, WebLinkMaxLive)
	}
	for _, h := range live {
		if h == hashA {
			t.Fatalf("самый старый грант остался живым: %v", live)
		}
	}
	rows, err := d.WebLinks().Redeem(hashA, now.Add(time.Minute), "198.51.100.7")
	if err != nil {
		t.Fatalf("redeem вытесненного: %v", err)
	}
	if rows != 0 {
		t.Fatalf("вытесненный грант ещё работает: rows = %d", rows)
	}
}

// Вытеснение считает живых, а не все строки: три просроченных гранта не
// должны мешать выдать новый.
func TestWebLinksExpiredGrantsDoNotEatTheLimit(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	for _, h := range []string{hashA, hashB, hashC} {
		if err := d.WebLinks().Issue(h, 999, now.Add(-time.Hour)); err != nil {
			t.Fatalf("issue %s: %v", h[:8], err)
		}
	}
	if err := d.WebLinks().Issue(hashD, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue свежего: %v", err)
	}
	live, err := d.WebLinks().ActiveFor(999, now)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(live) != 1 || live[0] != hashD {
		t.Fatalf("живые = %v, want [%s]", live, hashD)
	}
}

func TestWebLinksDeleteRevokesOneGrant(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if err := d.WebLinks().Issue(hashA, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue a: %v", err)
	}
	if err := d.WebLinks().Issue(hashB, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue b: %v", err)
	}
	if err := d.WebLinks().Delete(hashA); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, err := d.WebLinks().Redeem(hashA, now, "198.51.100.7")
	if err != nil {
		t.Fatalf("redeem удалённого: %v", err)
	}
	if rows != 0 {
		t.Fatalf("удалённый грант ещё работает: rows = %d", rows)
	}
	live, err := d.WebLinks().ActiveFor(999, now)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(live) != 1 || live[0] != hashB {
		t.Fatalf("живые = %v, want [%s] -- соседний грант трогать нельзя", live, hashB)
	}
	// Удаление несуществующего -- не ошибка: отзыв идемпотентен.
	if err := d.WebLinks().Delete(hashC); err != nil {
		t.Fatalf("delete несуществующего: %v", err)
	}
}

func TestWebLinksDeleteForUserRevokesAllHis(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if err := d.WebLinks().Issue(hashA, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue a: %v", err)
	}
	if err := d.WebLinks().Issue(hashB, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue b: %v", err)
	}
	if err := d.WebLinks().Issue(hashC, 100, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue чужой: %v", err)
	}
	if err := d.WebLinks().DeleteForUser(999); err != nil {
		t.Fatalf("delete for user: %v", err)
	}
	live, err := d.WebLinks().ActiveFor(999, now)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("живые = %v, want пусто", live)
	}
	other, err := d.WebLinks().ActiveFor(100, now)
	if err != nil {
		t.Fatalf("active чужого: %v", err)
	}
	if len(other) != 1 {
		t.Fatalf("чужие гранты = %v, want один", other)
	}
}

// Чистка -- по сроку, а не по факту использования: строка живёт сутки после
// того, как перестала работать, чтобы журнал обмена было с чем сверить.
func TestWebLinksPruneBeforeDropsOnlyStale(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if err := d.WebLinks().Issue(hashA, 999, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("issue старого: %v", err)
	}
	if err := d.WebLinks().Issue(hashB, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("issue живого: %v", err)
	}
	deleted, err := d.WebLinks().PruneBefore(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("удалено = %d, want 1", deleted)
	}
	live, err := d.WebLinks().ActiveFor(999, now)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(live) != 1 || live[0] != hashB {
		t.Fatalf("живые = %v, want [%s]", live, hashB)
	}
}
