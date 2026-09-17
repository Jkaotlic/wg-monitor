package backend

import (
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Формула обязана совпадать с экраном (miniapp/src/routes.js, tunnelRows):
// собственные правила туннеля плюс правила политики, которую он несёт
// сейчас. Разойдись они -- экран скажет «правил нет», а сервер откажет.
func TestMiniappTunnelRuleCountPolicyModel(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "awg12", Name: "vpn-nl", Iface: "nwg12", Type: "managed"},
			{ID: "awg10", Name: "vpn-de", Iface: "nwg10", Type: "managed"},
			{ID: "awg14", Name: "vpn-spare", Iface: "nwg14", Type: "managed"},
		},
		Counts: map[string]wire.TunnelCounts{"awg12": {DNS: 2, Static: 1, HRNeo: 1}},
		Policies: []wire.RoutePolicySummary{
			{Name: "HydraRoute", DNS: 5, HRNeo: 4, ActiveTunnelID: "awg10",
				Interfaces: []wire.RoutePolicyInterface{
					{Bind: "nwg10", TunnelID: "awg10", Role: "active"},
					// Резервное звено цепочки правил не несёт: считать их и ему --
					// значит умножать одни и те же правила.
					{Bind: "nwg14", TunnelID: "awg14", Role: "fallback"},
				}},
		},
		PolicyModel: true,
	}
	cases := map[string]miniappTunnelRules{
		"awg12": {Total: 3, DNS: 2, Static: 1, HRNeo: 1},
		"awg10": {Total: 5, HRNeo: 4, ViaPolicy: 5},
		"awg14": {},
		"nope":  {},
	}
	for id, want := range cases {
		if got := miniappTunnelRuleCount(snap, id); got != want {
			t.Errorf("%s: %+v, want %+v", id, got, want)
		}
	}
}

// Снимок старого агента: active_tunnel_id нет, звенья сопоставляются по
// iface и имени -- неточно, но так же, как на экране.
func TestMiniappTunnelRuleCountLegacySnapshot(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "awg12", Name: "vpn-nl", Iface: "nwg12", Type: "managed"},
			{ID: "awg10", Name: "vpn-de", Iface: "nwg10", Type: "managed"},
		},
		Policies: []wire.RoutePolicySummary{
			{Name: "HydraRoute", DNS: 7, Interfaces: []wire.RoutePolicyInterface{
				{Bind: "NWG12"},
				{Name: "vpn-de"},
				{Bind: "nwg12"}, // дубль того же туннеля не удваивает
			}},
		},
	}
	if got := miniappTunnelRuleCount(snap, "awg12"); got.ViaPolicy != 7 || got.Total != 7 {
		t.Errorf("awg12: %+v", got)
	}
	if got := miniappTunnelRuleCount(snap, "awg10"); got.ViaPolicy != 7 {
		t.Errorf("awg10: %+v", got)
	}
}

func TestMiniappRulesCountWords(t *testing.T) {
	for n, want := range map[int]string{1: "1 правило", 2: "2 правила", 4: "4 правила", 5: "5 правил", 11: "11 правил", 12: "12 правил", 21: "21 правило", 22: "22 правила", 111: "111 правил"} {
		if got := miniappRulesCount(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}

func TestMiniappTunnelTextsAreRussian(t *testing.T) {
	for code, text := range miniappTunnelTexts {
		if !strings.ContainsAny(text, "абвгдеёжзийклмнопрстуфхцчшщъыьэюяАБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯ") {
			t.Errorf("%s: текст не по-русски: %q", code, text)
		}
	}
	for _, code := range []string{"not_found", "bad_json", "internal"} {
		if miniappTunnelErrorText(code) != miniappCabinetErrorText(code) {
			t.Errorf("%s: общий код обязан брать текст кабинетов", code)
		}
	}
}
