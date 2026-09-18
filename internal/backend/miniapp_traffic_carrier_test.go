package backend

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Форма workrouter 18.09.2026: набор HydraRoute идёт через awg14 (hipvps,
// жив), запасное звено awg10 (nl2) мертво -- обмен ключами 24 минуты, а
// интерфейс поднят. Главный выход напрямую. Экран назвал несущим первый
// running (awg10) и красил живой обход его тревогой.
const workrouterHydraPolicies = `{"installed":true,"running":true,"routes_hrneo":31,"routes_ndms":0,"routes_static":0,"hrneo_required":true,"singbox_router_active":false,
	"policies":[{"name":"HydraRoute","active_tunnel_id":"%ACTIVE%","via_vpn":true,"dns":31,"hr_neo":31,
		"links":[{"tunnel_id":"%ACTIVE%","role":"active"},{"tunnel_id":"%FALLBACK%","role":"fallback"}]},
	{"name":"RU","active_tunnel_id":"","via_vpn":false,"dns":2,"hr_neo":2,"links":[{"tunnel_id":"","role":"active"}]}]}`

func carrierTunnelRow(id, name, status, details string) db.EventRow {
	return db.EventRow{CheckName: "tunnel_" + id, Status: status, DetailsJSON: details}
}

func workrouterCarrierTraffic(t *testing.T, active, fallback string, hydra string) miniappTraffic {
	t.Helper()
	rows := []db.EventRow{
		carrierTunnelRow("awg10", "nl2", "fail", `{"tunnel_id":"awg10","tunnel_name":"nl2","status":"running","enabled":true,"handshake_age_sec":1440,"default_route_intent":true,"active_default_known":true,"is_active_default":false}`),
		carrierTunnelRow("awg14", "hipvps", "ok", `{"tunnel_id":"awg14","tunnel_name":"hipvps","status":"running","enabled":true,"handshake_age_sec":40,"matrix_latency_ms":97,"default_route_intent":true,"active_default_known":true,"is_active_default":false}`),
	}
	if hydra == "" {
		hydra = strings.NewReplacer("%ACTIVE%", active, "%FALLBACK%", fallback).Replace(workrouterHydraPolicies)
	}
	byCheck := map[string]db.EventRow{"hydraroute": {CheckName: "hydraroute", Status: "ok", DetailsJSON: hydra}}
	var tunnels []miniappTunnel
	for _, r := range rows {
		tu, ok := miniappTunnelFromEvent(r)
		if !ok {
			t.Fatalf("строка %s не спроецировалась", r.CheckName)
		}
		tunnels = append(tunnels, tu)
		byCheck[r.CheckName] = r
	}
	return miniappDeriveTraffic(tunnels, byCheck)
}

// Несущий жив, упал только запасной: обход идёт через hipvps, резерва нет.
func TestMiniappTrafficCarrierFromPolicies(t *testing.T) {
	got := workrouterCarrierTraffic(t, "awg14", "awg10", "")
	if got.Mode != miniappTrafficSplit {
		t.Fatalf("mode = %q, хотим split", got.Mode)
	}
	if got.EgressTunnelID != "awg14" || got.EgressTunnelName != "hipvps" {
		t.Fatalf("несущий = %q/%q, хотим awg14/hipvps", got.EgressTunnelID, got.EgressTunnelName)
	}
	if len(got.ReserveTunnelIDs) != 0 {
		t.Fatalf("запасной awg10 мёртв по проверке -- резерва нет, получили %v", got.ReserveTunnelIDs)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "reserve_tunnel_ids") {
		t.Errorf("пустой резерв уехал в JSON: %s", b)
	}
}

// Обратный случай: активное звено -- мёртвое по проверке, живой лежит в запасе.
// Несущим всё равно называем активное звено (так решает роутер), а живой
// запасной попадает в reserve_tunnel_ids.
func TestMiniappTrafficCarrierDeadActiveLiveReserve(t *testing.T) {
	got := workrouterCarrierTraffic(t, "awg10", "awg14", "")
	if got.Mode != miniappTrafficSplit || got.EgressTunnelID != "awg10" || got.EgressTunnelName != "nl2" {
		t.Fatalf("got %+v, хотим split через awg10/nl2", got)
	}
	if !reflect.DeepEqual(got.ReserveTunnelIDs, []string{"awg14"}) {
		t.Fatalf("reserve = %v, хотим [awg14]", got.ReserveTunnelIDs)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"reserve_tunnel_ids":["awg14"]`) {
		t.Errorf("резерв не уехал в JSON: %s", b)
	}
}

// Старый агент без сводки политик -- прежнее поведение: при двух живых имя
// несущего не угадываем.
func TestMiniappTrafficNoPoliciesKeepsOldBehaviour(t *testing.T) {
	got := workrouterCarrierTraffic(t, "", "", `{"installed":true,"running":true,"routes_hrneo":31,"singbox_router_active":false}`)
	if got.Mode != miniappTrafficSplit || got.EgressTunnelID != "" || len(got.ReserveTunnelIDs) != 0 {
		t.Fatalf("got %+v, хотим split без имени и без резерва", got)
	}
}

// Остановленный HydraRoute правил набора не исполняет: несущего по политике
// нет, ответ -- по-старому.
func TestMiniappTrafficPoliciesIgnoredWhenHydraRouteStopped(t *testing.T) {
	hydra := strings.NewReplacer("%ACTIVE%", "awg14", "%FALLBACK%", "awg10", `"running":true`, `"running":false`).Replace(workrouterHydraPolicies)
	got := workrouterCarrierTraffic(t, "", "", hydra)
	if got.EgressTunnelID != "" {
		t.Fatalf("HydraRoute остановлен -- несущий по политике не назван, получили %+v", got)
	}
}

// Главный выход через VPN-туннель -- ответ «vpn», политики его не меняют.
func TestMiniappTrafficPoliciesDoNotOverrideVPNDefault(t *testing.T) {
	tunnels := []miniappTunnel{
		{TunnelID: "awg10", Name: "nl2", Status: "ok", RunState: "running", ActiveDefaultKnown: true, IsActiveDefault: true},
		{TunnelID: "awg14", Name: "hipvps", Status: "ok", RunState: "running", ActiveDefaultKnown: true},
	}
	hydra := strings.NewReplacer("%ACTIVE%", "awg14", "%FALLBACK%", "awg10").Replace(workrouterHydraPolicies)
	got := miniappDeriveTraffic(tunnels, map[string]db.EventRow{"hydraroute": {CheckName: "hydraroute", DetailsJSON: hydra}})
	if got.Mode != miniappTrafficVPN || got.EgressTunnelID != "awg10" {
		t.Fatalf("got %+v, хотим vpn через awg10", got)
	}
}

// Несущий -- набор, который ведёт больше ИСПОЛНЯЕМЫХ правил: при остановленном
// HydraRoute правила HR-Neo не считаются, и набор с правилами NDMS побеждает.
func TestMiniappPolicyCarrierComparesExecutedRules(t *testing.T) {
	tunnels := []miniappTunnel{
		{TunnelID: "awg10", Name: "nl2", Status: "ok", RunState: "running", ActiveDefaultKnown: true},
		{TunnelID: "awg14", Name: "hipvps", Status: "ok", RunState: "running", ActiveDefaultKnown: true},
	}
	var hd miniappHydraDetails
	if err := json.Unmarshal([]byte(`{"running":false,"policies":[
		{"name":"A","active_tunnel_id":"awg10","via_vpn":true,"dns":40,"hr_neo":38,"links":[{"tunnel_id":"awg10","role":"active"}]},
		{"name":"B","active_tunnel_id":"awg14","via_vpn":true,"dns":5,"hr_neo":0,"links":[{"tunnel_id":"awg14","role":"active"}]}]}`), &hd); err != nil {
		t.Fatal(err)
	}
	carrier, _ := miniappPolicyCarrier(tunnels, hd)
	if carrier == nil || carrier.TunnelID != "awg14" {
		t.Fatalf("carrier = %+v, хотим awg14 (5 исполняемых против 2)", carrier)
	}
}
