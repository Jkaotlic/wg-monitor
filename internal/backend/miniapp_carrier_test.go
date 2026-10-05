package backend

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// v0.56, спека B1: несущий VPN-туннель считается одной функцией, и экраны
// «Роутер» и «VPN-туннели» берут его из traffic.carrier_*, а не угадывают
// каждый по-своему «первый running».

func carrierTraffic(t *testing.T, hydra string, rows ...db.EventRow) miniappTraffic {
	t.Helper()
	byCheck := map[string]db.EventRow{}
	if hydra != "" {
		byCheck["hydraroute"] = db.EventRow{CheckName: "hydraroute", Status: "ok", DetailsJSON: hydra}
	}
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

func wantCarrier(t *testing.T, got miniappTraffic, id, basis string, alive bool) {
	t.Helper()
	if got.CarrierTunnelID != id || got.CarrierBasis != basis || got.CarrierAlive != alive {
		t.Fatalf("несущий = %q/%q/alive=%v, хотим %q/%q/alive=%v (traffic %+v)", got.CarrierTunnelID, got.CarrierBasis, got.CarrierAlive, id, basis, alive, got)
	}
}

const (
	rowNL2Dead  = `{"tunnel_id":"awg10","tunnel_name":"nl2","status":"running","enabled":true,"handshake_age_sec":1440,"default_route_intent":true,"active_default_known":true,"is_active_default":false}`
	rowHipAlive = `{"tunnel_id":"awg14","tunnel_name":"hipvps","status":"running","enabled":true,"handshake_age_sec":40,"default_route_intent":true,"active_default_known":true,"is_active_default":false}`
)

// Несущий жив по сводке политик -- policy, жив.
func TestMiniappCarrierPolicyAlive(t *testing.T) {
	got := workrouterCarrierTraffic(t, "awg14", "awg10", "")
	wantCarrier(t, got, "awg14", "policy", true)
	b, _ := json.Marshal(got)
	for _, k := range []string{`"carrier_tunnel_id":"awg14"`, `"carrier_basis":"policy"`, `"carrier_alive":true`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("в JSON нет %s: %s", k, b)
		}
	}
}

// Активное звено поднято, но проверка провалена: несущий -- оно (так решил
// роутер), но живым не считается. «Первый running» (живой запасной) несущим
// не становится.
func TestMiniappCarrierPolicyDeadActive(t *testing.T) {
	got := workrouterCarrierTraffic(t, "awg10", "awg14", "")
	wantCarrier(t, got, "awg10", "policy", false)
}

// Активное звено остановлено (у opkg-туннелей автофолбэка нет): политика
// всё равно ведёт в него -- несущий он, не живой; живой сосед не назначается.
func TestMiniappCarrierPolicyStoppedActive(t *testing.T) {
	hydra := strings.NewReplacer("%ACTIVE%", "awg10", "%FALLBACK%", "awg14").Replace(workrouterHydraPolicies)
	stopped := `{"tunnel_id":"awg10","tunnel_name":"nl2","status":"stopped","enabled":true,"default_route_intent":true,"active_default_known":true,"is_active_default":false}`
	got := carrierTraffic(t, hydra,
		carrierTunnelRow("awg10", "nl2", "fail", stopped),
		carrierTunnelRow("awg14", "hipvps", "ok", rowHipAlive),
	)
	wantCarrier(t, got, "awg10", "policy", false)
	// Резерв -- живые запасные звенья политики несущего, а не «нет»: иначе
	// экран сказал бы «запасного нет» при живом hipvps.
	if len(got.ReserveTunnelIDs) != 1 || got.ReserveTunnelIDs[0] != "awg14" {
		t.Fatalf("reserve = %v, хотим [awg14]", got.ReserveTunnelIDs)
	}
}

// sing-box выбирает маршрут для каждого адреса: несущего нет, даже если
// политика где-то назначила активное звено. Снимок песочницы sandbox-broken:
// vpn-nl провален, vpn-de жив -- раньше «Роутер» называл vpn-de.
func TestMiniappCarrierSingboxNone(t *testing.T) {
	hydra := `{"routes_hrneo":37,"routes_ndms":4,"routes_static":12,"active_backend":"hr_neo","singbox_router_active":true,
		"policies":[{"name":"HydraRoute","active_tunnel_id":"awg12","via_vpn":true,"dns":32,"hr_neo":28,"links":[{"tunnel_id":"awg12","role":"active"},{"tunnel_id":"awg10","role":"fallback"}]}]}`
	got := carrierTraffic(t, hydra,
		carrierTunnelRow("awg12", "vpn-nl", "fail", `{"tunnel_id":"awg12","tunnel_name":"vpn-nl","status":"down","enabled":true,"handshake_age_sec":5400,"default_route_intent":true,"is_active_default":false,"active_default_known":true}`),
		carrierTunnelRow("awg10", "vpn-de", "ok", `{"tunnel_id":"awg10","tunnel_name":"vpn-de","status":"running","enabled":true,"handshake_age_sec":48,"active_default_known":true}`),
	)
	if got.Mode != miniappTrafficSingbox {
		t.Fatalf("mode = %q, хотим singbox", got.Mode)
	}
	wantCarrier(t, got, "", "none", false)
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"carrier_basis":"none"`) {
		t.Errorf("basis none не уехал в JSON: %s", b)
	}
}

// Старый агент без сводки политик, живой VPN-туннель с правилами один --
// single.
func TestMiniappCarrierSingleLive(t *testing.T) {
	hydra := `{"installed":true,"running":true,"routes_hrneo":31,"singbox_router_active":false}`
	stopped := `{"tunnel_id":"awg10","tunnel_name":"nl2","status":"stopped","enabled":false,"active_default_known":true,"is_active_default":false}`
	got := carrierTraffic(t, hydra,
		carrierTunnelRow("awg10", "nl2", "ok", stopped),
		carrierTunnelRow("awg14", "hipvps", "ok", rowHipAlive),
	)
	wantCarrier(t, got, "awg14", "single", true)
}

// Старый агент, два поднятых: кто несёт -- выбирают правила, и из проверок
// этого не узнать. Не гадаем.
func TestMiniappCarrierTwoLiveNoPoliciesNone(t *testing.T) {
	hydra := `{"installed":true,"running":true,"routes_hrneo":31,"singbox_router_active":false}`
	got := carrierTraffic(t, hydra,
		carrierTunnelRow("awg10", "nl2", "fail", rowNL2Dead),
		carrierTunnelRow("awg14", "hipvps", "ok", rowHipAlive),
	)
	wantCarrier(t, got, "", "none", false)
}

// Главный выход через VPN-туннель (routeTag), сводки политик нет: несущий --
// он, слово роутера.
func TestMiniappCarrierDefaultEgressSingle(t *testing.T) {
	got := carrierTraffic(t, "",
		carrierTunnelRow("awg14", "hipvps", "ok", `{"tunnel_id":"awg14","tunnel_name":"hipvps","status":"running","enabled":true,"default_route_intent":true,"active_default_known":true,"is_active_default":true}`),
	)
	if got.Mode != miniappTrafficVPN {
		t.Fatalf("mode = %q, хотим vpn", got.Mode)
	}
	wantCarrier(t, got, "awg14", "single", true)
}
