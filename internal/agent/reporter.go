package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type Sender interface {
	SendReport(ctx context.Context, r wire.Report) error
}

type Reporter struct {
	sender   Sender
	version  string
	interval time.Duration
}

func NewReporter(sender Sender, version string, interval time.Duration) *Reporter {
	return &Reporter{sender: sender, version: version, interval: interval}
}

// Run sends an immediate report and then one per interval until ctx is done.
// Send errors are logged but do not stop the loop — Stage 0 has no JSONL buffer.
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
	report := wire.Report{
		Timestamp:    start.UTC(),
		AgentVersion: r.version,
		Checks: []wire.Check{
			{
				Name:       "agent_heartbeat",
				Status:     "ok",
				DurationMs: time.Since(start).Milliseconds(),
			},
		},
	}
	if err := r.sender.SendReport(ctx, report); err != nil {
		slog.Warn("send report failed", "err", err)
	}
}
