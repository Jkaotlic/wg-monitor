package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/agent/checks"
	"github.com/anex/wg-monitor/pkg/wire"
)

const perCheckTimeout = 10 * time.Second

type Sender interface {
	SendReport(ctx context.Context, r wire.Report) error
}

type Reporter struct {
	sender   Sender
	version  string
	interval time.Duration
	checks   []checks.Check
	deps     checks.Deps
}

func NewReporter(sender Sender, version string, interval time.Duration, chks []checks.Check, deps checks.Deps) *Reporter {
	return &Reporter{sender: sender, version: version, interval: interval, checks: chks, deps: deps}
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

func (r *Reporter) sendOnce(ctx context.Context) {
	start := time.Now()
	results := r.runAll(ctx)
	results = append(results, wire.Check{
		Name: "agent_heartbeat", Status: "ok", DurationMs: time.Since(start).Milliseconds(),
	})
	report := wire.Report{
		Timestamp:    start.UTC(),
		AgentVersion: r.version,
		Checks:       results,
	}
	if err := r.sender.SendReport(ctx, report); err != nil {
		slog.Warn("send report failed", "err", err)
	}
}

func (r *Reporter) runAll(parent context.Context) []wire.Check {
	out := make([]wire.Check, len(r.checks))
	var wg sync.WaitGroup
	for i, c := range r.checks {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, perCheckTimeout)
			defer cancel()
			out[i] = c.Run(ctx, r.deps)
		}()
	}
	wg.Wait()
	return out
}
