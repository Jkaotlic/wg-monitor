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
//   - opkg_cron_*     → install/status/log/remove managed scheduled opkg script
//   - entware_clean_* → install/status/run/log/remove managed Entware cleanup
//   - pingcheck_status → awgmgr.PingCheckStatus → JSON passthrough
//   - pingcheck_toggle → awg-mgr POST /api/pingcheck/toggle (primary)
//     with ndmc CLI fallback (interface <ndms_name> ping-check)
//   - tunnel_enable/disable → ndmc -c "interface <ndms_name> up|down"
//   - tunnel_restart → ndmc down, short settle delay, then ndmc up
//     (awg-manager API has no per-tunnel start/stop endpoint — Keenetic native
//     ndmc CLI is the authoritative path. NDMSName must be supplied in
//     cmd.Args["ndms_name"] by the backend.)
//   - tunnel_delete → awg-manager POST /api/tunnels/delete?id=<tunnel_id>
//   - service_restart  → init.d for hrneo/awg-mgr; ndmc system reboot for router
//     (router gated on AllowRouterReboot config flag)
//   - firmware_status  → ndmc components list parsed into wire.FirmwareStatus
//   - firmware_install → ndmc components commit (gated on AllowFirmwareInstall)
//   - version_audit    → composite of awgmgr SystemInfo + opkg + components list
//   - router_doctor    → read-only router health snapshot for Telegram
//   - dns_reset        → wipe dns-proxy DoT/DoH upstreams, apply reference DoT
//     set, then `system configuration save` (ndmc, local exec)
//
// Every action returns a wire.CommandResult with the original Command.ID
// preserved so the backend can correlate outcome with the TG callback.
package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	Sleep                func(ctx context.Context, d time.Duration) error
	AllowRouterReboot    bool // gates `service_restart router`
	AllowFirmwareInstall bool // gates `firmware_install`
	// DiagPollEvery is the interval between DiagResult polls after a DiagRun
	// is triggered by a NO_REPORT response. Zero defaults to 3s.
	DiagPollEvery time.Duration
	// DiagPollMax is the maximum number of poll iterations before DiagNow
	// returns a DIAG_TIMEOUT error. Zero defaults to 12 (= 36s budget).
	DiagPollMax int
	// ConfigPath is the path to the agent's config.yaml. Required for
	// update_backend_url to rewrite the URL in-place.
	ConfigPath string
	// BackendURL is the trusted command-plane origin from the agent config.
	// self_update may use its same-origin /v1/releases/download mirror.
	BackendURL string
	routeMu    sync.Mutex // serialises concurrent route_rebind calls
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

func tunnelNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 404") ||
		strings.Contains(msg, "not_found") ||
		strings.Contains(msg, "not found")
}

