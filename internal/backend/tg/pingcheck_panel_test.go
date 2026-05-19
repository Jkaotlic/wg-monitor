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
	if !strings.Contains(got, "Мониторинг: ⏸") {
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

func TestPingCheckPanelKeyboard_Layout(t *testing.T) {
	entries := []PingCheckPanelEntry{
		{TunnelID: "awg10", Name: "amst", NDMSName: "Wireguard0", PerTunnelEnabled: true},
		{TunnelID: "awg11", Name: "fra", NDMSName: "Wireguard1", PerTunnelEnabled: false},
	}
	kb := PingCheckPanelKeyboard(42, entries)
	if len(kb.InlineKeyboard) < 3 {
		t.Fatalf("expected at least 3 rows, got %d", len(kb.InlineKeyboard))
	}
	// Row 0: per-tunnel toggles
	row0 := kb.InlineKeyboard[0]
	if len(row0) != 2 {
		t.Errorf("toggle row should have 2 buttons, got %d", len(row0))
	}
	// awg10 is enabled → button shows ⏸ (would disable on tap)
	if !strings.Contains(row0[0].Text, "⏸") {
		t.Errorf("enabled tunnel should show ⏸ to disable, got %q", row0[0].Text)
	}
	// awg11 is disabled → button shows ▶ (would enable on tap)
	if !strings.Contains(row0[1].Text, "▶") {
		t.Errorf("disabled tunnel should show ▶ to enable, got %q", row0[1].Text)
	}
	// Callback data shape: pingcheck_toggle:42:awg10:Wireguard0:0
	wantCB0 := "pingcheck_toggle:42:awg10:Wireguard0:0"
	if row0[0].CallbackData != wantCB0 {
		t.Errorf("toggle cb mismatch: got %q want %q", row0[0].CallbackData, wantCB0)
	}
	wantCB1 := "pingcheck_toggle:42:awg11:Wireguard1:1"
	if row0[1].CallbackData != wantCB1 {
		t.Errorf("toggle cb mismatch: got %q want %q", row0[1].CallbackData, wantCB1)
	}
}

func TestPingCheckPanelKeyboard_GlobalControls(t *testing.T) {
	kb := PingCheckPanelKeyboard(42, nil)
	// Even with no tunnels, global controls + close should appear.
	flat := []string{}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			flat = append(flat, b.CallbackData)
		}
	}
	for _, want := range []string{"pingcheck_now:42:_menu", "pingcheck_open:42:_panel_", "panel:0:help:pingcheck", "routes_close:42:_panel_"} {
		found := false
		for _, c := range flat {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing callback_data %q; got %v", want, flat)
		}
	}
}
