// Package actions executes wire.Commands fetched by cmdloop.
//
// Execution maps:
//   - restart_tunnel  → awgmgr.RestartAll
//   - diag_now        → awgmgr GET /api/diagnostics/result (raw body)
//   - pingcheck_now   → awgmgr.PingCheckNow
//   - force_recheck   → caller-provided callback (typically reporter.SendOnce)
//   - opkg_upgrade    → OpkgRunner.DryRun (preflight only — no live upgrade
//     yet; juicy live-upgrade path is deferred to a later iteration)
//   - tunnel_enable/disable → ndmc -c "interface <ndms_name> up|down"
//     (awg-manager API has no per-tunnel start/stop endpoint — Keenetic native
//     ndmc CLI is the authoritative path. NDMSName must be supplied in
//     cmd.Args["ndms_name"] by the backend.)
//
// Every action returns a wire.CommandResult with the original Command.ID
// preserved so the backend can correlate outcome with the TG callback.
package actions

import (
	"context"
	"fmt"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// OpkgExecutor is the surface a real Opkg implementation exposes to Runner.
// SmartUpgrade does the full live workflow (update + space-check + upgrade);
// DryRun is kept for tests / external callers that want a preflight only.
type OpkgExecutor interface {
	DryRun(ctx context.Context) (status, output string)
	SmartUpgrade(ctx context.Context) (status, output string)
}

// Runner is built once at agent startup and re-used per-command.
type Runner struct {
	AwgClient    *awgmgr.Client
	ForceRecheck func(ctx context.Context) // typically wraps reporter.SendOnce
	Opkg         OpkgExecutor
	Exec         ExecFunc // for tunnel_enable/disable via ndmc
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
		return r.Opkg.SmartUpgrade(ctx)
	case "check_via_tunnel":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured"
		}
		return CheckViaTunnel(ctx, r.AwgClient)
	case "check_direct":
		return CheckDirect(ctx)
	case "tunnel_enable", "tunnel_disable":
		if r.Exec == nil {
			return "err", "exec not configured"
		}
		ndms, _ := cmd.Args["ndms_name"].(string)
		if ndms == "" {
			return "err", "tunnel_enable/disable: ndms_name missing in args"
		}
		state := "up"
		if cmd.Action == "tunnel_disable" {
			state = "down"
		}
		out, err := r.Exec(ctx, "ndmc", "-c", fmt.Sprintf("interface %s %s", ndms, state))
		if err != nil {
			return "err", fmt.Sprintf("ndmc interface %s %s: %v\n%s", ndms, state, err, string(out))
		}
		return "ok", fmt.Sprintf("interface %s -> %s\n%s", ndms, state, string(out))
	default:
		return "err", "unknown action: " + cmd.Action
	}
}
