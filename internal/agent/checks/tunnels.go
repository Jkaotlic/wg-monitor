package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

const (
	defaultHandshakeMaxAge = 180 * time.Second
	tunnelCheckPrefix      = "tunnel_"
)

// TunnelsCheck emits one wire.Check per tunnel from awg-manager
// /api/tunnels/all and /api/pingcheck/status. Per-tunnel check_name is
// "tunnel_<tunnelId>" e.g. "tunnel_awg11" — that's the FSM key.
//
// HandshakeMaxAge: if non-zero, overrides the default 180s threshold for
// "stale handshake" failure. Tunnels with type != "awg"/"wg" are ignored.
type TunnelsCheck struct {
	Client           *awgmgr.Client
	HandshakeMaxAge  time.Duration
}

func (t TunnelsCheck) Group() string { return "tunnels" }

func (t TunnelsCheck) Run(ctx context.Context, _ Deps) []wire.Check {
	start := time.Now()
	maxAge := t.HandshakeMaxAge
	if maxAge <= 0 {
		maxAge = defaultHandshakeMaxAge
	}

	tunnels, err := t.Client.TunnelsAll(ctx)
	if err != nil {
		return []wire.Check{Fail("tunnels", start, err.Error(), nil)}
	}

	// Index ping-status per tunnelId. If pingcheck/status fails, we still
	// emit checks based on tunnels/all — pingCheck data degrades, not blocks.
	pcByID := map[string]awgmgr.PingCheckTunnel{}
	if pc, perr := t.Client.PingCheckStatus(ctx); perr == nil {
		for _, p := range pc.Tunnels {
			pcByID[p.TunnelID] = p
		}
	}

	out := make([]wire.Check, 0, len(tunnels.Tunnels)+1)
	// Synthetic "tunnels" check tracks awg-manager TunnelsAll endpoint health.
	// On error (above) we emit "tunnels=fail"; on success here we emit
	// "tunnels=ok" so the FSM can transition out of HARD when awg-manager
	// recovers — without this, an old "tunnels=fail" incident has no recovery
	// path because the success branch only emits per-tunnel checks.
	out = append(out, OK("tunnels", start, map[string]any{
		"tunnel_count": len(tunnels.Tunnels),
	}))
	for _, tu := range tunnels.Tunnels {
		if tu.Type != "" && tu.Type != "awg" && tu.Type != "wg" {
			continue
		}
		out = append(out, evalTunnel(tu, pcByID[tu.ID], start, maxAge))
	}
	return out
}

func evalTunnel(tu awgmgr.Tunnel, pc awgmgr.PingCheckTunnel, start time.Time, maxAge time.Duration) wire.Check {
	name := tunnelCheckPrefix + tu.ID
	details := map[string]any{
		"tunnel_id":            tu.ID,
		"tunnel_name":          tu.Name,
		"interface":            tu.InterfaceName,
		"ndms_name":            tu.NDMSName,
		"endpoint":             tu.Endpoint,
		"address":              tu.Address,
		"isp_interface":        tu.ResolvedISPInterface,
		"backend":              tu.Backend,
		"awg_version":          tu.AWGVersion,
		"mtu":                  tu.MTU,
		"status":               tu.Status,
		"enabled":              tu.Enabled,
		"default_route_intent": tu.DefaultRoute,
		"address_conflict":     tu.HasAddressConflict,
		"rx_bytes":             tu.RxBytes,
		"tx_bytes":             tu.TxBytes,
	}
	if t := tu.LastHandshake.Time(); t != nil {
		details["last_handshake"] = t.UTC().Format(time.RFC3339)
		details["handshake_age_sec"] = int(time.Since(*t).Seconds())
	}
	if t := tu.StartedAt.Time(); t != nil {
		details["started_at"] = t.UTC().Format(time.RFC3339)
	}
	// pingCheck data: prefer the dedicated /api/pingcheck/status (richer)
	// over the brief object embedded in /api/tunnels/all.
	if pc.TunnelID != "" {
		details["ping_check_status"] = pc.Status
		details["ping_check_method"] = pc.Method
		details["ping_check_fail_count"] = pc.FailCount
		details["ping_check_success_count"] = pc.SuccessCount
		details["ping_check_fail_threshold"] = pc.FailThreshold
		details["ping_check_restart_count"] = pc.RestartCount
		details["ping_check_last_latency_ms"] = pc.LastLatency
	} else {
		details["ping_check_status"] = tu.PingCheck.Status
		details["ping_check_fail_count"] = tu.PingCheck.FailCount
		details["ping_check_fail_threshold"] = tu.PingCheck.FailThreshold
		details["ping_check_restart_count"] = tu.PingCheck.RestartCount
	}

	// A tunnel that's `enabled=false` in the router config is a deliberate
	// admin choice, not a failure. Don't drag it through the FSM as HARD —
	// no handshake / no pingCheck are *expected* when disabled. Mark OK with
	// a `note` so the renderer can still show "выключен" if anyone looks.
	if !tu.Enabled {
		details["note"] = "tunnel disabled in config"
		return OK(name, start, details)
	}

	reasons := tunnelFailReasons(tu, pc, maxAge)
	if len(reasons) == 0 {
		return OK(name, start, details)
	}
	return Fail(name, start, strings.Join(reasons, "; "), details)
}

func tunnelFailReasons(tu awgmgr.Tunnel, pc awgmgr.PingCheckTunnel, maxAge time.Duration) []string {
	var reasons []string
	if tu.Status != "" && tu.Status != "running" {
		reasons = append(reasons, fmt.Sprintf("status=%s", tu.Status))
	}
	if tu.HasAddressConflict {
		reasons = append(reasons, "address conflict on interface")
	}
	if t := tu.LastHandshake.Time(); t == nil {
		reasons = append(reasons, "no handshake ever")
	} else if age := time.Since(*t); age > maxAge {
		reasons = append(reasons, fmt.Sprintf("handshake stale (%ds > %ds)", int(age.Seconds()), int(maxAge.Seconds())))
	}
	if pc.TunnelID != "" {
		if pc.Status != "" && pc.Status != "alive" {
			reasons = append(reasons, fmt.Sprintf("pingCheck=%s (fails %d/%d)", pc.Status, pc.FailCount, pc.FailThreshold))
		}
		// pc.RestartCount is the cumulative restart counter since awg-manager
		// boot, not a current-failure indicator. A live tunnel with a non-zero
		// restart history was previously kept HARD forever — log only via
		// Details (formatter renders it for context).
	} else if tu.PingCheck.Status != "" && tu.PingCheck.Status != "alive" {
		reasons = append(reasons, fmt.Sprintf("pingCheck=%s (fails %d/%d)", tu.PingCheck.Status, tu.PingCheck.FailCount, tu.PingCheck.FailThreshold))
	}
	return reasons
}
