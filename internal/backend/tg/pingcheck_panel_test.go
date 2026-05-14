package tg

import (
	"strings"
	"testing"
)

func TestPingCheckPanelText_Empty(t *testing.T) {
	got := PingCheckPanelText("router1", true, nil)
	if !strings.Contains(got, "Туннелей не обнаружено") {
		t.Errorf("expected empty-state, got: %s", got)
	}
}

func TestPingCheckPanelText_AliveAndDead(t *testing.T) {
	entries := []PingCheckPanelEntry{
		{TunnelID: "awg10", Name: "amst", Status: "alive", PerTunnelEnabled: true, LastLatencyMs: 82, SuccessCount: 417, FailCount: 0, FailThreshold: 3, RestartCount: 0},
		{TunnelID: "awg11", Name: "fra", Status: "dead", PerTunnelEnabled: true, LastLatencyMs: 0, SuccessCount: 5, FailCount: 3, FailThreshold: 3, RestartCount: 7},
	}
	got := PingCheckPanelText("router1", true, entries)
	for _, want := range []string{"router1", "🟢", "🔴", "amst", "fra", "82ms", "---", "✓417", "✓5", "✗0/3", "✗3/3", "restart×0", "restart×7", "⚠"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPingCheckPanelText_PerTunnelDisabled(t *testing.T) {
	entries := []PingCheckPanelEntry{
		{TunnelID: "awg10", Name: "amst", Status: "alive", PerTunnelEnabled: false, LastLatencyMs: 0},
	}
	got := PingCheckPanelText("router1", true, entries)
	if !strings.Contains(got, "⏸") {
		t.Errorf("disabled tunnel must use ⏸: %s", got)
	}
}

func TestPingCheckPanelText_GloballyDisabled(t *testing.T) {
	entries := []PingCheckPanelEntry{{TunnelID: "awg10", Name: "amst", Status: "alive", PerTunnelEnabled: true, LastLatencyMs: 50}}
	got := PingCheckPanelText("router1", false, entries)
	if !strings.Contains(got, "Глобально: ⏸") {
		t.Errorf("must show global disabled banner: %s", got)
	}
}

func TestPingCheckPanelText_LongSuccessCount(t *testing.T) {
	entries := []PingCheckPanelEntry{{TunnelID: "awg10", Name: "amst", Status: "alive", PerTunnelEnabled: true, LastLatencyMs: 50, SuccessCount: 12500}}
	got := PingCheckPanelText("router1", true, entries)
	if !strings.Contains(got, "✓12.5k") {
		t.Errorf("expected k-suffix for >9999, got: %s", got)
	}
}
