// Package retention runs periodic maintenance on the backend SQLite DB:
// prunes old events, runs VACUUM to reclaim space, and checkpoints the WAL
// so it doesn't grow unbounded after a crash. All three operations are
// individually opt-out via zero-valued config.
package retention

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Config governs periodic DB maintenance. Each field 0 disables that operation.
type Config struct {
	EventsDays         int           // delete events older than this; 0 disables
	VacuumInterval     time.Duration // VACUUM cadence; 0 disables
	WALCheckpointEvery time.Duration // wal_checkpoint(TRUNCATE) cadence; 0 disables
}

// Policy is the long-running coordinator. Call Run from a goroutine; cancel
// the context to stop. Each operation runs on its own ticker.
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

// Run starts the policy. Three independent tickers fire prune, vacuum, and
// checkpoint at their configured intervals. Returns when ctx is cancelled.
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
	flapCutoff := cutoff.UTC().Format(time.DateOnly)
	flapsDeleted := int64(0)
	if res, err := p.DB.SQL().ExecContext(ctx, `DELETE FROM daily_soft_flaps WHERE date < ?`, flapCutoff); err != nil {
		p.Logger.Warn("retention: daily_soft_flaps prune failed", "err", err)
	} else {
		flapsDeleted, _ = res.RowsAffected()
	}
	orphanCutoff := p.now().Add(-7 * 24 * time.Hour).UTC()
	orphanDeleted := int64(0)
	if res, err := p.DB.SQL().ExecContext(ctx,
		`DELETE FROM incident_state WHERE (user_id, check_name) NOT IN (
		    SELECT user_id, check_name FROM events WHERE ts >= ?
		 )`, orphanCutoff); err != nil {
		p.Logger.Warn("retention: incident_state orphan prune failed", "err", err)
	} else {
		orphanDeleted, _ = res.RowsAffected()
	}
	p.Logger.Info("retention: pruned",
		"before", cutoff.UTC(),
		"events_deleted", deleted,
		"daily_flaps_deleted", flapsDeleted,
		"incident_orphans_deleted", orphanDeleted)
	return nil
}

// vacuum reclaims free pages. In-place full VACUUM holds an exclusive lock on
// the whole database for its entire duration, which on a busy backend blocks
// /v1/report writes until busy_timeout and produces false OFFLINE alerts
// (BE-01). Instead we convert the DB to incremental auto-vacuum once (the only
// full VACUUM that ever runs — it rewrites the file to apply the mode change),
// then every later tick runs PRAGMA incremental_vacuum, which frees pages
// incrementally without locking the database for minutes.
func (p *Policy) vacuum(ctx context.Context) error {
	start := p.now()
	maint, err := sql.Open("sqlite", "file:"+p.DB.Path()+"?_pragma=busy_timeout(100)")
	if err != nil {
		return err
	}
	defer maint.Close()
	maint.SetMaxOpenConns(1)

	const incremental = 2 // SQLite auto_vacuum: 0=NONE, 1=FULL, 2=INCREMENTAL
	var mode int
	if err := maint.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return fmt.Errorf("read auto_vacuum: %w", err)
	}
	if mode != incremental {
		if _, err := maint.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
			return fmt.Errorf("set auto_vacuum incremental: %w", err)
		}
		// The mode change only takes effect once the file is rewritten.
		if _, err := maint.ExecContext(ctx, "VACUUM"); err != nil {
			p.Logger.Warn("retention: VACUUM (incremental conversion) failed", "duration_ms", p.now().Sub(start).Milliseconds(), "err", err)
			return err
		}
		p.Logger.Info("retention: converted DB to incremental auto-vacuum", "duration_ms", p.now().Sub(start).Milliseconds())
		return nil
	}
	if _, err := maint.ExecContext(ctx, "PRAGMA incremental_vacuum"); err != nil {
		p.Logger.Warn("retention: incremental_vacuum failed", "duration_ms", p.now().Sub(start).Milliseconds(), "err", err)
		return err
	}
	p.Logger.Info("retention: incremental_vacuum done", "duration_ms", p.now().Sub(start).Milliseconds())
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
