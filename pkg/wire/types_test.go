package wire

import (
	"encoding/json"
	"strings"
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

func TestCommand_JSONRoundTrip(t *testing.T) {
	issued := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	c := Command{
		ID:       "01HZX-restart-1",
		Action:   "restart_tunnel",
		Args:     map[string]any{"tunnel_id": "nwg0"},
		IssuedAt: issued,
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Command
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "01HZX-restart-1" {
		t.Errorf("id: got %q", got.ID)
	}
	if got.Action != "restart_tunnel" {
		t.Errorf("action: got %q", got.Action)
	}
	if !got.IssuedAt.Equal(issued) {
		t.Errorf("issued_at: got %v want %v", got.IssuedAt, issued)
	}
	if got.Args["tunnel_id"] != "nwg0" {
		t.Errorf("args.tunnel_id: got %v", got.Args["tunnel_id"])
	}
}

func TestCommand_JSONFieldNames(t *testing.T) {
	c := Command{ID: "x", Action: "diag_now", IssuedAt: time.Now()}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"id"`, `"action"`, `"issued_at"`} {
		if !contains(s, want) {
			t.Errorf("expected JSON to contain %s, got: %s", want, s)
		}
	}
	// args is omitempty when nil/empty
	if contains(s, `"args"`) {
		t.Errorf("expected omitempty for args when nil, got: %s", s)
	}
}

func TestIsValidCommandAction(t *testing.T) {
	cases := map[string]bool{
		"restart_tunnel":   true,
		"diag_now":         true,
		"pingcheck_now":    true,
		"opkg_upgrade":     true,
		"force_recheck":    true,
		"service_restart":  true,
		"firmware_status":  true,
		"firmware_install": true,
		"version_audit":    true,
		"router_doctor":    true,
		"tunnel_delete":    true,
		"tunnels_status":   true,
		"hrneo_doctor":     true,
		"":                 false,
		"reboot":           false,
		"silence":          false, // not a command-channel action
	}
	for in, want := range cases {
		got := IsValidCommandAction(in)
		if got != want {
			t.Errorf("IsValidCommandAction(%q) = %v want %v", in, got, want)
		}
	}
}

func TestCommandResult_JSONRoundTrip(t *testing.T) {
	res := CommandResult{
		ID:         "01HZX-restart-1",
		Status:     "ok",
		Output:     "all tunnels restarted",
		DurationMs: 1234,
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CommandResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != res.ID || got.Status != res.Status || got.Output != res.Output || got.DurationMs != res.DurationMs {
		t.Errorf("roundtrip mismatch: got %+v want %+v", got, res)
	}
	s := string(b)
	for _, want := range []string{`"id"`, `"status"`, `"output"`, `"duration_ms"`} {
		if !contains(s, want) {
			t.Errorf("expected JSON to contain %s, got: %s", want, s)
		}
	}
}

func TestCommandResult_OmitsEmptyOutput(t *testing.T) {
	res := CommandResult{ID: "x", Status: "ok", DurationMs: 1}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if contains(string(b), `"output"`) {
		t.Errorf("expected omitempty for output when blank, got: %s", b)
	}
}

func TestIsValidCommandResultStatus(t *testing.T) {
	cases := map[string]bool{
		"ok":      true,
		"err":     true,
		"locked":  true,
		"timeout": true,
		"":        false,
		"failed":  false,
	}
	for in, want := range cases {
		got := IsValidCommandResultStatus(in)
		if got != want {
			t.Errorf("IsValidCommandResultStatus(%q) = %v want %v", in, got, want)
		}
	}
}

func TestIsValidCommandAction_TunnelImport(t *testing.T) {
	if !IsValidCommandAction("tunnel_import") {
		t.Error("tunnel_import must be valid")
	}
}

func TestIsValidCommandAction_OpkgFeedDisable(t *testing.T) {
	if !IsValidCommandAction("opkg_feed_disable") {
		t.Error("opkg_feed_disable should be a valid command action")
	}
}

func TestOpkgUpgradeResult_JSONRoundTrip(t *testing.T) {
	in := OpkgUpgradeResult{
		Output:      "✅ Обновлено: 3 пакета",
		FailedFeeds: []string{"https://dead.example/Packages.gz"},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out OpkgUpgradeResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Output != in.Output {
		t.Errorf("Output: got %q want %q", out.Output, in.Output)
	}
	if len(out.FailedFeeds) != 1 || out.FailedFeeds[0] != in.FailedFeeds[0] {
		t.Errorf("FailedFeeds: got %v want %v", out.FailedFeeds, in.FailedFeeds)
	}
}

func TestOpkgUpgradeResult_OmitsEmptyFailedFeeds(t *testing.T) {
	in := OpkgUpgradeResult{Output: "ok"}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "failed_feeds") {
		t.Errorf("empty failed_feeds should be omitted, got %s", b)
	}
}

func TestCommandResult_PayloadRoundTrip(t *testing.T) {
	payload := OpkgUpgradeResult{FailedFeeds: []string{"https://x/Packages.gz"}}
	pb, _ := json.Marshal(payload)
	in := CommandResult{
		ID:      "cmd-1",
		Status:  "ok",
		Output:  "hello",
		Payload: pb,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out CommandResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(out.Payload) != string(pb) {
		t.Errorf("Payload round-trip mismatch: %s vs %s", out.Payload, pb)
	}
}

func TestCommandResult_OmitsEmptyPayload(t *testing.T) {
	in := CommandResult{ID: "x", Status: "ok"}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "payload") {
		t.Errorf("nil payload should be omitted, got %s", b)
	}
}
