package db

import (
	"strings"
	"testing"
	"time"
)

// PERF-01: «последний отчёт каждого роутера» зовётся каждые 30 с. GROUP BY по
// events проходит весь индекс (2 млн строк, 1,19 с на живой базе при пуле в
// одно соединение); коррелированный подзапрос по users делает один поиск по
// индексу на роутер (0,0 с). План запроса не должен сканировать events.
func TestLatestPerUserAllUsesIndexSeekPerUser(t *testing.T) {
	d := newTestDB(t)
	rows, err := d.db.Query(`EXPLAIN QUERY PLAN ` + latestPerUserAllQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	for _, p := range plan {
		if strings.HasPrefix(p, "SCAN") && (strings.Contains(p, "events") || strings.HasPrefix(p, "SCAN e ") || p == "SCAN e") {
			t.Fatalf("план сканирует events целиком:\n%s", joined)
		}
	}
	if !strings.Contains(joined, "SEARCH") || !strings.Contains(joined, "INDEX") || !strings.Contains(joined, "user_id=?") {
		t.Fatalf("план не ищет по индексу user_id:\n%s", joined)
	}
}

// Семантика прежняя: MAX(ts) по каждому роутеру с событиями; роутера без
// событий в карте нет (вызывающие подставляют CreatedAt сами).
func TestLatestPerUserAllSemantics(t *testing.T) {
	d := newTestDB(t)
	a, _ := d.Users().Insert("alpha", strings.Repeat("a", 64), "198.51.100.1", "awg0")
	b, _ := d.Users().Insert("bravo", strings.Repeat("b", 64), "198.51.100.2", "awg0")
	c, _ := d.Users().Insert("charlie", strings.Repeat("c", 64), "198.51.100.3", "awg0")
	now := time.Now().UTC().Truncate(time.Second)
	mustIns := func(uid int64, check string, ts time.Time) {
		t.Helper()
		if err := d.Events().Insert(uid, check, "ok", "{}", ts); err != nil {
			t.Fatal(err)
		}
	}
	mustIns(a, "dns", now.Add(-time.Hour))
	mustIns(a, "tunnels", now.Add(-time.Minute))
	mustIns(b, "dns", now.Add(-2*time.Hour))
	got, err := d.Events().LatestPerUserAll()
	if err != nil {
		t.Fatal(err)
	}
	if !got[a].Equal(now.Add(-time.Minute)) || !got[b].Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("got=%v", got)
	}
	if _, ok := got[c]; ok || len(got) != 2 {
		t.Fatalf("роутер без событий попал в карту: %v", got)
	}
}
