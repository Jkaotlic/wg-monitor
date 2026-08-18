package tg

import (
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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

func TestRoutesPanelTextShowsHRNeoPolicyActiveAndFallback(t *testing.T) {
	snap := wire.RouteSnapshot{
		HRNeo: wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "NetherlandsAmsterdamH17", Iface: "nwg3", Enabled: true},
			{ID: "t2", Name: "amnezia_for_awg-nktelecom", Iface: "nwg0", Enabled: true},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {},
			"t2": {},
		},
		Policies: []wire.RoutePolicySummary{{
			Name:  "HydraRoute",
			DNS:   25,
			HRNeo: 25,
			Interfaces: []wire.RoutePolicyInterface{
				{Name: "NetherlandsAmsterdamH17", Bind: "nwg3", Role: "active", Available: true},
				{Name: "amnezia_for_awg-nktelecom", Bind: "nwg0", Role: "fallback", Available: true},
			},
		}},
	}

	text := RoutesPanelText("testkeen", snap)
	for _, want := range []string{
		"DNS routes: 25",
		"HR-Neo: 25",
		"NetherlandsAmsterdamH17 (nwg3): 0",
		"amnezia_for_awg-nktelecom (nwg0): 0",
		"HydraRoute: 25",
		"NetherlandsAmsterdamH17 (nwg3) [сейчас]",
		"amnezia_for_awg-nktelecom (nwg0) [fallback]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestRoutesPanelKeyboard_CanRebindHRNeoPolicyInterface(t *testing.T) {
	snap := wire.RouteSnapshot{
		HRNeo: wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "policy-primary", Iface: "nwg3", Enabled: true},
			{ID: "t2", Name: "policy-dst", Iface: "nwg0", Enabled: true},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {},
			"t2": {},
		},
		Policies: []wire.RoutePolicySummary{{
			Name:  "HydraRoute",
			DNS:   25,
			HRNeo: 25,
			Interfaces: []wire.RoutePolicyInterface{
				{Name: "policy-primary", Bind: "nwg3", Role: "active", Available: true},
				{Name: "policy-dst", Bind: "nwg0", Role: "fallback", Available: true},
			},
		}},
	}

	kb := RoutesPanelKeyboard(42, snap)
	if !routesKeyboardHasCallback(kb, "routes_rebind:42:t1") {
		t.Fatalf("HR-Neo policy source should have a rebind button: %+v", kb.InlineKeyboard)
	}
}

func TestRebindPreviewText_CountsHRNeoPolicyInterfaceMoves(t *testing.T) {
	snap := wire.RouteSnapshot{
		HRNeo: wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "policy-primary", Iface: "nwg3", Enabled: true},
			{ID: "t2", Name: "policy-dst", Iface: "nwg0", Enabled: true},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {},
			"t2": {},
		},
		Policies: []wire.RoutePolicySummary{{
			Name:       "HydraRoute",
			DNS:        25,
			HRNeo:      25,
			Interfaces: []wire.RoutePolicyInterface{{Name: "policy-primary", Bind: "nwg3", Role: "active", Available: true}},
		}},
	}

	text := RebindPreviewText(snap, "t1", "t2", "tok")
	if !strings.Contains(text, "25") || !strings.Contains(text, "HR-Neo") {
		t.Fatalf("preview should count HR-Neo policy routes, got:\n%s", text)
	}
}

