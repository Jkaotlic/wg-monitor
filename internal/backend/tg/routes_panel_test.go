package tg

import (
	"strings"
	"testing"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRoutesPanelText_HappyPath(t *testing.T) {
	snap := wire.RouteSnapshot{
		HRNeo: wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true, DefaultRoute: true},
			{ID: "t2", Name: "amnezia2", Iface: "nwg0", Enabled: true},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {DNS: 5, Static: 2, HRNeo: 4},
			"t2": {},
		},
		Other: wire.TunnelCounts{DNS: 1, Static: 1, HRNeo: 1},
	}
	text := RoutesPanelText("testkeen", snap)
	if !strings.Contains(text, "testkeen") {
		t.Errorf("router name missing: %s", text)
	}
	// total DNS shown = 5 + 1 (other) = 6
	if !strings.Contains(text, "6") {
		t.Errorf("DNS total missing: %s", text)
	}
	// t1 row: visible total = DNS + Static = 7 (HRNeo not added)
	if !strings.Contains(text, "amnezia") || !strings.Contains(text, "7") {
		t.Errorf("t1 row missing or wrong total: %s", text)
	}
	if !strings.Contains(text, "WAN") {
		t.Errorf("WAN/Other not shown: %s", text)
	}
}

func TestRoutesPanelText_HRNeoAbsent(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true}},
		Counts:  map[string]wire.TunnelCounts{"t1": {DNS: 1}},
	}
	text := RoutesPanelText("testkeen", snap)
	if strings.Contains(text, "HydraRoute Neo:") {
		t.Errorf("HR-Neo line should be hidden: %s", text)
	}
}

func TestRoutesPanelKeyboard_RebindOnlyForNonZero(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true},
			{ID: "t2", Name: "newtun", Iface: "nwg0", Enabled: true},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {DNS: 3},
			"t2": {},
		},
	}
	kb := RoutesPanelKeyboard(42, snap)
	hasT1 := false
	hasT2 := false
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.CallbackData, "routes_rebind:42:t1") {
				hasT1 = true
			}
			if strings.Contains(btn.CallbackData, "routes_rebind:42:t2") {
				hasT2 = true
			}
		}
	}
	if !hasT1 {
		t.Errorf("t1 (3 rules) should have rebind button")
	}
	if hasT2 {
		t.Errorf("t2 (0 rules) should NOT have rebind button")
	}
}

func TestRebindPreviewText_ShowsUntouchedBlock(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1"},
			{ID: "t2", Name: "newtun", Iface: "nwg0"},
			{ID: "t3", Name: "third", Iface: "nwg2"},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {DNS: 5, Static: 2, HRNeo: 1},
			"t3": {DNS: 4},
		},
		Other: wire.TunnelCounts{DNS: 12},
	}
	text := RebindPreviewText(snap, "t1", "t2", "8a3f")
	if !strings.Contains(text, "5") || !strings.Contains(text, "2") {
		t.Errorf("preview missing per-category counts: %s", text)
	}
	if !strings.Contains(text, "WAN") || !strings.Contains(text, "12") {
		t.Errorf("untouched WAN block missing: %s", text)
	}
	if !strings.Contains(text, "third") || !strings.Contains(text, "4") {
		t.Errorf("untouched other-tunnel row missing: %s", text)
	}
	if !strings.Contains(text, "8a3f") {
		t.Errorf("token must be in preview: %s", text)
	}
}