func legacyAWGTunnelID(id string) bool {
	if !strings.HasPrefix(id, "awg") || len(id) == len("awg") {
		return false
	}
	for _, r := range strings.TrimPrefix(id, "awg") {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func legacyAWGDeleteFallbackAllowed(t *awgmgr.Tunnel) bool {
	if t == nil {
		return false
	}
	status := tunnelRuntimeState(t)
	iface := strings.ToLower(strings.TrimSpace(t.InterfaceName))
	backend := strings.ToLower(strings.TrimSpace(t.Backend))
	return legacyAWGTunnelID(t.ID) &&
		!t.Enabled &&
		(status == "disabled" || status == "stopped") &&
		strings.HasPrefix(iface, "opkgtun") &&
		backend != "nativewg"
}

func tunnelRuntimeState(t *awgmgr.Tunnel) string {
	if t == nil {
		return ""
	}
	if status := strings.ToLower(strings.TrimSpace(t.Status)); status != "" {
		return status
	}
	return strings.ToLower(strings.TrimSpace(t.State))
}

func forgetLegacyAWGTunnel(ctx context.Context, exec ExecFunc, tunnelID string) error {
	if exec == nil {
		return fmt.Errorf("exec not configured")
	}
	if !legacyAWGTunnelID(tunnelID) {
		return fmt.Errorf("unsafe legacy tunnel id %q", tunnelID)
	}
	paths := []string{
		"/opt/etc/awg-manager/tunnels/" + tunnelID + ".json",
		"/opt/etc/awg-manager/" + tunnelID + ".conf",
	}
	for _, path := range paths {
		if out, err := exec(ctx, "rm", "-f", path); err != nil {
			return fmt.Errorf("rm %s: %v\n%s", path, err, string(out))
		}
	}
	if out, err := exec(ctx, "/opt/etc/init.d/S99awg-manager", "restart"); err != nil {
		return fmt.Errorf("S99awg-manager restart: %v\n%s", err, string(out))
	}
	return nil
}

func (r *Runner) forceForgetLegacyAWGTunnel(ctx context.Context, tunnelID string) error {
	if err := forgetLegacyAWGTunnel(ctx, r.Exec, tunnelID); err != nil {
		return err
	}
	if err := r.sleep(ctx, 2*time.Second); err != nil {
		return fmt.Errorf("legacy cleanup wait %s: %v", tunnelID, err)
	}
	if _, err := getTunnel(ctx, r.AwgClient, tunnelID); err == nil {
		return fmt.Errorf("tunnel_delete: %s still exists after legacy cleanup", tunnelID)
	} else if !tunnelNotFoundError(err) {
		return fmt.Errorf("verify legacy cleanup %s: %v", tunnelID, err)
	}
	return nil
}

func (r *Runner) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
		return "ok", "все туннели перезапущены", payload
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
	case "opkg_cron_status", "opkg_cron_install", "opkg_cron_logs", "opkg_cron_remove":
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		manager := &OpkgCronManager{Exec: r.Exec, Now: r.Now}
		lines := logLinesArg(cmd.Args)
		var (
			status wire.OpkgCronStatus
			err    error
		)
		switch cmd.Action {
		case "opkg_cron_status":
			status, err = manager.Status(ctx, lines)
		case "opkg_cron_install":
			schedule, _ := cmd.Args["schedule"].(string)
			status, err = manager.Install(ctx, schedule)
		case "opkg_cron_logs":
			status, err = manager.Logs(ctx, lines)
		case "opkg_cron_remove":
			status, err = manager.Remove(ctx)
		}
		if err != nil {
			return "err", err.Error(), payload
		}
		b, err := json.Marshal(status)
		if err != nil {
			return "err", "encode opkg cron status: " + err.Error(), payload
		}
		return "ok", string(b), payload
	case "entware_clean_status", "entware_clean_install", "entware_clean_run", "entware_clean_logs", "entware_clean_remove":
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		manager := &EntwareCleanManager{Exec: r.Exec, Now: r.Now}
		lines := logLinesArg(cmd.Args)
		var (
			status wire.EntwareCleanStatus
			err    error
		)
		switch cmd.Action {
		case "entware_clean_status":
			status, err = manager.Status(ctx, lines)
		case "entware_clean_install":
			schedule, _ := cmd.Args["schedule"].(string)
			status, err = manager.Install(ctx, schedule)
		case "entware_clean_run":
			status, err = manager.Run(ctx)
		case "entware_clean_logs":
			status, err = manager.Logs(ctx, lines)
		case "entware_clean_remove":
			status, err = manager.Remove(ctx)
		}
		if err != nil {
			return "err", err.Error(), payload
		}
		b, err := json.Marshal(status)
		if err != nil {
			return "err", "encode entware cleanup status: " + err.Error(), payload
		}
		return "ok", string(b), payload
	case "check_via_tunnel":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		s, o := CheckViaTunnel(ctx, r.AwgClient)
		return s, o, payload
	case "check_direct":
		s, o := CheckDirect(ctx)
		return s, o, payload
	case "pingcheck_status":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		body, err := PingCheckStatusJSON(ctx, r.AwgClient)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", body, payload
	case "pingcheck_toggle":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		tid, _ := cmd.Args["tunnel_id"].(string)
		ndms, _ := cmd.Args["ndms_name"].(string)
		enable, _ := cmd.Args["enable"].(bool)
		if tid == "" || ndms == "" {
			return "err", "pingcheck_toggle: tunnel_id and ndms_name are required", payload
		}
		if !safeNDMSName(ndms) {
			return "err", "pingcheck_toggle: ndms_name must match ^[A-Za-z0-9_-]{1,32}$", payload
		}
		if err := PingCheckToggle(ctx, r.AwgClient, r.Exec, tid, ndms, enable); err != nil {
			return "err", err.Error(), payload
		}
		return "ok", fmt.Sprintf("pingcheck %s for %s", boolEnableLabel(enable), tid), payload
	case "tunnel_enable", "tunnel_disable":
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		ndms, _ := cmd.Args["ndms_name"].(string)
		if ndms == "" {
			return "err", "tunnel_enable/disable: ndms_name missing in args", payload
		}
		if !safeNDMSName(ndms) {
			return "err", "tunnel_enable/disable: ndms_name must match ^[A-Za-z0-9_-]{1,32}$", payload
		}
		state := "up"
		if cmd.Action == "tunnel_disable" {
			state = "down"
		}
		out, err := r.Exec(ctx, "ndmc", "-c", fmt.Sprintf("interface %s %s", ndms, state))
		if err != nil {
			return "err", fmt.Sprintf("ndmc interface %s %s: %v\n%s", ndms, state, err, string(out)), payload
		}
		if r.ForceRecheck != nil {
			r.ForceRecheck(ctx)
		}
		return "ok", fmt.Sprintf("interface %s -> %s\n%s", ndms, state, string(out)), payload
	case "tunnel_restart":
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		ndms, _ := cmd.Args["ndms_name"].(string)
		if ndms == "" {
			return "err", "tunnel_restart: ndms_name missing in args", payload
		}
		if !safeNDMSName(ndms) {
			return "err", "tunnel_restart: ndms_name must match ^[A-Za-z0-9_-]{1,32}$", payload
		}
		outDown, err := r.Exec(ctx, "ndmc", "-c", fmt.Sprintf("interface %s down", ndms))
		if err != nil {
			return "err", fmt.Sprintf("ndmc interface %s down: %v\n%s", ndms, err, string(outDown)), payload
		}
		if err := r.sleep(ctx, time.Second); err != nil {
			return "err", fmt.Sprintf("wait after interface %s down: %v", ndms, err), payload
		}
		outUp, err := r.Exec(ctx, "ndmc", "-c", fmt.Sprintf("interface %s up", ndms))
		if err != nil {
			return "err", fmt.Sprintf("ndmc interface %s up: %v\n%s", ndms, err, string(outUp)), payload
		}
		if r.ForceRecheck != nil {
			r.ForceRecheck(ctx)
		}
		return "ok", fmt.Sprintf("restarted %s\n-- down --\n%s\n-- up --\n%s", ndms, string(outDown), string(outUp)), payload
	case "tunnel_delete":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		tunnelID, _ := cmd.Args["tunnel_id"].(string)
		if tunnelID == "" {
			checkName, _ := cmd.Args["check_name"].(string)
			tunnelID = tunnelIDFromCheckName(checkName)
		}
		forceLegacyCleanup, _ := cmd.Args["force_legacy_cleanup"].(bool)
		if tunnelID == "" {
			return "err", "tunnel_delete: tunnel_id missing in args", payload
		}
		if existing, err := getTunnel(ctx, r.AwgClient, tunnelID); err == nil {
			if tunnelRuntimeState(existing) == "running" || tunnelRuntimeState(existing) == "connected" {
				if err := r.AwgClient.StopTunnel(ctx, tunnelID); err != nil {
					return "err", fmt.Sprintf("stop tunnel %s before delete: %v", tunnelID, err), payload
				}
			}
			if existing.DefaultRoute {
				if err := r.AwgClient.ToggleDefaultRoute(ctx, tunnelID); err != nil {
					return "err", fmt.Sprintf("toggle default-route off for %s before delete: %v", tunnelID, err), payload
				}
			}
			if existing.Enabled {
				if err := r.AwgClient.ToggleEnabled(ctx, tunnelID); err != nil {
					return "err", fmt.Sprintf("toggle enabled off for %s before delete: %v", tunnelID, err), payload
				}
			}
		}
		forcedLegacy := forceLegacyCleanup && legacyAWGTunnelID(tunnelID)
		if err := r.AwgClient.DeleteTunnel(ctx, tunnelID); err != nil {
			if forcedLegacy {
				if cleanupErr := r.forceForgetLegacyAWGTunnel(ctx, tunnelID); cleanupErr != nil {
					return "err", fmt.Sprintf("legacy cleanup %s after delete error %v: %v", tunnelID, err, cleanupErr), payload
				}
				if r.ForceRecheck != nil {
					r.ForceRecheck(ctx)
				}
				return "ok", fmt.Sprintf("tunnel %s deleted with legacy cleanup after awgmgr delete error: %v", tunnelID, err), payload
			}
			return "err", err.Error(), payload
		}
		if remaining, err := getTunnel(ctx, r.AwgClient, tunnelID); err == nil {
			if !legacyAWGDeleteFallbackAllowed(remaining) && !forcedLegacy {
				return "err", fmt.Sprintf("tunnel_delete: %s still exists after delete", tunnelID), payload
			}
			if err := r.forceForgetLegacyAWGTunnel(ctx, tunnelID); err != nil {
				return "err", fmt.Sprintf("legacy cleanup %s: %v", tunnelID, err), payload
			}
		} else if !tunnelNotFoundError(err) {
			return "err", fmt.Sprintf("verify delete %s: %v", tunnelID, err), payload
		}
		if r.ForceRecheck != nil {
			r.ForceRecheck(ctx)
		}
		return "ok", fmt.Sprintf("tunnel %s deleted", tunnelID), payload
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
		backend, _ := cmd.Args["backend"].(string)
		if confB64 == "" || name == "" {
			return "err", "tunnel_import: conf and name are required", payload
		}
		out, err := ImportTunnel(ctx, r.AwgClient, r.Exec, r.sleep, confB64, name, replace, backend)
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
		case "hrneo_start":
			out, err := r.Exec(ctx, "/opt/etc/init.d/S99hrneo", "start")
			if err != nil {
				return "err", fmt.Sprintf("S99hrneo start: %v\n%s", err, string(out)), payload
			}
			return "ok", "hrneo start sent\n" + string(out), payload
		case "hrneo_stop":
			out, err := r.Exec(ctx, "/opt/etc/init.d/S99hrneo", "stop")
			if err != nil {
				return "err", fmt.Sprintf("S99hrneo stop: %v\n%s", err, string(out)), payload
			}
			return "ok", "hrneo stop sent\n" + string(out), payload
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

	case "router_doctor":
		s, o := RouterDoctor(ctx, r.AwgClient, r.Exec)
		return s, o, payload

	case "dns_reset":
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		s, o := DNSReset(ctx, r.Exec)
		return s, o, payload

	case "route_status", "tunnels_status":
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
		return routeRebindCommandStatus(out), out, payload
	case "route_add_plan":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		req := routeAddRequestFromArgs(cmd.Args)
		out, err := RouteAddPlanJSON(ctx, r.AwgClient, req)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "route_templates":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		out, err := RouteTemplatesJSON(ctx, r.AwgClient)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "route_add":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		req := routeAddRequestFromArgs(cmd.Args)
		r.routeMu.Lock()
		defer r.routeMu.Unlock()
		out, err := RouteAddJSON(ctx, r.AwgClient, req)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "route_delete_plan":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		req := routeDeleteRequestFromArgs(cmd.Args)
		out, err := RouteDeletePlanJSON(ctx, r.AwgClient, req)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "route_delete":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		req := routeDeleteRequestFromArgs(cmd.Args)
		r.routeMu.Lock()
		defer r.routeMu.Unlock()
		out, err := RouteDeleteJSON(ctx, r.AwgClient, req)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "hrneo_inventory":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		out, err := HRNeoInventoryJSON(ctx, r.AwgClient)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "hrneo_doctor":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		s, out := HRNeoDoctor(ctx, r.AwgClient, r.Exec)
		return s, out, payload
	case "update_backend_url":
		newURL, _ := cmd.Args["url"].(string)
		if newURL == "" {
			return "err", "update_backend_url: url arg is required", payload
		}
		if r.ConfigPath == "" {
			return "err", "update_backend_url: config path not set on runner", payload
		}
		out, err := UpdateBackendURL(ctx, newURL, r.ConfigPath)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "agent_config_get":
		out, err := GetAgentConfig(r.ConfigPath)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "update_agent_config":
		out, err := UpdateAgentConfig(ctx, cmd.Args, r.ConfigPath)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	case "self_update":
		version, _ := cmd.Args["version"].(string)
		if version == "" {
			return "err", "self_update: version is required", payload
		}
		repoBase, _ := cmd.Args["repo_base"].(string)
		repoResolveIP, _ := cmd.Args["repo_resolve_ip"].(string)
		out, err := SelfUpdate(ctx, version, repoBase, repoResolveIP, r.BackendURL)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", out, payload
	default:
		return "err", "unknown action: " + cmd.Action, payload
	}
}

