package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const perCheckTimeout = 10 * time.Second

// ResumedThreshold: a gap longer than this between successful reports flags
// the next report as Resumed=true. Mobile routers (in cars) pause for
// minutes; we want any pause longer than ~5min treated as "back online".
const ResumedThreshold = 5 * time.Minute

type Sender interface {
	SendReport(ctx context.Context, r wire.Report) error
}

type Reporter struct {
	sender       Sender
	version      string
	interval     time.Duration
	checks       []checks.Check
	multiChecks  []checks.MultiCheck
	deps         checks.Deps
	awgClient    *awgmgr.Client // optional, used to force fresh pingcheck on resume
	statePath    string         // persists last_report_at across agent restarts
	mu           sync.Mutex
	sendOnceMu   sync.Mutex // serialises sendOnce; see sendOnce comment
	lastReportAt time.Time
	forceResumed bool // set by ForceResumed; consumed by next sendOnce
}

type ReporterConfig struct {
	Sender      Sender
	Version     string
	Interval    time.Duration
	Checks      []checks.Check
	MultiChecks []checks.MultiCheck
	Deps        checks.Deps
	AwgClient   *awgmgr.Client
	StatePath   string // e.g. /opt/var/wg-monitor/reporter-state.json; "" disables persistence
}

func NewReporter(cfg ReporterConfig) *Reporter {
	if cfg.Interval <= 0 {
		cfg.Interval = 60 * time.Second
	}
	r := &Reporter{
		sender:      cfg.Sender,
		version:     cfg.Version,
		interval:    cfg.Interval,
		checks:      cfg.Checks,
		multiChecks: cfg.MultiChecks,
		deps:        cfg.Deps,
		awgClient:   cfg.AwgClient,
		statePath:   cfg.StatePath,
	}
	r.lastReportAt = r.loadLastReportAt()
	return r
}

func (r *Reporter) Run(ctx context.Context) {
	r.sendOnce(ctx)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sendOnce(ctx)
		}
	}
}

// ForceResumed triggers an immediate report cycle with Resumed=true regardless
// of last-report timing. Wired into the cmdloop force_recheck action so an
// admin can poke a mobile router into a fresh report from TG.
func (r *Reporter) ForceResumed(ctx context.Context) {
	r.mu.Lock()
	r.forceResumed = true
	r.mu.Unlock()
	r.sendOnce(ctx)
}

// sendMu serialises sendOnce — both the scheduled tick and ForceResumed call
// sendOnce. Without serialisation, concurrent invocations issue duplicate
// /v1/report POSTs and pollute events with same-timestamp duplicates.
func (r *Reporter) sendOnce(ctx context.Context) {
	r.sendOnceMu.Lock()
	defer r.sendOnceMu.Unlock()
	r.sendOnceLocked(ctx)
}

func (r *Reporter) sendOnceLocked(ctx context.Context) {
	start := time.Now()

	r.mu.Lock()
	prev := r.lastReportAt
	forced := r.forceResumed
	r.forceResumed = false
	r.mu.Unlock()
	resumed := forced || (!prev.IsZero() && time.Since(prev) > ResumedThreshold)

	if resumed && r.awgClient != nil {
		// Kick awg-manager into a fresh ping cycle so our /pingcheck/status
		// fetch a moment later returns up-to-date failCount/restartCount.
		// Best-effort: ignore errors and continue with whatever we have.
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := r.awgClient.PingCheckNow(cctx); err != nil {
			slog.Debug("force pingcheck on resume failed", "err", err)
		}
		cancel()
		// Brief settle window so awg-manager's internal state reflects the kick.
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}

	results := r.runAll(ctx)
	results = append(results, wire.Check{
		Name:       "agent_heartbeat",
		Status:     "ok",
		DurationMs: time.Since(start).Milliseconds(),
	})

	report := wire.Report{
		Timestamp:    start.UTC(),
		AgentVersion: r.version,
		Checks:       results,
		Resumed:      resumed,
	}
	if err := r.sender.SendReport(ctx, report); err != nil {
		slog.Warn("send report failed", "err", err)
		return
	}
	now := time.Now()
	r.mu.Lock()
	r.lastReportAt = now
	r.mu.Unlock()
	r.persistLastReportAt(now)
}

func (r *Reporter) runAll(parent context.Context) []wire.Check {
	var (
		mu  sync.Mutex
		out []wire.Check
		wg  sync.WaitGroup
	)
	for _, c := range r.checks {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, perCheckTimeout)
			defer cancel()
			res := c.Run(ctx, r.deps)
			mu.Lock()
			out = append(out, res)
			mu.Unlock()
		}()
	}
	for _, mc := range r.multiChecks {
		mc := mc
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, perCheckTimeout)
			defer cancel()
			res := mc.Run(ctx, r.deps)
			mu.Lock()
			out = append(out, res...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

type reporterState struct {
	LastReportAt time.Time `json:"last_report_at"`
}

func (r *Reporter) loadLastReportAt() time.Time {
	if r.statePath == "" {
		return time.Time{}
	}
	body, err := os.ReadFile(r.statePath)
	if err != nil {
		return time.Time{}
	}
	var s reporterState
	if err := json.Unmarshal(body, &s); err != nil {
		// State file existed but was corrupt: surface so accumulated stale
		// `.tmp` artefacts or aborted writes are diagnosable.
		slog.Warn("reporter state corrupt; treating agent as freshly-started", "path", r.statePath, "err", err)
		return time.Time{}
	}
	return s.LastReportAt
}

func (r *Reporter) persistLastReportAt(t time.Time) {
	if r.statePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0o755); err != nil {
		slog.Debug("reporter state mkdir", "err", err)
		return
	}
	body, _ := json.Marshal(reporterState{LastReportAt: t})
	tmp := r.statePath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		slog.Debug("reporter state write", "err", err)
		return
	}
	if err := os.Rename(tmp, r.statePath); err != nil {
		slog.Debug("reporter state rename", "err", err)
	}
}
