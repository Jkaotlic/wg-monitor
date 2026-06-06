package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const (
	defaultHandshakeMaxAge = 180 * time.Second
	maxHandshakeFutureSkew = 2 * time.Minute
	tunnelCheckPrefix      = "tunnel_"
)

// TunnelsCheck emits one wire.Check per tunnel from awg-manager
// /api/tunnels/all and /api/pingcheck/status. Per-tunnel check_name is
// "tunnel_<tunnelId>" e.g. "tunnel_awg11" — that's the FSM key.
//
// HandshakeMaxAge: if non-zero, overrides the default 180s threshold for
// "stale handshake" failure. Tunnels with type != "awg"/"wg" are ignored.
type TunnelsCheck struct {
	Client          *awgmgr.Client
	HandshakeMaxAge time.Duration
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

	// Best-effort: tally per-iface route counts so HARD alerts can show
	// the blast radius ("this tunnel carries N DNS rules"). Fall-through
	// HR-Neo rules (routes=nil, hydraroute backend) are credited to the
	// default-route tunnel — mirrors what RouteRebind would migrate.
	// Any list error → empty map → alert silently omits the line.
	routeCounts := tallyRouteCounts(ctx, t.Client, tunnels.Tunnels)

	out := make([]wire.Check, 0, len(tunnels.Tunnels)+1)
	// Synthetic "tunnels" check tracks awg-manager TunnelsAll endpoint health.
	// On error (above) we emit "tunnels=fail"; on success here we emit
	// "tunnels=ok" so the FSM can transition out of HARD when awg-manager
	// recovers — without this, an old "tunnels=fail" incident has no recovery
	// path because the success branch only emits per-tunnel checks.
	enabledCount := 0
	for _, tu := range tunnels.Tunnels {
		if tu.Enabled {
			enabledCount++
		}
	}
	out = append(out, OK("tunnels", start, map[string]any{
		"tunnel_count":         len(tunnels.Tunnels),
		"tunnel_count_enabled": enabledCount,
	}))
	for _, tu := range tunnels.Tunnels {
		if tu.Type != "" && tu.Type != "awg" && tu.Type != "wg" {
			continue
		}
		out = append(out, evalTunnel(tu, pcByID[tu.ID], routeCounts[tu.InterfaceName], start, maxAge))
	}
	return out
}

// routeCounts is the per-iface tally embedded in tunnel_* check details.
// Zero value means "no rules attached" — the formatter omits the line.
type routeCounts struct {
	DNS    int // total DNS rules pointing at this iface (explicit + fall-through credit)
	DNSHR  int // subset of DNS that are HR-Neo (backend=hydraroute)
	Static int // static-IP rules pointing at this iface
}

// tallyRouteCounts queries dns-routes + static-routes and returns counts per
// interface name (e.g. "nwg1"). Fall-through HR-Neo rules (routes=nil,
// hrPolicyName set) are credited to the default-route tunnel. Either list
// failing returns an empty map — degraded, not fatal.
func tallyRouteCounts(ctx context.Context, c *awgmgr.Client, tunnels []awgmgr.Tunnel) map[string]routeCounts {
	dns, err := c.ListDNSRoutes(ctx)
	if err != nil {
		return nil
	}
	statics, err := c.ListStaticRoutes(ctx)
	if err != nil {
		return nil
	}
	defaultIface := ""
	for _, tu := range tunnels {
		if tu.DefaultRoute && tunnelRouteFallbackUsable(tu) {
			defaultIface = tu.InterfaceName
			break
		}
	}
	out := map[string]routeCounts{}
	bump := func(iface string, dns, hr, stat int) {
		if iface == "" {
			return
		}
		c := out[iface]
		c.DNS += dns
		c.DNSHR += hr
		c.Static += stat
		out[iface] = c
	}
	for _, r := range dns {
		if !r.Enabled {
			continue
		}
		isHR := r.Backend == "hydraroute"
		hr := 0
		if isHR {
			hr = 1
		}
		if len(r.Routes) > 0 {
			// Credit primary route only — mirrors route_status.buildRouteSnapshot.
			// awg-mgr 2.8.1+ introduced automatic failover where a rule can
			// list a backup route after the primary. The blast-radius signal
			// we want here is "if THIS iface dies, how many rules are
			// initially affected?", which is the primary route's count. A
			// follow-up could track failover separately.
			bump(r.Routes[0].Interface, 1, hr, 0)
			continue
		}
		// Fall-through HR-Neo rule → follows global policy → default tunnel.
		if isHR && r.HRPolicyName != "" && defaultIface != "" {
			bump(defaultIface, 1, 1, 0)
		}
	}
	for _, r := range statics {
		if !r.Enabled {
			continue
		}
		bump(r.TunnelID, 0, 0, 1)
	}
	return out
}

