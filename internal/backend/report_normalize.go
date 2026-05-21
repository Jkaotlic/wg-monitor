package backend

import (
	"strings"

	"github.com/anex/wg-monitor/pkg/wire"
)

const tunnelHandshakeHealthyMaxAgeSec = 180

func normalizeReportedCheck(c wire.Check) wire.Check {
	if !isLegacyPingCheckDisabledFalseFail(c) {
		return c
	}
	c.Status = "ok"
	d := cloneDetails(c.Details)
	delete(d, "error")
	d["note"] = "pingcheck disabled; tunnel healthy by handshake"
	c.Details = d
	return c
}

func isLegacyPingCheckDisabledFalseFail(c wire.Check) bool {
	if !strings.HasPrefix(c.Name, "tunnel_") || strings.ToLower(c.Status) != "fail" || c.Details == nil {
		return false
	}
	d := c.Details
	pc := strings.ToLower(strings.TrimSpace(stringDetail(d, "ping_check_status")))
	if pc != "disabled" && pc != "off" && pc != "inactive" {
		return false
	}
	if failCount, ok := intDetail(d, "ping_check_fail_count"); ok && failCount > 0 {
		return false
	}
	if status := strings.ToLower(strings.TrimSpace(stringDetail(d, "status"))); status != "" && status != "running" {
		return false
	}
	if enabled, ok := boolDetail(d, "enabled"); ok && !enabled {
		return false
	}
	if conflict, ok := boolDetail(d, "address_conflict"); ok && conflict {
		return false
	}
	age, ok := intDetail(d, "handshake_age_sec")
	return ok && age <= tunnelHandshakeHealthyMaxAgeSec
}

func cloneDetails(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stringDetail(d map[string]any, key string) string {
	if v, ok := d[key].(string); ok {
		return v
	}
	return ""
}

func boolDetail(d map[string]any, key string) (bool, bool) {
	switch v := d[key].(type) {
	case bool:
		return v, true
	default:
		return false, false
	}
}

func intDetail(d map[string]any, key string) (int, bool) {
	switch v := d[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case jsonNumber:
		i, err := v.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

type jsonNumber interface {
	Int64() (int64, error)
}
