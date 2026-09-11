package backend

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Главный выход "direct" -- не поломка, а обычная раздельная маршрутизация:
// всё, что не названо правилами, идёт мимо VPN, а заблокированное уводят
// правила. Так настроены рабочий роутер и testkeen: settings.download.routeTag
// = "direct", HydraRoute запущен, правила ведут в VPN-туннели. Экран, который
// видел здесь «обход не работает», врал владельцу о его же интернете.

func tunnelRowFromAgent(id, name, details string) db.EventRow {
	return db.EventRow{CheckName: "tunnel_" + id, Status: "ok", DetailsJSON: details}
}

// Форма снята с рабочего роутера 11.09.2026: два работающих VPN-туннеля, оба
// заявляют основной маршрут, главный выход -- напрямую, HydraRoute ведёт 30
// правил через набор с активным звеном awg14.
func workRouterTraffic(t *testing.T) miniappTraffic {
	t.Helper()
	rows := []db.EventRow{
		tunnelRowFromAgent("awg10", "vpn-nl", `{"tunnel_id":"awg10","tunnel_name":"vpn-nl","status":"running","enabled":true,"default_route_intent":true,"active_default_known":true,"is_active_default":false,"routes_dns":30,"routes_dns_hr":30,"routes_static":0}`),
		tunnelRowFromAgent("awg14", "vpn-de", `{"tunnel_id":"awg14","tunnel_name":"vpn-de","status":"running","enabled":true,"default_route_intent":true,"active_default_known":true,"is_active_default":false}`),
	}
	byCheck := map[string]db.EventRow{
		"hydraroute": {CheckName: "hydraroute", Status: "ok", DetailsJSON: `{"installed":true,"running":true,"routes_hrneo":30,"routes_ndms":1,"routes_static":0,"hrneo_required":true,"singbox_router_active":false}`},
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

func TestMiniappDeriveTrafficSplitWhenRulesCarryBypass(t *testing.T) {
	got := workRouterTraffic(t)
	if got.Mode != miniappTrafficSplit {
		t.Fatalf("главный выход напрямую, но правила ведут заблокированное в VPN-туннели -- ждали %q, получили %q", miniappTrafficSplit, got.Mode)
	}
	// Какой из двух живых VPN-туннелей несёт обход, решает набор HydraRoute, и
	// в проверках этого нет: правила без явного маршрута агент приписывает
	// первому заявившему основной маршрут (vpn-nl), а на деле активен vpn-de.
	// Назвать любой из двух -- угадать.
	if got.EgressTunnelID != "" || got.EgressTunnelName != "" {
		t.Fatalf("при двух живых VPN-туннелях имя выхода -- догадка, получили %+v", got)
	}
}

// Единственный живой VPN-туннель -- единственный, кто вообще может нести
// обход. Тут имя не догадка, а вывод.
func TestMiniappDeriveTrafficSplitNamesTheOnlyLiveTunnel(t *testing.T) {
	tunnels := []miniappTunnel{
		{TunnelID: "awg10", Name: "vpn-nl", Status: "ok", RunState: "running", ActiveDefaultKnown: true, RoutesDNS: 12},
		{TunnelID: "awg14", Name: "vpn-reserve", Status: "fail", RunState: "stopped", ActiveDefaultKnown: true},
	}
	got := miniappDeriveTraffic(tunnels, nil)
	if got.Mode != miniappTrafficSplit {
		t.Fatalf("правила есть -- ждали %q, получили %q", miniappTrafficSplit, got.Mode)
	}
	if got.EgressTunnelID != "awg10" || got.EgressTunnelName != "vpn-nl" {
		t.Fatalf("живой VPN-туннель один -- его и назвать, получили %+v", got)
	}
}

// Статические маршруты -- тоже обход: адреса уходят в VPN-туннель правилом.
func TestMiniappDeriveTrafficSplitCountsStaticRoutes(t *testing.T) {
	tunnels := []miniappTunnel{
		{TunnelID: "awg10", Name: "vpn-nl", Status: "ok", RunState: "running", ActiveDefaultKnown: true, RoutesStatic: 3},
	}
	if got := miniappDeriveTraffic(tunnels, nil); got.Mode != miniappTrafficSplit {
		t.Fatalf("статические маршруты ведут в VPN-туннель -- ждали %q, получили %q", miniappTrafficSplit, got.Mode)
	}
}

// Правила, которые ведут в лежащий VPN-туннель, ничего не обходят.
func TestMiniappDeriveTrafficDirectWhenRulesPointAtDeadTunnel(t *testing.T) {
	tunnels := []miniappTunnel{
		{TunnelID: "awg10", Name: "vpn-nl", Status: "fail", RunState: "stopped", ActiveDefaultKnown: true, RoutesDNS: 12},
	}
	if got := miniappDeriveTraffic(tunnels, nil); got.Mode != miniappTrafficDirect {
		t.Fatalf("единственный VPN-туннель с правилами лежит -- ждали %q, получили %q", miniappTrafficDirect, got.Mode)
	}
}

// Остановленный HydraRoute правил не исполняет, сколько бы их ни было.
func TestMiniappDeriveTrafficDirectWhenHydraRouteStopped(t *testing.T) {
	tunnels := []miniappTunnel{
		{TunnelID: "awg10", Name: "vpn-nl", Status: "ok", RunState: "running", ActiveDefaultKnown: true},
	}
	byCheck := map[string]db.EventRow{
		"hydraroute": {CheckName: "hydraroute", Status: "fail", DetailsJSON: `{"installed":true,"running":false,"routes_hrneo":30}`},
	}
	if got := miniappDeriveTraffic(tunnels, byCheck); got.Mode != miniappTrafficDirect {
		t.Fatalf("HydraRoute остановлен, других правил нет -- ждали %q, получили %q", miniappTrafficDirect, got.Mode)
	}
}

// Счётчики правил не уезжают в мини-апп: экрану нужен вывод, а не сырьё.
func TestMiniappTunnelRuleCountsStayServerSide(t *testing.T) {
	tu, _ := miniappTunnelFromEvent(tunnelRowFromAgent("awg10", "vpn-nl", `{"routes_dns":30,"routes_static":2}`))
	if tu.RoutesDNS != 30 || tu.RoutesStatic != 2 {
		t.Fatalf("счётчики правил не прочитаны: %+v", tu)
	}
}
