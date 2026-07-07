package retention

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
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

// dbAutoVacuumMode reads the on-disk auto_vacuum mode via a fresh connection so
// it reflects the actual file header rather than a connection's cached view.
func dbAutoVacuumMode(t *testing.T, path string) int {
	t.Helper()
	c, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open db for pragma read: %v", err)
	}
	defer c.Close()
	var mode int
	if err := c.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		t.Fatalf("read auto_vacuum: %v", err)
	}
	return mode
}

// TestPolicy_Vacuum_ConvertsToIncremental pins BE-01: instead of a blocking
// in-place full VACUUM on every tick, the policy converts the DB to incremental
// auto-vacuum once, then reclaims free pages incrementally without holding the
// writer lock for the whole database.
func TestPolicy_Vacuum_ConvertsToIncremental(t *testing.T) {
	d := newTestDB(t)
	p := &Policy{
		DB:     d,
		Cfg:    Config{VacuumInterval: time.Hour},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	const incremental = 2
	if got := dbAutoVacuumMode(t, d.Path()); got == incremental {
		t.Fatalf("fresh DB already incremental (%d); test would not exercise conversion", got)
	}
	// First run converts NONE -> incremental.
	if err := p.vacuum(context.Background()); err != nil {
		t.Fatalf("vacuum (convert): %v", err)
	}
	if got := dbAutoVacuumMode(t, d.Path()); got != incremental {
		t.Fatalf("auto_vacuum=%d after first vacuum, want %d (incremental)", got, incremental)
	}
	// Second run takes the incremental path and must not error or regress mode.
	if err := p.vacuum(context.Background()); err != nil {
		t.Fatalf("vacuum (incremental): %v", err)
	}
	if got := dbAutoVacuumMode(t, d.Path()); got != incremental {
		t.Fatalf("auto_vacuum=%d after second vacuum, want %d", got, incremental)
	}
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

func TestPolicy_Vacuum_DoesNotWaitForPrimaryConnection(t *testing.T) {
	d := newTestDB(t)
	tx, err := d.SQL().Begin()
	if err != nil {
		t.Fatalf("begin primary tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO users (nickname, token_hash, expected_exit_ip, awg_iface) VALUES (?, ?, ?, ?)`,
		"vacuum-busy",
		strings.Repeat("b", 64),
		"10.0.0.2",
		"awg0",
	); err != nil {
		t.Fatalf("hold primary write tx: %v", err)
	}

	p := &Policy{
		DB:     d,
		Cfg:    Config{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	err = p.vacuum(ctx)
	if err == nil {
		t.Fatal("vacuum unexpectedly succeeded while primary connection is held")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("vacuum waited for the primary DB connection: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("vacuum waited too long under SQLite write lock: %v (%v)", err, elapsed)
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

// TestPolicy_RunLoop_FixedPhase_NoDriftFromSlowOp pins C14: runLoop must not
// let a slow fn push subsequent fires later by fn's own duration each cycle
// (the old code called timer.Reset(every) AFTER fn returned, so the cadence
// drifted by the operation's runtime every tick). With a fixed-phase
// schedule, fire N should land near start+N*every regardless of how long fn
// takes (as long as it stays under `every`).
func TestPolicy_RunLoop_FixedPhase_NoDriftFromSlowOp(t *testing.T) {
	p := &Policy{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const every = 100 * time.Millisecond
	const opDuration = 40 * time.Millisecond
	const wantFires = 5

	var mu sync.Mutex
	var fireTimes []time.Time
	ctx, cancel := context.WithCancel(context.Background())
	fn := func(context.Context) error {
		mu.Lock()
		fireTimes = append(fireTimes, time.Now())
		n := len(fireTimes)
		mu.Unlock()
		time.Sleep(opDuration)
		if n >= wantFires {
			cancel()
		}
		return nil
	}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		p.runLoop(ctx, "test", every, 0, fn)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runLoop did not stop after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(fireTimes) < wantFires {
		t.Fatalf("expected >= %d fires, got %d", wantFires, len(fireTimes))
	}
	const tolerance = 40 * time.Millisecond
	for i, ft := range fireTimes {
		want := start.Add(time.Duration(i) * every)
		if drift := ft.Sub(want); drift < -tolerance || drift > tolerance {
			t.Errorf("fire[%d] drift = %v (fired at %v after start, want ~%v after start)",
				i, drift, ft.Sub(start), want.Sub(start))
		}
	}
}

func TestPolicy_Run_RespectsContextCancel(t *testing.T) {
	d := newTestDB(t)
	p := &Policy{
		DB:     d,
		Cfg:    Config{EventsDays: 30, VacuumInterval: 1 * time.Hour, WALCheckpointEvery: 1 * time.Hour},
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