func evalTunnel(tu awgmgr.Tunnel, pc awgmgr.PingCheckTunnel, rc routeCounts, start time.Time, maxAge time.Duration) wire.Check {
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
	if rc.DNS > 0 || rc.Static > 0 {
		details["routes_dns"] = rc.DNS
		details["routes_dns_hr"] = rc.DNSHR
		details["routes_static"] = rc.Static
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
	if suppressUnusedTunnelFailure(tu, pc, rc, reasons) {
		details["note"] = "tunnel has no DNS/static routes and pingCheck is disabled"
		return OK(name, start, details)
	}
	return Fail(name, start, strings.Join(reasons, "; "), details)
}

func tunnelRouteFallbackUsable(tu awgmgr.Tunnel) bool {
	if !tu.Enabled || strings.TrimSpace(tu.InterfaceName) == "" {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(firstNonEmptyTunnel(tu.Status, tu.State)))
	return status == "" || status == "running" || status == "connected"
}

func suppressUnusedTunnelFailure(tu awgmgr.Tunnel, pc awgmgr.PingCheckTunnel, rc routeCounts, reasons []string) bool {
	if rc.DNS != 0 || rc.Static != 0 {
		return false
	}
	if !pingCheckDisabled(tu, pc) {
		return false
	}
	for _, reason := range reasons {
		switch {
		case strings.HasPrefix(reason, "status="), strings.HasPrefix(reason, "no handshake"), strings.HasPrefix(reason, "handshake stale"):
			continue
		default:
			return false
		}
	}
	return true
}

func pingCheckDisabled(tu awgmgr.Tunnel, pc awgmgr.PingCheckTunnel) bool {
	status := tu.PingCheck.Status
	if pc.TunnelID != "" {
		status = pc.Status
	}
	return strings.EqualFold(strings.TrimSpace(status), "disabled")
}

func firstNonEmptyTunnel(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
	} else if age := time.Since(*t); age < -maxHandshakeFutureSkew {
		reasons = append(reasons, fmt.Sprintf("handshake timestamp in future (%ds)", int((-age).Seconds())))
	} else if age > maxAge {
		reasons = append(reasons, fmt.Sprintf("handshake stale (%ds > %ds)", int(age.Seconds()), int(maxAge.Seconds())))
	}
	if pc.TunnelID != "" {
		if isPingCheckFailureStatus(pc.Status) {
			reasons = append(reasons, fmt.Sprintf("pingCheck=%s (fails %d/%d)", pc.Status, pc.FailCount, pc.FailThreshold))
		}
		// pc.RestartCount is the cumulative restart counter since awg-manager
		// boot, not a current-failure indicator. A live tunnel with a non-zero
		// restart history was previously kept HARD forever — log only via
		// Details (formatter renders it for context).
	} else if isPingCheckFailureStatus(tu.PingCheck.Status) {
		reasons = append(reasons, fmt.Sprintf("pingCheck=%s (fails %d/%d)", tu.PingCheck.Status, tu.PingCheck.FailCount, tu.PingCheck.FailThreshold))
	}
	return reasons
}

func isPingCheckFailureStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "dead", "down", "fail", "failed", "error", "unreachable":
		return true
	default:
		return false
	}
}