func routeRebindCommandStatus(out string) string {
	var res wire.RouteRebindResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return "ok"
	}
	if res.DNS.Failed+res.Static.Failed+res.HRNeo.Failed > 0 ||
		len(res.DNS.Errors)+len(res.Static.Errors)+len(res.HRNeo.Errors) > 0 {
		return "partial"
	}
	return "ok"
}

const (
	defaultLogTailLines = 80
	maxLogTailLines     = 300
)

func logLinesArg(args map[string]any) int {
	return boundedIntArg(args, "lines", defaultLogTailLines, 1, maxLogTailLines)
}

func boundedIntArg(args map[string]any, key string, def, min, max int) int {
	if args == nil {
		return def
	}
	if min > max {
		return def
	}
	switch v := args[key].(type) {
	case int:
		return clampIntArg(v, def, min, max)
	case int64:
		return clampInt64Arg(v, def, min, max)
	case float64:
		return clampFloat64Arg(v, def, min, max)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return clampInt64Arg(n, def, min, max)
		}
	}
	return def
}

func clampIntArg(v, def, min, max int) int {
	if v < min {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func clampInt64Arg(v int64, def, min, max int) int {
	if v < int64(min) {
		return def
	}
	if v > int64(max) {
		return max
	}
	return int(v)
}

func clampFloat64Arg(v float64, def, min, max int) int {
	if math.IsNaN(v) {
		return def
	}
	if math.IsInf(v, 1) {
		return max
	}
	if math.IsInf(v, -1) {
		return def
	}
	if v < float64(min) {
		return def
	}
	if v > float64(max) {
		return max
	}
	return int(v)
}

func tunnelIDFromCheckName(checkName string) string {
	checkName = strings.TrimSpace(checkName)
	if !strings.HasPrefix(checkName, "tunnel_") {
		return ""
	}
	return strings.TrimPrefix(checkName, "tunnel_")
}

func boolEnableLabel(enable bool) string {
	if enable {
		return "enabled"
	}
	return "disabled"
}

func safeNDMSName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func routeAddRequestFromArgs(args map[string]any) wire.RouteAddRequest {
	req := wire.RouteAddRequest{}
	req.Kind, _ = args["kind"].(string)
	req.Name, _ = args["name"].(string)
	req.TunnelID, _ = args["tunnel_id"].(string)
	req.UseHRNeo, _ = args["use_hr_neo"].(bool)
	req.TemplateID, _ = args["template_id"].(string)
	req.DraftHash, _ = args["draft_hash"].(string)
	req.Targets = stringSliceArg(args["targets"])
	return req
}

func routeDeleteRequestFromArgs(args map[string]any) wire.RouteDeleteRequest {
	req := wire.RouteDeleteRequest{}
	req.Kind, _ = args["kind"].(string)
	req.RouteID, _ = args["route_id"].(string)
	req.PreviewHash, _ = args["preview_hash"].(string)
	return req
}

func stringSliceArg(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
