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
	if strings.Contains(text, "        ") {
		t.Errorf("routes text should not rely on wide space padding:\n%s", text)
	}
	for _, want := range []string{
		"DNS routes: 6 правил",
		"Static IP routes: 3 правил",
		"amnezia (nwg1): 7 правил",
		"DNS routes: 1 правил ← WAN/system",
		"Что видно:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
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

func TestRoutesPanelKeyboard_CanRebindWANSystemDNS(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true},
			{ID: "eth3", Name: "Ethernet", Iface: "eth3", Type: "wan", Enabled: true},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1":   {},
			"eth3": {},
		},
		Other: wire.TunnelCounts{DNS: 10},
	}
	kb := RoutesPanelKeyboard(42, snap)
	if !routesKeyboardHasCallback(kb, "routes_rebind:42:__other__") {
		t.Fatalf("WAN/system DNS rebind button missing: %+v", kb.InlineKeyboard)
	}
	if !routesKeyboardHasText(kb, "DNS routes") {
		t.Fatalf("WAN/system DNS source should be labelled as DNS routes: %+v", kb.InlineKeyboard)
	}
}

func TestRebindPickKeyboard_AllowsWANSystemSource(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true},
			{ID: "eth3", Name: "Ethernet", Iface: "eth3", Type: "wan", Enabled: true},
		},
		Other: wire.TunnelCounts{DNS: 10},
	}
	text, kb := RebindPickKeyboard(42, "__other__", snap)
	if !strings.Contains(text, "DNS routes") || !strings.Contains(text, "WAN/system") {
		t.Fatalf("source label missing: %s", text)
	}
	if !routesKeyboardHasCallback(kb, "routes_pick:42:__other__:t1") {
		t.Fatalf("managed destination missing: %+v", kb.InlineKeyboard)
	}
}

func TestRoutesPanelKeyboard_HasDoctorAndSnapshot(t *testing.T) {
	snap := wire.RouteSnapshot{
		HRNeo:   wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{{ID: "t1", Name: "amnezia", Iface: "nwg1"}},
		Counts:  map[string]wire.TunnelCounts{"t1": {DNS: 1}},
	}
	kb := RoutesPanelKeyboard(42, snap)
	for _, want := range []string{"routes_hrneo_doctor:42:_panel_", "routes_snapshot:42:_panel_"} {
		if !routesKeyboardHasCallback(kb, want) {
			t.Errorf("missing callback %q in %+v", want, kb)
		}
	}
}

func TestRebindResultKeyboard_OffersTunnelCheck(t *testing.T) {
	kb := RebindResultKeyboard(42, "old", "new", 0)
	if !routesKeyboardHasCallback(kb, "tunnels_refresh:42:_panel_") {
		t.Fatalf("rebind result should offer tunnel check next-action: %+v", kb.InlineKeyboard)
	}
}

func TestRouteExplainText_DomainSuffixMatchesHRNeo(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{{ID: "awg1", Name: "amnezia", Iface: "nwg1"}},
		Rules: []wire.RouteRuleSummary{{
			ID: "r1", Name: "YouTube", Kind: "dns", Backend: "hydraroute", Enabled: true, Bind: "nwg1",
			Targets: []wire.RouteTarget{{Type: "domain", Value: "youtube.com"}},
		}},
	}
	text := RouteExplainText("testkeen", "music.youtube.com", snap)
	for _, want := range []string{"music.youtube.com", "YouTube", "HR-Neo", "amnezia", "nwg1"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestRouteExplainText_IPMatchesStaticCIDR(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{{ID: "awg1", Name: "corp", Iface: "nwg2"}},
		Rules: []wire.RouteRuleSummary{{
			ID: "s1", Name: "CorpNet", Kind: "static", Enabled: true, Bind: "nwg2",
			Targets: []wire.RouteTarget{{Type: "cidr", Value: "10.10.0.0/16"}},
		}},
	}
	text := RouteExplainText("testkeen", "10.10.4.5", snap)
	for _, want := range []string{"10.10.4.5", "CorpNet", "Static", "corp"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestRouteSnapshotText_IncludesStableSummary(t *testing.T) {
	snap := wire.RouteSnapshot{
		HRNeo:   wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{{ID: "awg1", Name: "amnezia", Iface: "nwg1"}},
		Counts:  map[string]wire.TunnelCounts{"awg1": {DNS: 2, Static: 1, HRNeo: 2}},
		Other:   wire.TunnelCounts{DNS: 1},
		Rules:   []wire.RouteRuleSummary{{ID: "r1", Name: "YouTube", Kind: "dns"}},
	}
	text := RouteSnapshotText("testkeen", snap)
	for _, want := range []string{"Снапшот маршрутов", "testkeen", "HR-Neo: установлен и работает", "правил всего: 1", "amnezia"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "installed/running") {
		t.Errorf("technical HR-Neo state should not leak:\n%s", text)
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

func routesKeyboardHasCallback(kb InlineKeyboardMarkup, want string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == want {
				return true
			}
		}
	}
	return false
}

func routesKeyboardHasText(kb InlineKeyboardMarkup, want string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.Text, want) {
				return true
			}
		}
	}
	return false
}
