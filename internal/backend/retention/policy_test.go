package retention

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func insertTestUser(t *testing.T, d *db.DB) int64 {
	t.Helper()
	tok := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uid, err := d.Users().Insert("testuser", tok, "10.0.0.1", "awg0")
	if err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	return uid
}

func TestPolicy_Prune_DeletesOldEventsOnly(t *testing.T) {
	d := newTestDB(t)
	uid := insertTestUser(t, d)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	if err := d.Events().Insert(uid, "tunnel_awg11", "ok", "{}", old); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "tunnel_awg11", "ok", "{}", recent); err != nil {
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

	// Recent should survive; old should be gone.
	rows, err := d.Events().LatestEventsByPrefix(uid, "tunnel_")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("expected 1 row after prune, got %d", len(rows))
	}
	if !rows[0].TS.Equal(recent) {
		t.Errorf("survivor TS=%v, want %v (the recent one)", rows[0].TS, recent)
	}
}

func TestPolicy_Prune_NoopWhenAllRecent(t *testing.T) {
	d := newTestDB(t)
	uid := insertTestUser(t, d)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	if err := d.Events().Insert(uid, "tunnel_x", "ok", "{}", now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	p := &Policy{
		DB:     d,
		Cfg:    Config{EventsDays: 30},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return now },
	}
	if err := p.prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := d.Events().LatestEventsByPrefix(uid, "tunnel_")
	if len(rows) != 1 {
		t.Errorf("expected row preserved, got %d", len(rows))
	}
}

func TestPolicy_Vacuum_Runs(t *testing.T) {
	d := newTestDB(t)
	p := &Policy{
		DB:     d,
		Cfg:    Config{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := p.vacuum(context.Background()); err != nil {
		t.Fatalf("VACUUM should succeed on empty DB: %v", err)
	}
}

func TestPolicy_Checkpoint_Runs(t *testing.T) {
	d := newTestDB(t)
	p := &Policy{
		DB:     d,
		Cfg:    Config{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := p.checkpoint(context.Background()); err != nil {
		t.Fatalf("checkpoint should succeed on empty DB: %v", err)
	}
}

func TestPolicy_Run_RespectsContextCancel(t *testing.T) {
	d := newTestDB(t)
	p := &Policy{
		DB:  d,
		Cfg: Config{EventsDays: 30, VacuumInterval: 1 * time.Hour, WALCheckpointEvery: 1 * time.Hour},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
