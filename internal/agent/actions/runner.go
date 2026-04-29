// Package actions executes wire.Commands fetched by cmdloop.
//
// Execution maps:
//   - restart_tunnel  → awgmgr.RestartAll
//   - diag_now        → awgmgr GET /api/diagnostics/result (raw body)
//   - pingcheck_now   → awgmgr.PingCheckNow
//   - force_recheck   → caller-provided callback (typically reporter.SendOnce)
//   - opkg_upgrade    → OpkgRunner.DryRun (preflight only — no live upgrade
//     yet; juicy live-upgrade path is deferred to a later iteration)
//
// Every action returns a wire.CommandResult with the original Command.ID
// preserved so the backend can correlate outcome with the TG callback.
package actions

import (
	"context"
	"time"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// OpkgExecutor is the surface a real Opkg implementation exposes to Runner.
type OpkgExecutor interface {
	DryRun(ctx context.Context) (status, output string)
}

// Runner is built once at agent startup and re-used per-command.
type Runner struct {
	AwgClient    *awgmgr.Client
	ForceRecheck func(ctx context.Context) // typically wraps reporter.SendOnce
	Opkg         OpkgExecutor
	Now          func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Runner) Execute(ctx context.Context, cmd wire.Command) wire.CommandResult {
	start := r.now()
	status, output := r.dispatch(ctx, cmd)
	return wire.CommandResult{
		ID:         cmd.ID,
		Status:     status,
		Output:     output,
		DurationMs: r.now().Sub(start).Milliseconds(),
	}
}

func (r *Runner) dispatch(ctx context.Context, cmd wire.Command) (status, output string) {
	switch cmd.Action {
	case "restart_tunnel":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured"
		}
		if err := r.AwgClient.RestartAll(ctx); err != nil {
			return "err", err.Error()
		}
		return "ok", "all tunnels restarted (awg-manager)"
	case "pingcheck_now":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured"
		}
		if err := r.AwgClient.PingCheckNow(ctx); err != nil {
			return "err", err.Error()
		}
		return "ok", "pingcheck-now triggered"
	case "diag_now":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured"
		}
		body, err := r.AwgClient.DiagResult(ctx)
		if err != nil {
			return "err", err.Error()
		}
		return "ok", body
	case "force_recheck":
		if r.ForceRecheck == nil {
			return "err", "force_recheck callback not wired"
		}
		r.ForceRecheck(ctx)
		return "ok", "agent report kicked"
	case "opkg_upgrade":
		if r.Opkg == nil {
			return "err", "opkg runner not configured"
		}
		return r.Opkg.DryRun(ctx)
	default:
		return "err", "unknown action: " + cmd.Action
	}
}
