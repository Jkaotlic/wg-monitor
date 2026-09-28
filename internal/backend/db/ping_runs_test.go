package db

import (
	"testing"
	"time"
)

// Агент после перезапуска перечитывает двухчасовой буфер и шлёт те же серии
// заново. Строка на серию обязана остаться одной, а открытая -- дорасти.
func TestPingRunUpsertIsIdempotent(t *testing.T) {
	d, uid := newTestDBForFacts(t)
	from := time.Date(2026, 9, 28, 3, 12, 0, 0, time.UTC)
	open := PingRunRow{TunnelID: "awg11", TunnelName: "NL", From: from, To: from.Add(90 * time.Second), Fails: 2}
	if err := d.PingRuns().Upsert(uid, open); err != nil {
		t.Fatal(err)
	}
	closed := open
	closed.To = from.Add(5 * time.Minute)
	closed.Fails = 5
	closed.WentDown = true
	closed.Recovered = true
	closed.Error = "timeout"
	for i := 0; i < 2; i++ {
		if err := d.PingRuns().Upsert(uid, closed); err != nil {
			t.Fatal(err)
		}
	}
	// Запоздалая копия открытой серии не откатывает закрытую назад.
	if err := d.PingRuns().Upsert(uid, open); err != nil {
		t.Fatal(err)
	}
	rows, err := d.PingRuns().Since(uid, from.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("строк %d, хотим 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Fails != 5 || !r.WentDown || !r.Recovered || !r.To.Equal(closed.To) || r.Error != "timeout" {
		t.Fatalf("серия = %+v", r)
	}
}

func TestPingRunsSinceAndPrune(t *testing.T) {
	d, uid := newTestDBForFacts(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	old := PingRunRow{TunnelID: "awg11", From: now.Add(-40 * 24 * time.Hour), To: now.Add(-40 * 24 * time.Hour), Fails: 1}
	fresh := PingRunRow{TunnelID: "awg11", From: now.Add(-time.Hour), To: now.Add(-50 * time.Minute), Fails: 3}
	for _, r := range []PingRunRow{old, fresh} {
		if err := d.PingRuns().Upsert(uid, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := d.PingRuns().Since(uid, now.Add(-24*time.Hour))
	if len(rows) != 1 || rows[0].Fails != 3 {
		t.Fatalf("Since = %+v", rows)
	}
	n, err := d.PingRuns().PruneBefore(now.Add(-30 * 24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune n=%d err=%v", n, err)
	}
}

// P10: больше строк, чем лимит выборки -- отдаются САМЫЕ НОВЫЕ (по from_ts),
// но в порядке возрастания. Слепая обрезка по LIMIT без ORDER ... DESC
// вернула бы старейшие серии флаппинга, а не последние.
func TestPingRunsSinceReturnsNewestUnderLimit(t *testing.T) {
	d, uid := newTestDBForFacts(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	total := pingRunsSinceLimit + 5
	for i := 0; i < total; i++ {
		from := base.Add(time.Duration(i) * time.Minute)
		r := PingRunRow{TunnelID: "awg11", From: from, To: from.Add(10 * time.Second), Fails: i}
		if err := d.PingRuns().Upsert(uid, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.PingRuns().Since(uid, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != pingRunsSinceLimit {
		t.Fatalf("строк %d, хотим лимит %d", len(rows), pingRunsSinceLimit)
	}
	// Ожидаем серии с индексами [5 .. total-1) -- пять самых старых отброшены.
	wantFirstFails := total - pingRunsSinceLimit
	if rows[0].Fails != wantFirstFails {
		t.Fatalf("первая серия fails=%d, хотим %d (не новейшие под лимитом)", rows[0].Fails, wantFirstFails)
	}
	if rows[len(rows)-1].Fails != total-1 {
		t.Fatalf("последняя серия fails=%d, хотим %d", rows[len(rows)-1].Fails, total-1)
	}
	for i := 1; i < len(rows); i++ {
		if !rows[i].From.After(rows[i-1].From) {
			t.Fatalf("порядок не по возрастанию: rows[%d].From=%v <= rows[%d].From=%v", i, rows[i].From, i-1, rows[i-1].From)
		}
	}
}
