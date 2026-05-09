// Package retention runs periodic maintenance on the backend SQLite DB:
// prunes old events, runs VACUUM to reclaim space, and checkpoints the WAL
// so it doesn't grow unbounded after a crash. All three operations are
// individually opt-out via zero-valued config.
package retention

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

// Config governs periodic DB maintenance. Each field 0 disables that operation.
type Config struct {
	EventsDays         int           // delete events older than this; 0 disables
	VacuumInterval     time.Duration // VACUUM cadence; 0 disables
	WALCheckpointEvery time.Duration // wal_checkpoint(TRUNCATE) cadence; 0 disables
}

// Policy is the long-running coordinator. Call Run from a goroutine; cancel
// the context to stop. Each operation runs on its own ticker so a slow
// VACUUM doesn't delay the next prune.
type Policy struct {
	DB     *db.DB
	Cfg    Config
	Logger *slog.Logger
	Now    func() time.Time // overridable for tests
}

func (p *Policy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Run starts the policy. Three independent tickers fire prune / vacuum /
// checkpoint at their configured intervals. Returns when ctx is cancelled.
//
// At startup, all three operations run once after a short delay (5s) so
// stale data from before the backend started gets cleaned up promptly
// without colliding with startup load.
func (p *Policy) Run(ctx context.Context) {
	startupDelay := 5 * time.Second
	pruneEvery := 24 * time.Hour
	if p.Cfg.EventsDays > 0 {
		go p.runLoop(ctx, "prune", pruneEvery, startupDelay, p.prune)
	}
	if p.Cfg.VacuumInterval > 0 {
		go p.runLoop(ctx, "vacuum", p.Cfg.VacuumInterval, startupDelay+1*time.Second, p.vacuum)
	}
	if p.Cfg.WALCheckpointEvery > 0 {
		go p.runLoop(ctx, "wal_checkpoint", p.Cfg.WALCheckpointEvery, startupDelay+2*time.Second, p.checkpoint)
	}
	<-ctx.Done()
}

func (p *Policy) runLoop(ctx context.Context, name string, every, initialDelay time.Duration, fn func(context.Context) error) {
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := fn(ctx); err != nil {
			p.Logger.Warn("retention: operation failed", "op", name, "err", err)
		}
		timer.Reset(every)
	}
}

func (p *Policy) prune(ctx context.Context) error {
	cutoff := p.now().Add(-time.Duration(p.Cfg.EventsDays) * 24 * time.Hour)
	deleted, err := p.DB.Events().PruneBefore(cutoff)
	if err != nil {
		return err
	}
	p.Logger.Info("retention: events pruned", "before", cutoff.UTC(), "deleted", deleted)
	return nil
}

func (p *Policy) vacuum(ctx context.Context) error {
	start := p.now()
	if _, err := p.DB.SQL().ExecContext(ctx, "VACUUM"); err != nil {
		return err
	}
	p.Logger.Info("retention: VACUUM done", "duration_ms", p.now().Sub(start).Milliseconds())
	return nil
}

func (p *Policy) checkpoint(ctx context.Context) error {
	var busy, log, checkpointed sql.NullInt64
	row := p.DB.SQL().QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	if err := row.Scan(&busy, &log, &checkpointed); err != nil {
		return err
	}
	p.Logger.Info("retention: WAL checkpoint", "busy", busy.Int64, "log_pages", log.Int64, "checkpointed_pages", checkpointed.Int64)
	return nil
}
