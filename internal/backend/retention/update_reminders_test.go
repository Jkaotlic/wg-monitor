package retention

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Чистка новостей об обновлениях трогает только скрытые, и только давно
// скрытые.
//
// Две границы в одном тесте намеренно: «скрыта, но недавно» и «не скрыта
// вовсе» -- разные причины остаться, и перепутать их легко одним лишним
// условием в WHERE. Новость, которую никто не скрывал, обязана пережить любую
// чистку: удалить её значит забыть про невыполненное обновление и показать его
// потом как свежую новость.
func TestPolicy_Prune_DropsOnlyLongDismissedUpdateNews(t *testing.T) {
	d := newTestDB(t)
	uid := insertTestUser(t, d)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	r := d.UpdateReminders()
	for _, c := range []struct{ component, version string }{
		{"awgmgr", "2.17.0"}, // скрыта давно -- уходит
		{"awgmgr", "2.18.0"}, // скрыта недавно -- остаётся
		{"hrneo", "3.18.3"},  // не скрыта вовсе -- остаётся навсегда
	} {
		if err := r.Ensure(uid, c.component, c.version); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Dismiss(uid, "awgmgr", "2.17.0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Dismiss(uid, "awgmgr", "2.18.0"); err != nil {
		t.Fatal(err)
	}
	// Dismiss ставит время сервера, а «давно» иначе не воспроизвести.
	if _, err := d.SQL().Exec(
		`UPDATE router_update_reminders SET dismissed_at = ? WHERE version = '2.17.0'`,
		now.Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL().Exec(
		`UPDATE router_update_reminders SET dismissed_at = ? WHERE version = '2.18.0'`,
		now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	p := &Policy{
		DB:     d,
		Cfg:    Config{EventsDays: 30},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return now },
	}
	if err := p.prune(context.Background()); err != nil {
		t.Fatalf("prune: %v", err)
	}

	for _, c := range []struct {
		version string
		want    int
		why     string
	}{
		{"2.17.0", 0, "скрытая 100 дней назад новость обязана уйти"},
		{"2.18.0", 1, "скрытая вчера новость ещё не просрочена"},
		{"3.18.3", 1, "новость, которую никто не скрывал, чистка трогать не смеет"},
	} {
		var n int
		if err := d.SQL().QueryRow(
			`SELECT COUNT(*) FROM router_update_reminders WHERE version = ?`, c.version).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != c.want {
			t.Errorf("версия %s: строк %d, ожидалось %d -- %s", c.version, n, c.want, c.why)
		}
	}
}
