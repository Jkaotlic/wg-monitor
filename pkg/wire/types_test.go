package wire

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReport_JSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	r := Report{
		Timestamp:    ts,
		AgentVersion: "0.1.0",
		Checks: []Check{
			{Name: "agent_heartbeat", Status: "ok", DurationMs: 1, Details: map[string]any{}},
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("ts: got %v want %v", got.Timestamp, ts)
	}
	if got.AgentVersion != "0.1.0" {
		t.Errorf("agent_version: got %q", got.AgentVersion)
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "agent_heartbeat" || got.Checks[0].Status != "ok" {
		t.Errorf("checks roundtrip mismatch: %+v", got.Checks)
	}
}

func TestReport_JSONFieldNames(t *testing.T) {
	r := Report{
		Timestamp:    time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		AgentVersion: "0.1.0",
		Checks:       []Check{{Name: "agent_heartbeat", Status: "ok", DurationMs: 1}},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"ts"`, `"agent_version"`, `"checks"`, `"name"`, `"status"`, `"duration_ms"`} {
		if !contains(s, want) {
			t.Errorf("expected JSON to contain %s, got: %s", want, s)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
