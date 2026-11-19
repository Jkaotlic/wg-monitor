package backend

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// v0.56, спека B1: несущий VPN-туннель считается одной функцией, и экраны
// «Роутер» и «VPN-туннели» берут его из traffic.carrier_*, а не угадывают
// каждый по-своему «первый running».

func carrierTraffic(t *testing.T, hydra string, rows ...db.EventRow) miniappTraffic {
	t.Helper()
	byCheck := map[string]db.EventRow{}
	if hydra != "" {
		byCheck["hydraroute"] = db.EventRow{CheckName: "hydraroute", TS: time.Now(), Status: "ok", DetailsJSON: hydra}
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
	if got.CarrierTunnelID != id || got.CarrierBasis != basis || (id != "" && (got.CarrierAlive == nil || *got.CarrierAlive != alive)) || (id == "" && got.CarrierAlive != nil) {
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

// Старый агент без сводки политик, живой VPN-туннель со своими правилами
// один -- single.
func TestMiniappCarrierSingleLive(t *testing.T) {
	hydra := `{"installed":true,"running":true,"routes_hrneo":31,"singbox_router_active":false}`
	stopped := `{"tunnel_id":"awg10","tunnel_name":"nl2","status":"stopped","enabled":false,"active_default_known":true,"is_active_default":false}`
	withRules := `{"tunnel_id":"awg14","tunnel_name":"hipvps","status":"running","enabled":true,"routes_dns":3,"default_route_intent":true,"active_default_known":true,"is_active_default":false}`
	got := carrierTraffic(t, hydra,
		carrierTunnelRow("awg10", "nl2", "ok", stopped),
		carrierTunnelRow("awg14", "hipvps", "ok", withRules),
	)
	wantCarrier(t, got, "awg14", "single", true)
}

// Живой один, но своих правил у него нет: обход держат правила HydraRoute,
// а в какой VPN-туннель они ведут, из проверок не узнать -- единственный
// живой ещё не несущий (ревью B1, п. 4).
func TestMiniappCarrierSingleLiveWithoutOwnRulesNone(t *testing.T) {
	hydra := `{"installed":true,"running":true,"routes_hrneo":31,"singbox_router_active":false}`
	stopped := `{"tunnel_id":"awg10","tunnel_name":"nl2","status":"stopped","enabled":false,"active_default_known":true,"is_active_default":false}`
	got := carrierTraffic(t, hydra,
		carrierTunnelRow("awg10", "nl2", "ok", stopped),
		carrierTunnelRow("awg14", "hipvps", "ok", rowHipAlive),
	)
	if got.Mode != miniappTrafficSplit {
		t.Fatalf("mode = %q, хотим split", got.Mode)
	}
	wantCarrier(t, got, "", "none", false)
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

func TestCarrierUnknownAndUnavailableReserves(t *testing.T) {
	hydra := strings.NewReplacer("%ACTIVE%", "awg14", "%FALLBACK%", "awg10").Replace(workrouterHydraPolicies)
	for _, fallback := range []string{
		`{"status":"stopped","enabled":true,"active_default_known":true}`,
		`{"status":"running","enabled":false,"active_default_known":true}`,
	} {
		got := carrierTraffic(t, hydra,
			carrierTunnelRow("awg14", "", "unknown", rowHipAlive),
			carrierTunnelRow("awg10", "", "ok", fallback))
		b, err := json.Marshal(got)
		if err != nil || got.CarrierTunnelID != "awg14" || strings.Contains(string(b), `"carrier_alive"`) || !strings.Contains(string(b), `"reserve_tunnel_ids":[]`) {
			t.Errorf("unverified carrier and unavailable reserve: %s, %v", b, err)
		}
	}
}

func TestPolicyReservesRequireFreshMeasurementsAndPolicy(t *testing.T) {
	now := time.Now().UTC()
	hydra := strings.NewReplacer("%ACTIVE%", "line", "%FALLBACK%", "reserve").Replace(workrouterHydraPolicies)
	for _, tc := range []struct {
		name                  string
		reserveAge, policyAge time.Duration
		want                  bool
	}{
		{"fresh", time.Minute, time.Minute, true},
		{"old reserve", 20 * time.Minute, time.Minute, false},
		{"future reserve", -20 * time.Minute, time.Minute, false},
		{"old policy", time.Minute, 20 * time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := map[string]db.EventRow{"hydraroute": {TS: now.Add(-tc.policyAge), DetailsJSON: hydra}}
			tunnels := []miniappTunnel{
				{TunnelID: "line", RunState: "running", Status: "ok", ActiveDefaultKnown: true, TS: now.Format(time.RFC3339)},
				{TunnelID: "reserve", RunState: "running", Status: "ok", ActiveDefaultKnown: true, TS: now.Add(-tc.reserveAge).Format(time.RFC3339)},
			}
			got := miniappDeriveTraffic(tunnels, checks)
			if (len(got.ReserveTunnelIDs) != 0) != tc.want {
				t.Fatalf("reserve list %v, want available=%v", got.ReserveTunnelIDs, tc.want)
			}
			if tc.policyAge > statusEvidenceFreshFor && got.CarrierAlive != nil {
				t.Fatalf("old policy supported a current carrier verdict: %+v", got)
			}
			tunnels[0].TS = now.Add(-20 * time.Minute).Format(time.RFC3339)
			if stale := miniappDeriveTraffic(tunnels, checks); stale.CarrierAlive != nil {
				t.Fatalf("old carrier measurement supported a current verdict: %+v", stale)
			}
		})
	}
}

func TestTrafficFactsDoNotAdvertiseUnconfirmedSpare(t *testing.T) {
	db, uid, _, _ := seedMiniappFleet(t)
	seedWorkrouterEvents(t, db, uid, "awg10", "awg14")
	facts := AlertTrafficFacts(db)
	if got := facts(uid, "tunnel_awg10"); got == nil || got.Spare == "" {
		t.Fatalf("fresh spare missing: %+v", got)
	}
	if err := db.Events().Insert(uid, "tunnel_awg14", "ok", `{"status":"running","unverified":true}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := facts(uid, "tunnel_awg10"); got != nil {
		t.Fatalf("unknown spare cannot support a traffic claim: %+v", got)
	}
}