func TestRoutesPanelTextShowsSnapshotWarnings(t *testing.T) {
	text := RoutesPanelText("testkeen", wire.RouteSnapshot{
		Warnings: []string{"/api/routing/tunnels failed: HTTP 502"},
	})
	if !strings.Contains(text, "неполный") || !strings.Contains(text, "/api/routing/tunnels") {
		t.Fatalf("route warnings should be visible in panel:\n%s", text)
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

func TestRoutesPanelKeyboard_CloseIsRouterScoped(t *testing.T) {
	kb := RoutesPanelKeyboard(42, wire.RouteSnapshot{})
	if !routesKeyboardHasCallback(kb, "routes_close:42:_panel_") {
		t.Fatalf("routes panel close must carry router user id: %+v", kb.InlineKeyboard)
	}
	if routesKeyboardHasCallback(kb, "routes_close:0:_panel_") {
		t.Fatalf("routes panel close must not be globally closable: %+v", kb.InlineKeyboard)
	}
}

func TestRebindResultKeyboard_OffersTunnelCheck(t *testing.T) {
	kb := RebindResultKeyboard(42, "old", "new", 0)
	for _, want := range []string{
		"tunnels_refresh:42:_panel_",
		"pingcheck_open:42:_panel_",
		"router_doctor:42:_menu",
	} {
		if !routesKeyboardHasCallback(kb, want) {
			t.Fatalf("rebind result should offer post-route verification %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestRebindResultKeyboard_OffersRouteVerifyAndRollback(t *testing.T) {
	kb := RebindResultKeyboard(42, "old", "new", 0)
	for _, want := range []string{
		"routes_refresh:42:_panel_",
		"routes_rollback:42:old:new",
		"check_via_tunnel:42:_panel_",
	} {
		if !routesKeyboardHasCallback(kb, want) {
			t.Fatalf("rebind result should offer %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestRebindResultKeyboard_PartialFailRefreshesFreshRoutes(t *testing.T) {
	kb := RebindResultKeyboard(42, "old", "new", 1)
	if !routesKeyboardHasCallback(kb, "routes_refresh:42:_panel_") {
		t.Fatalf("partial rebind result should refresh routes before retry: %+v", kb.InlineKeyboard)
	}
	if routesKeyboardHasCallback(kb, "routes_pick:42:old:new") {
		t.Fatalf("partial rebind result must not use stale route cache retry: %+v", kb.InlineKeyboard)
	}
}

func TestRebindResultKeyboard_CloseIsRouterScoped(t *testing.T) {
	kb := RebindResultKeyboard(42, "old", "new", 1)
	if !routesKeyboardHasCallback(kb, "routes_close:42:_panel_") {
		t.Fatalf("rebind result close must carry router user id: %+v", kb.InlineKeyboard)
	}
	if routesKeyboardHasCallback(kb, "routes_close:0:_panel_") {
		t.Fatalf("rebind result close must not be globally closable: %+v", kb.InlineKeyboard)
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
		Rules: []wire.RouteRuleSummary{
			{ID: "r1", Name: "YouTube", Kind: "dns", Enabled: true},
			{ID: "ndms:old", Name: "Old NDMS", Kind: "dns", Backend: "ndms", Enabled: false},
		},
	}
	text := RouteSnapshotText("testkeen", snap)
	for _, want := range []string{"Снапшот маршрутов", "testkeen", "HR-Neo: установлен и работает", "правил активных: 1", "выключено: 1", "amnezia"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "правил всего: 2") {
		t.Errorf("disabled rules must not look active in summary:\n%s", text)
	}
	if strings.Contains(text, "installed/running") {
		t.Errorf("technical HR-Neo state should not leak:\n%s", text)
	}
}

func TestRouteSnapshotText_IncludesHRNeoPolicyTotals(t *testing.T) {
	snap := wire.RouteSnapshot{
		HRNeo:  wire.HRStatus{Installed: true, Running: true},
		Counts: map[string]wire.TunnelCounts{},
		Policies: []wire.RoutePolicySummary{{
			Name:  "HydraRoute",
			DNS:   25,
			HRNeo: 25,
			Interfaces: []wire.RoutePolicyInterface{
				{Name: "primary", Bind: "nwg3", Role: "active", Available: true},
				{Name: "fallback", Bind: "nwg0", Role: "fallback", Available: true},
			},
		}},
	}

	text := RouteSnapshotText("testkeen", snap)
	for _, want := range []string{"DNS: 25, static: 0, HR-Neo: 25", "HydraRoute", "primary", "fallback"} {
		if !strings.Contains(text, want) {
			t.Fatalf("snapshot text missing %q:\n%s", want, text)
		}
	}
}

func TestRebindResultText_ShowsCategoryErrorsWhenFailedCountIsZero(t *testing.T) {
	text := RebindResultText("old", "new", wire.RouteRebindResult{
		DNS:    wire.CategoryResult{OK: 1},
		Static: wire.CategoryResult{Errors: []string{"routing/refresh: boom"}},
	})

	if !strings.Contains(text, "частично") || !strings.Contains(text, "routing/refresh: boom") {
		t.Fatalf("maintenance errors must render as partial:\n%s", text)
	}
	if strings.Contains(text, "готово") {
		t.Fatalf("maintenance errors must not render clean success:\n%s", text)
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

func TestRoutesPanelText_PolicyRulesCreditedOnceToActiveTunnel(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "awg11", Name: "awg3-work-via-ru1", Iface: "opkgtun11"},
			{ID: "awg20", Name: "NetherlandsKerkradeS24", Iface: "nwg0"},
		},
		Counts: map[string]wire.TunnelCounts{},
		Policies: []wire.RoutePolicySummary{{
			Name: "HydraRoute", DNS: 26, HRNeo: 26, ActiveTunnelID: "awg11", ViaVPN: true,
			Interfaces: []wire.RoutePolicyInterface{
				{Bind: "OpkgTun11", Name: "awg3-work-via-ru1", Role: "active", Available: true, TunnelID: "awg11", ViaVPN: true},
				{Bind: "Wireguard0", Name: "NetherlandsKerkradeS24", Role: "unavailable", Order: 1, TunnelID: "awg20"},
			},
		}},
	}
	got := RoutesPanelText("client-b", snap)
	// Раньше 26 правил приписывались каждому звену цепочки.
	if !strings.Contains(got, "awg3-work-via-ru1 (opkgtun11): 0 правил (+26 HR-Neo policy)") {
		t.Errorf("active tunnel line missing:\n%s", got)
	}
	if strings.Contains(got, "NetherlandsKerkradeS24 (nwg0): 0 правил (+26") {
		t.Errorf("fallback tunnel must not be credited:\n%s", got)
	}
}

func TestRoutesPanelText_MarksPolicyBypassingVPN(t *testing.T) {
	snap := wire.RouteSnapshot{
		Counts: map[string]wire.TunnelCounts{},
		Policies: []wire.RoutePolicySummary{{
			Name: "RU", DNS: 2, HRNeo: 2,
			Interfaces: []wire.RoutePolicyInterface{
				{Bind: "GigabitEthernet1", Name: "Подключение Ethernet", Role: "active", Available: true},
			},
		}, {
			Name: "HydraRoute", DNS: 26, HRNeo: 26, ActiveTunnelID: "awg11", ViaVPN: true,
			Interfaces: []wire.RoutePolicyInterface{
				{Bind: "OpkgTun11", Role: "active", Available: true, TunnelID: "awg11", ViaVPN: true},
			},
		}},
	}
	got := RoutesPanelText("client-b", snap)
	if !strings.Contains(got, "мимо VPN") {
		t.Errorf("RU must be marked as bypassing VPN:\n%s", got)
	}
	if strings.Count(got, "мимо VPN") != 1 {
		t.Errorf("only RU must carry the mark:\n%s", got)
	}
}

func TestRoutesPanelText_OldAgentKeepsLegacyAttribution(t *testing.T) {
	// Снимок старого агента: ни ActiveTunnelID, ни TunnelID в звеньях.
	// Приписывание должно остаться прежним, а метки "мимо VPN" не появиться.
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{{ID: "awg11", Name: "amst", Iface: "nwg1"}},
		Counts:  map[string]wire.TunnelCounts{},
		Policies: []wire.RoutePolicySummary{{
			Name: "HydraRoute", DNS: 7, HRNeo: 7,
			Interfaces: []wire.RoutePolicyInterface{{Bind: "nwg1", Name: "amst", Role: "active", Available: true}},
		}},
	}
	got := RoutesPanelText("client-b", snap)
	if !strings.Contains(got, "(+7 HR-Neo policy)") {
		t.Errorf("legacy attribution lost:\n%s", got)
	}
	if strings.Contains(got, "мимо VPN") {
		t.Errorf("old agent snapshot must not be labelled:\n%s", got)
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
