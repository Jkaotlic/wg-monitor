package linkrepair

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Evidence retains a small incident history. Only explicitly selected diagnostic
// fields are stored: raw commands, configurations, rules and credentials never are.
type Evidence struct {
	KV  KVStore
	Now func() time.Time
}
type incident struct {
	At    time.Time      `json:"at"`
	Check wire.Check     `json:"check"`
	Route map[string]any `json:"route"`
}

func evidenceKey(nickname, check string) string {
	return "linkrepair:evidence:" + nickname + ":" + check
}
func (e Evidence) Record(nickname, check string, trigger wire.Check, snap wire.RouteSnapshot) error {
	raw, err := e.KV.Get(evidenceKey(nickname, check))
	if err != nil {
		return err
	}
	var rows []incident
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &rows); err != nil {
			return fmt.Errorf("incident history: %w", err)
		}
	}
	at := time.Now()
	if e.Now != nil {
		at = e.Now()
	}
	details := map[string]any{}
	// Free-form strings from routers are deliberately excluded. Diagnostic codes
	// must be short; confirmation is rebuilt recursively using the same allowlist.
	for _, key := range []string{"failure_kind", "reason", "handshake_age_sec", "has_handshake", "last_handshake", "rx_bytes", "tx_bytes", "rx_delta_bytes", "tx_delta_bytes", "traffic_sample_ms", "traffic_counters_reset", "matrix_ok", "awgm_conn", "enabled", "address_conflict", "ping_check_status", "ping_check_fail_count", "ping_check_fail_threshold", "connectivity", "tunnel_confirmation", "status", "targets", "success", "reachable", "error_kind", "confirmation_results", "confirmation_at", "target", "state", "latency_ms"} {
		if v, ok := trigger.Details[key]; ok {
			if safe := safeEvidenceValue(key, v, 0); safe != nil {
				details[key] = safe
			}
		}
	}
	tunnels := make([]map[string]any, 0, min(len(snap.Tunnels), 32))
	for _, t := range snap.Tunnels[:min(len(snap.Tunnels), 32)] {
		tunnels = append(tunnels, map[string]any{"id": safeEvidenceID(t.ID), "enabled": t.Enabled, "has_handshake": t.HasHandshake, "handshake_age_sec": t.HandshakeAge, "available": t.Available})
	}
	policies := make([]map[string]any, 0, min(len(snap.Policies), 32))
	for _, p := range snap.Policies[:min(len(snap.Policies), 32)] {
		links := []map[string]any{}
		for _, l := range p.Interfaces[:min(len(p.Interfaces), 16)] {
			links = append(links, map[string]any{"tunnel_id": safeEvidenceID(l.TunnelID), "available": l.Available, "order": l.Order, "role": safeEvidenceID(l.Role)})
		}
		policies = append(policies, map[string]any{"active_tunnel_id": safeEvidenceID(p.ActiveTunnelID), "links": links})
	}
	rows = append(rows, incident{At: at, Check: wire.Check{Name: safeEvidenceID(check), Status: safeEvidenceID(trigger.Status), Details: details}, Route: map[string]any{"tunnels": tunnels, "policies": policies, "partial": len(snap.Warnings) > 0}})
	if len(rows) > 8 {
		rows = rows[len(rows)-8:]
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return e.KV.Set(evidenceKey(nickname, check), string(encoded))
}
func safeEvidenceID(v string) string {
	if len(v) > 80 {
		return ""
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return ""
		}
	}
	return v
}
func safeEvidenceValue(key string, v any, depth int) any {
	if depth > 3 {
		return nil
	}
	switch x := v.(type) {
	case bool, float64, int, int64, uint64:
		return x
	case string:
		if key == "confirmation_at" || key == "last_handshake" {
			if at, err := time.Parse(time.RFC3339Nano, x); err == nil {
				return at.UTC().Format(time.RFC3339Nano)
			}
			return nil
		}
		if key == "reason" {
			switch x {
			case "interface_address_missing", "invalid_target", "cancelled", "interface_bind_failed", "request_failed", "target_server_error":
				return x
			}
			return nil
		}
		if s := safeEvidenceID(x); s != "" {
			return s
		}
	case map[string]any:
		out := map[string]any{}
		for _, k := range []string{"status", "targets", "success", "reachable", "error_kind", "failure_kind", "target", "state", "reason", "latency_ms"} {
			if val, ok := x[k]; ok {
				if s := safeEvidenceValue(k, val, depth+1); s != nil {
					out[k] = s
				}
			}
		}
		return out
	case []any:
		out := []any{}
		for _, val := range x[:min(len(x), 2)] {
			if s := safeEvidenceValue(key, val, depth+1); s != nil {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
