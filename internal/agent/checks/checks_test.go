package checks

import (
	"testing"
	"time"
)

func TestResultOK(t *testing.T) {
	start := time.Now().Add(-50 * time.Millisecond)
	r := OK("awg_handshake", start, map[string]any{"handshake_age_sec": 47})
	if r.Name != "awg_handshake" || r.Status != "ok" {
		t.Fatalf("bad name/status: %+v", r)
	}
	if r.DurationMs < 40 || r.DurationMs > 5000 {
		t.Fatalf("duration looks wrong: %d", r.DurationMs)
	}
	if r.Details["handshake_age_sec"] != 47 {
		t.Fatalf("details lost: %+v", r.Details)
	}
}

func TestResultFail(t *testing.T) {
	start := time.Now()
	r := Fail("awg_routing", start, "exit ip mismatch", map[string]any{"got": "1.2.3.4"})
	if r.Status != "fail" || r.Details["error"] != "exit ip mismatch" {
		t.Fatalf("unexpected: %+v", r)
	}
	if r.Details["got"] != "1.2.3.4" {
		t.Fatalf("extra details lost: %+v", r.Details)
	}
}
