// Package actions executes wire.Commands fetched by cmdloop.
//
// Execution maps:
//   - restart_tunnel  → awgmgr.RestartAll
//   - diag_now        → awgmgr GET /api/diagnostics/result (raw body)
//   - pingcheck_now   → awgmgr.PingCheckNow
//   - force_recheck   → caller-provided callback (typically reporter.SendOnce)
//   - opkg_upgrade    → OpkgRunner.SmartUpgrade (opkg update + space check +
//     live upgrade; partial-update failures continue and surface failed feeds)
//   - opkg_feed_disable → OpkgRunner.DisableFeed (comment matching feed in
//     opkg config + auto-retry SmartUpgrade)
//   - tunnel_enable/disable → ndmc -c "interface <ndms_name> up|down"
//     (awg-manager API has no per-tunnel start/stop endpoint — Keenetic native
//     ndmc CLI is the authoritative path. NDMSName must be supplied in
//     cmd.Args["ndms_name"] by the backend.)
//   - service_restart  → init.d for hrneo/awg-mgr; ndmc system reboot for router
//     (router gated on AllowRouterReboot config flag)
//   - firmware_status  → ndmc components list parsed into wire.FirmwareStatus
//   - firmware_install → ndmc components commit (gated on AllowFirmwareInstall)
//   - version_audit    → composite of awgmgr SystemInfo + opkg + components list
//
// Every action returns a wire.CommandResult with the original Command.ID
// preserved so the backend can correlate outcome with the TG callback.
package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// OpkgExecutor is the surface a real Opkg implementation exposes to Runner.
// SmartUpgrade does the full live workflow (update + space-check + upgrade);
// DryRun is kept for tests / external callers that want a preflight only.
type OpkgExecutor interface {
	DryRun(ctx context.Context) (status, output string)
	SmartUpgrade(ctx context.Context) (status, output string, payload wire.OpkgUpgradeResult)
	DisableFeed(ctx context.Context, url string) (status, output string, payload wire.OpkgUpgradeResult)
}

// Runner is built once at agent startup and re-used per-command.
type Runner struct {
	AwgClient            *awgmgr.Client
	ForceRecheck         func(ctx context.Context) // typically wraps reporter.SendOnce
	Opkg                 OpkgExecutor
	Exec                 ExecFunc // for tunnel_enable/disable via ndmc
	Now                  func() time.Time
	AllowRouterReboot    bool // gates `service_restart router`
	AllowFirmwareInstall bool // gates `firmware_install`
	// DiagPollEvery is the interval between DiagResult polls after a DiagRun
	// is triggered by a NO_REPORT response. Zero defaults to 3s.
	DiagPollEvery time.Duration
	// DiagPollMax is the maximum number of poll iterations before DiagNow
	// returns a DIAG_TIMEOUT error. Zero defaults to 12 (= 36s budget).
	DiagPollMax   int
	routeMu       sync.Mutex // serialises concurrent route_rebind calls
}

// DiagNow fetches the awg-manager diagnostic report. If awg-manager
// reports NO_REPORT, DiagNow auto-triggers a fresh run via DiagRun and
// polls DiagResult every r.DiagPollEvery for up to r.DiagPollMax
// iterations. Final outcomes:
//   - immediate 200               → return body, nil
//   - NO_REPORT → run → poll-succeeds → return body, nil
//   - NO_REPORT → run-error       → return "", HTTP_NNN error
//   - NO_REPORT → run → poll-never-resolves → return "", DIAG_TIMEOUT error
//   - other GET error             → return "", that error (typed prefix preserved)
func (r *Runner) DiagNow(ctx context.Context) (string, error) {
	body, err := r.AwgClient.DiagResult(ctx)
	if err == nil {
		return body, nil
	}
	if !isNoReport(err) {
		return "", err
	}
	if runErr := r.AwgClient.DiagRun(ctx); runErr != nil {
		return "", runErr
	}
	every := r.DiagPollEvery
	if every <= 0 {
		every = 3 * time.Second
	}
	max := r.DiagPollMax
	if max <= 0 {
		max = 12
	}
	for i := 0; i < max; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(every):
		}
		body, err := r.AwgClient.DiagResult(ctx)
		if err == nil {
			return body, nil
		}
		if !isNoReport(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("DIAG_TIMEOUT: triggered but no result after %d iterations", max)
}

func isNoReport(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NO_REPORT")
}

func (r *Runner) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Runner) Execute(ctx context.Context, cmd wire.Command) wire.CommandResult {
	start := r.now()
	status, output, payload := r.dispatchWithPayload(ctx, cmd)
	res := wire.CommandResult{
		ID:         cmd.ID,
		Status:     status,
		Output:     output,
		DurationMs: r.now().Sub(start).Milliseconds(),
	}
	if !payload.IsZero() {
		if b, err := json.Marshal(payload); err == nil {
			res.Payload = b
		}
	}
	return res
}

