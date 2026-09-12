package retention

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Чистка грантов на вход в веб-управление идёт вместе с обычным суточным
// проходом обслуживания: своей горутины у неё нет.
//
// Порог -- сутки ПОСЛЕ смерти ссылки, а не момент смерти: строка нужна, чтобы
// было с чем сверить журнал обмена, когда разбираются, кто и откуда входил.
func TestPolicy_PruneDropsStaleWebLinksAndKeepsTheRest(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	const (
		liveHash    = "1111000000000000000000000000000000000000000000000000000000000000"
		freshDead   = "2222000000000000000000000000000000000000000000000000000000000000"
		ancientDead = "3333000000000000000000000000000000000000000000000000000000000000"
	)
	if err := d.WebLinks().Issue(liveHash, 999, now.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.WebLinks().Issue(freshDead, 999, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.WebLinks().Issue(ancientDead, 999, now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}

	p := &Policy{
		DB:     d,
		Cfg:    Config{EventsDays: 30},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return now },
	}
	if err := p.prune(context.Background()); err != nil {
		t.Fatalf("проход обслуживания: %v", err)
	}

	all, err := d.WebLinks().ActiveFor(999, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, h := range all {
		kept[h] = true
	}
	if !kept[liveHash] {
		t.Error("живой грант вычистили -- человек потерял вход посреди работы")
	}
	if !kept[freshDead] {
		t.Error("вчерашний грант вычистили сразу: журнал обмена стало не с чем сверить")
	}
	if kept[ancientDead] {
		t.Error("грант трёхдневной давности остался в базе")
	}
}
