package actions

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

func loadLiveFixture[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile("../awgmgr/testdata/live-2172/" + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	var env awgmgr.Envelope[T]
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return env.Data
}

// Снимок живого роутера 2.17.2: 28 правил, все hrRouteMode="policy",
// hrPolicyInterfaces пуст у всех. Единственный источник привязки -- политики.
func TestBuildRouteSnapshot_LivePolicyModel(t *testing.T) {
	tunnels := loadLiveFixture[awgmgr.TunnelsAll](t, "tunnels-all.json")
	routing := loadLiveFixture[[]awgmgr.RoutingTunnel](t, "routing-tunnels.json")
	dns := loadLiveFixture[[]awgmgr.DNSRoute](t, "dns-routes-list.json")
	policies := loadLiveFixture[[]awgmgr.AccessPolicy](t, "access-policies.json")
	polIfaces := loadLiveFixture[[]awgmgr.PolicyInterface](t, "policy-interfaces.json")

	snap := buildRouteSnapshot(nil, &tunnels, routing, dns, nil, "direct", policies, polIfaces, false)

	if !snap.PolicyModel {
		t.Error("snapshot must flag that the agent read access policies")
	}

	byName := map[string]int{}
	for i, p := range snap.Policies {
		byName[p.Name] = i
	}
	hydra, ok := byName["HydraRoute"]
	if !ok {
		t.Fatalf("HydraRoute policy missing: %+v", snap.Policies)
	}
	if got := snap.Policies[hydra].DNS; got != 26 {
		t.Errorf("HydraRoute DNS = %d, want 26", got)
	}
	if got := snap.Policies[hydra].ActiveTunnelID; got != "awg11" {
		t.Errorf("HydraRoute active tunnel = %q, want awg11", got)
	}
	if !snap.Policies[hydra].ViaVPN {
		t.Error("HydraRoute must be marked as going through a tunnel")
	}
	chain := snap.Policies[hydra].Interfaces
	if len(chain) != 3 || chain[0].Role != "active" || chain[1].Role != "unavailable" || chain[2].Role != "unavailable" {
		t.Errorf("chain roles = %+v", chain)
	}
	if chain[0].TunnelID != "awg11" || chain[1].TunnelID != "awg20" || chain[2].TunnelID != "awg10" {
		t.Errorf("chain tunnels = %+v", chain)
	}

	ru, ok := byName["RU"]
	if !ok {
		t.Fatalf("RU policy missing")
	}
	if snap.Policies[ru].DNS != 2 {
		t.Errorf("RU DNS = %d, want 2", snap.Policies[ru].DNS)
	}
	// RU выходит через GigabitEthernet1 -- это WAN, не туннель.
	if snap.Policies[ru].ActiveTunnelID != "" || snap.Policies[ru].ViaVPN {
		t.Errorf("RU must be marked as bypassing VPN: %+v", snap.Policies[ru])
	}

	// Ни одно правило не должно осесть в "неизвестно": привязка известна вся.
	if snap.Other.DNS != 0 || snap.Other.Static != 0 {
		t.Errorf("Other = %+v, want zero", snap.Other)
	}
	// Правила политики учитываются только в Policies -- панель бота складывает
	// Counts и Policies в общий итог, и двойной учёт удвоил бы цифру.
	for id, c := range snap.Counts {
		if c.DNS != 0 {
			t.Errorf("Counts[%s].DNS = %d, want 0 (policy rules live in Policies)", id, c.DNS)
		}
	}
}

func TestBuildRouteSnapshot_PolicyChainSortedByOrder(t *testing.T) {
	policies := []awgmgr.AccessPolicy{{
		Name: "HydraRoute",
		// Роутер волен отдать цепочку в любом порядке; приоритет несёт order.
		Interfaces: []awgmgr.AccessPolicyInterface{
			{Name: "OpkgTun10", Label: "second", Order: 2},
			{Name: "OpkgTun11", Label: "first", Order: 0},
		},
	}}
	polIfaces := []awgmgr.PolicyInterface{
		{Name: "OpkgTun10", Up: true},
		{Name: "OpkgTun11", Up: true},
	}
	snap := buildRouteSnapshot(nil, nil, nil, nil, nil, "", policies, polIfaces, false)
	chain := snap.Policies[0].Interfaces
	if len(chain) != 2 || chain[0].Bind != "OpkgTun11" || chain[0].Role != "active" {
		t.Fatalf("chain = %+v", chain)
	}
	if chain[1].Role != "fallback" {
		t.Errorf("second link role = %q, want fallback", chain[1].Role)
	}
}

// Правило, созданное мастером импорта (Task 10), приколочено к интерфейсу, а
// не к политике: снимок обязан прочесть его обратно и отнести туннелю.
func TestBuildRouteSnapshot_InterfaceModeRuleCreditedToItsTunnel(t *testing.T) {
	tunnels := awgmgr.TunnelsAll{Tunnels: []awgmgr.Tunnel{
		{ID: "awg20", Name: "NetherlandsKerkradeS24", InterfaceName: "nwg0", NDMSName: "Wireguard0", Enabled: true},
	}}
	dns := []awgmgr.DNSRoute{{
		ID: "hr:Streaming", Name: "Streaming", Enabled: true, Backend: "hydraroute",
		HRRouteMode: "interface", HRPolicyInterfaces: []string{"Wireguard0"},
	}}
	policies := []awgmgr.AccessPolicy{{
		Name:       "HydraRoute",
		Interfaces: []awgmgr.AccessPolicyInterface{{Name: "OpkgTun11", Order: 0}},
	}}
	polIfaces := []awgmgr.PolicyInterface{{Name: "OpkgTun11", Up: true}, {Name: "Wireguard0", Up: true}}

	snap := buildRouteSnapshot(nil, &tunnels, nil, dns, nil, "", policies, polIfaces, false)
	if snap.Counts["awg20"].DNS != 1 {
		t.Errorf("Counts[awg20] = %+v, want DNS=1", snap.Counts["awg20"])
	}
	if snap.Policies[0].DNS != 0 {
		t.Errorf("interface-mode rule must not be credited to the policy: %+v", snap.Policies[0])
	}
}

func TestBuildRouteSnapshot_PolicyWithNothingUpHasNoActive(t *testing.T) {
	policies := []awgmgr.AccessPolicy{{
		Name:       "HydraRoute",
		Interfaces: []awgmgr.AccessPolicyInterface{{Name: "OpkgTun10", Order: 0}},
	}}
	polIfaces := []awgmgr.PolicyInterface{{Name: "OpkgTun10", Up: false}}
	snap := buildRouteSnapshot(nil, nil, nil, nil, nil, "", policies, polIfaces, false)
	if snap.Policies[0].ActiveTunnelID != "" || snap.Policies[0].ViaVPN {
		t.Errorf("policy with a dead chain must claim no active egress: %+v", snap.Policies[0])
	}
	if snap.Policies[0].Interfaces[0].Role != "unavailable" {
		t.Errorf("role = %q", snap.Policies[0].Interfaces[0].Role)
	}
}

// Роутер без /api/routing/access-policies (2.8.x и кастомные сборки): привязка
// живёт в самом правиле. Поведение обязано остаться ровно таким, как до фазы B.
func TestBuildRouteSnapshot_LegacyModelUnchanged(t *testing.T) {
	tunnels := awgmgr.TunnelsAll{Tunnels: []awgmgr.Tunnel{
		{ID: "awg11", Name: "amst", InterfaceName: "nwg1", NDMSName: "Wireguard1", Enabled: true, DefaultRoute: true},
		{ID: "awg12", Name: "fra", InterfaceName: "nwg0", NDMSName: "Wireguard0", Enabled: true},
	}}
	dns := []awgmgr.DNSRoute{
		{ID: "hr:A", Name: "A", Enabled: true, Backend: "hydraroute", HRRouteMode: "policy",
			HRPolicyName: "HydraRoute", HRPolicyInterfaces: []string{"nwg1", "nwg0"}},
		{ID: "hr:B", Name: "B", Enabled: true, Backend: "hydraroute",
			Routes: []awgmgr.DNSRouteEntry{{Interface: "nwg0", TunnelID: "nwg0"}}},
	}

	// Политик нет -- ровно это и означает старую модель.
	snap := buildRouteSnapshot(nil, &tunnels, nil, dns, nil, "awg11", nil, nil, false)

	if len(snap.Policies) != 1 || snap.Policies[0].Name != "HydraRoute" || snap.Policies[0].DNS != 1 {
		t.Fatalf("legacy policy summary = %+v", snap.Policies)
	}
	// Роли по-прежнему считаются из доступности туннелей, а не из up/down.
	if snap.Policies[0].Interfaces[0].Role != "active" {
		t.Errorf("legacy roles = %+v", snap.Policies[0].Interfaces)
	}
	// Правило с явной привязкой по-прежнему кредитуется туннелю.
	if snap.Counts["awg12"].DNS != 1 {
		t.Errorf("Counts[awg12] = %+v, want DNS=1", snap.Counts["awg12"])
	}
	// Новые поля старая ветка не заполняет -- и не должна.
	if snap.Policies[0].ActiveTunnelID != "" {
		t.Errorf("legacy branch must not claim an active tunnel: %q", snap.Policies[0].ActiveTunnelID)
	}
	if snap.PolicyModel {
		t.Error("legacy snapshot (policies == nil) must not claim to have read policies")
	}
}

func TestBuildRouteSnapshot_RestartMethodTellsScreenWhatIsPossible(t *testing.T) {
	tunnels := loadLiveFixture[awgmgr.TunnelsAll](t, "tunnels-all.json")
	routing := loadLiveFixture[[]awgmgr.RoutingTunnel](t, "routing-tunnels.json")
	snap := buildRouteSnapshot(nil, &tunnels, routing, nil, nil, "direct", nil, nil, false)
	got := map[string]string{}
	for _, t := range snap.Tunnels {
		got[t.ID] = t.RestartMethod
	}
	// opkg-туннель перезапускается по id -- кнопка предлагается.
	if got["awg11"] != "control" {
		t.Errorf("awg11 restart_method = %q, want control", got["awg11"])
	}
	// WAN и системные интерфейсы перезапуску не подлежат -- кнопки быть не должно.
	// Ключ -- "eth3" (iface), а не сырой routing id "wan:eth3": ndmsRouteEndpoint
	// в route_targets.go всегда использует iface как ID, когда он не пуст -- так
	// же, как во всех соседних тестах пакета (route_ifaces_test.go,
	// route_status_test.go, route_rebind_test.go).
	if got["eth3"] != "none" {
		t.Errorf("eth3 restart_method = %q, want none", got["eth3"])
	}
}