func (r *Runner) dispatchWithPayload(ctx context.Context, cmd wire.Command) (status, output string, payload wire.OpkgUpgradeResult) {
	switch cmd.Action {
	case "restart_tunnel":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		if err := r.AwgClient.RestartAll(ctx); err != nil {
			return "err", err.Error(), payload
		}
		return "ok", "all tunnels restarted (awg-manager)", payload
	case "pingcheck_now":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		if err := r.AwgClient.PingCheckNow(ctx); err != nil {
			return "err", err.Error(), payload
		}
		return "ok", "pingcheck-now triggered", payload
	case "diag_now":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		body, err := r.DiagNow(ctx)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", body, payload
	case "force_recheck":
		if r.ForceRecheck == nil {
			return "err", "force_recheck callback not wired", payload
		}
		r.ForceRecheck(ctx)
		return "ok", "agent report kicked", payload
	case "opkg_upgrade":
		if r.Opkg == nil {
			return "err", "opkg runner not configured", payload
		}
		s, o, p := r.Opkg.SmartUpgrade(ctx)
		return s, o, p
	case "opkg_feed_disable":
		if r.Opkg == nil {
			return "err", "opkg runner not configured", payload
		}
		url, _ := cmd.Args["url"].(string)
		if url == "" {
			return "err", "opkg_feed_disable: url is required", payload
		}
		s, o, p := r.Opkg.DisableFeed(ctx, url)
		return s, o, p
	case "check_via_tunnel":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		s, o := CheckViaTunnel(ctx, r.AwgClient)
		return s, o, payload
	case "check_direct":
		s, o := CheckDirect(ctx)
		return s, o, payload
	case "tunnel_enable", "tunnel_disable":
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		ndms, _ := cmd.Args["ndms_name"].(string)
		if ndms == "" {
			return "err", "tunnel_enable/disable: ndms_name missing in args", payload
		}
		state := "up"
		if cmd.Action == "tunnel_disable" {
			state = "down"
		}
		out, err := r.Exec(ctx, "ndmc", "-c", fmt.Sprintf("interface %s %s", ndms, state))
		if err != nil {
			return "err", fmt.Sprintf("ndmc interface %s %s: %v\n%s", ndms, state, err, string(out)), payload
		}
		return "ok", fmt.Sprintf("interface %s -> %s\n%s", ndms, state, string(out)), payload
	case "tunnel_import":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		confB64, _ := cmd.Args["conf"].(string)
		name, _ := cmd.Args["name"].(string)
		replace, _ := cmd.Args["replace"].(bool)
		if confB64 == "" || name == "" {
			return "err", "tunnel_import: conf and name are required", payload
		}
		out, err := ImportTunnel(ctx, r.AwgClient, r.Exec, confB64, name, replace)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "service_restart":
		name, _ := cmd.Args["name"].(string)
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		switch name {
		case "hrneo":
			out, err := r.Exec(ctx, "/opt/etc/init.d/S99hrneo", "restart")
			if err != nil {
				return "err", fmt.Sprintf("S99hrneo restart: %v\n%s", err, string(out)), payload
			}
			return "ok", "hrneo restart sent\n" + string(out), payload
		case "awgmgr":
			out, err := r.Exec(ctx, "/opt/etc/init.d/S99awg-manager", "restart")
			if err != nil {
				return "err", fmt.Sprintf("S99awg-manager restart: %v\n%s", err, string(out)), payload
			}
			return "ok", "awg-manager restart sent\n" + string(out), payload
		case "router":
			if !r.AllowRouterReboot {
				return "err", "router reboot disabled in agent config", payload
			}
			out, err := r.Exec(ctx, "ndmc", "-c", "system reboot")
			if err != nil {
				return "err", fmt.Sprintf("ndmc system reboot: %v\n%s", err, string(out)), payload
			}
			return "ok", "reboot scheduled\n" + string(out), payload
		default:
			return "err", "unknown service: " + name, payload
		}

	case "firmware_status":
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		fs, err := GetFirmwareStatus(ctx, r.Exec)
		if err != nil {
			return "err", err.Error(), payload
		}
		b, jerr := json.Marshal(fs)
		if jerr != nil {
			return "err", "encode firmware_status: " + jerr.Error(), payload
		}
		return "ok", string(b), payload

	case "firmware_install":
		if !r.AllowFirmwareInstall {
			return "err", "firmware install disabled in agent config", payload
		}
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		if err := InstallFirmware(ctx, r.Exec); err != nil {
			return "err", err.Error(), payload
		}
		return "ok", "firmware install kicked; router will reboot", payload

	case "version_audit":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		va, err := VersionAudit(ctx, r.AwgClient, r.Exec)
		if err != nil {
			return "err", err.Error(), payload
		}
		out, err := EncodeVersionAudit(va)
		if err != nil {
			return "err", "encode version_audit: " + err.Error(), payload
		}
		return "ok", out, payload

	case "route_status":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		out, err := RouteStatus(ctx, r.AwgClient)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "route_rebind":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		srcID, _ := cmd.Args["src_tunnel_id"].(string)
		dstID, _ := cmd.Args["dst_tunnel_id"].(string)
		if srcID == "" || dstID == "" {
			return "err", "route_rebind: src_tunnel_id and dst_tunnel_id are required", payload
		}
		r.routeMu.Lock()
		defer r.routeMu.Unlock()
		out, err := RouteRebind(ctx, r.AwgClient, srcID, dstID)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	default:
		return "err", "unknown action: " + cmd.Action, payload
	}
}
