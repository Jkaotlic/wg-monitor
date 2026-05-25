package backend

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestNormalizeReportedCheckCoercesLegacyPingCheckDisabledTunnelFail(t *testing.T) {
	in := wire.Check{
		Name:   "tunnel_awg11",
		Status: "fail",
		Details: map[string]any{
			"status":                    "running",
			"enabled":                   true,
			"handshake_age_sec":         float64(51),
			"ping_check_status":         "disabled",
			"ping_check_fail_count":     float64(0),
			"ping_check_fail_threshold": float64(3),
			"error":                     "pingCheck=disabled (fails 0/3)",
		},
	}
	got := normalizeReportedCheck(in)
	if got.Status != "ok" {
		t.Fatalf("Status=%q, want ok; check=%+v", got.Status, got)
	}
	if errText, _ := got.Details["error"].(string); errText != "" {
		t.Fatalf("legacy false error should be cleared, got %q", errText)
	}
	if note, _ := got.Details["note"].(string); note == "" {
		t.Fatalf("expected normalization note in details: %+v", got.Details)
	}
}

func TestNormalizeReportedCheckKeepsRealTunnelFailure(t *testing.T) {
	in := wire.Check{
		Name:   "tunnel_awg11",
		Status: "fail",
		Details: map[string]any{
			"status":                    "running",
			"enabled":                   true,
			"handshake_age_sec":         float64(260),
			"ping_check_status":         "disabled",
			"ping_check_fail_count":     float64(0),
			"ping_check_fail_threshold": float64(3),
			"error":                     "handshake stale (260s > 180s); pingCheck=disabled (fails 0/3)",
		},
	}
	got := normalizeReportedCheck(in)
	if got.Status != "fail" {
		t.Fatalf("Status=%q, want fail; check=%+v", got.Status, got)
	}
}

func TestNormalizeReportedCheckCoercesDisabledTunnelFail(t *testing.T) {
	in := wire.Check{
		Name:   "tunnel_awg12",
		Status: "fail",
		Details: map[string]any{
			"status":  "disabled",
			"enabled": false,
			"error":   "status=disabled; no handshake ever",
		},
	}
	got := normalizeReportedCheck(in)
	if got.Status != "ok" {
		t.Fatalf("Status=%q, want ok; check=%+v", got.Status, got)
	}
	if errText, _ := got.Details["error"].(string); errText != "" {
		t.Fatalf("disabled tunnel error should be cleared, got %q", errText)
	}
}

func TestIsDisabledTunnelOKAcceptsStatusWithDisabledNote(t *testing.T) {
	c := wire.Check{
		Name:   "tunnel_awg12",
		Status: "ok",
		Details: map[string]any{
			"status": "stopped",
			"note":   "tunnel disabled in config",
		},
	}
	if !isDisabledTunnelOK(c) {
		t.Fatalf("disabled-looking tunnel should be silently cleared: %+v", c)
	}
}
