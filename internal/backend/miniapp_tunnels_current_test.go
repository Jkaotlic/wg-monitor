package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func miniappEventsFor(t *testing.T, deps Deps, routerID, telegramUserID int64) miniappRouterEventsResp {
	t.Helper()
	h := NewMux(deps)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", routerID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappRouterEventsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func miniappByCheck(hydraDetails string) map[string]db.EventRow {
	return map[string]db.EventRow{
		"hydraroute": {CheckName: "hydraroute", Status: "ok", DetailsJSON: hydraDetails},
	}
}

func tunnelIDs(ts []miniappTunnel) []string {
	out := make([]string, 0, len(ts))
	for _, tu := range ts {
		out = append(out, tu.TunnelID)
	}
	return out
}

// Форма vvarg с прода 15.09.2026: туннель awg10 удалён мастером замены 07.09,
// но его последняя строка (с is_active_default=true) лежит в окне 30 дней.
// Экран писал «через VPN awg3-nl2-vvarg», а на роутере один туннель awg11 и
// обход идёт правилами через него. Сводная проверка tunnels из последнего
// отчёта -- опись того, что есть на роутере: строка туннеля старше неё не
// пришла в этом отчёте, значит туннеля больше нет.
func TestMiniappEventsDropsGhostTunnelOlderThanTunnelsInventory(t *testing.T) {
	d, ownedID, _, tgID := seedMiniappFleet(t)
	now := time.Now().UTC().Truncate(time.Second)
	ghostTS := now.Add(-8 * 24 * time.Hour)
	ins := func(check, status, details string, ts time.Time) {
		t.Helper()
		if err := d.Events().Insert(ownedID, check, status, details, ts); err != nil {
			t.Fatalf("insert %s: %v", check, err)
		}
	}
	ins("tunnel_awg10", "ok", `{"tunnel_id":"awg10","tunnel_name":"awg3-nl2-vvarg","status":"running","enabled":true,"default_route_intent":true,"is_active_default":true,"active_default_known":true,"routes_dns":18}`, ghostTS)
	ins("agent_heartbeat", "ok", "", now)
	ins("tunnels", "ok", `{"tunnel_count":1,"tunnel_count_enabled":1}`, now)
	ins("hydraroute", "ok", `{"singbox_router_active":false,"running":true,"routes_hrneo":18}`, now)
	ins("tunnel_awg11", "ok", `{"tunnel_id":"awg11","tunnel_name":"vvarg","status":"running","enabled":true,"default_route_intent":true,"is_active_default":false,"active_default_known":true,"routes_dns":18}`, now)

	resp := miniappEventsFor(t, Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999}, ownedID, tgID)

	if got := tunnelIDs(resp.Tunnels); len(got) != 1 || got[0] != "awg11" {
		t.Fatalf("туннели = %v, want [awg11]: awg10 не пришёл в последнем отчёте", got)
	}
	if resp.Traffic.Mode != miniappTrafficSplit || resp.Traffic.EgressTunnelName != "vvarg" {
		t.Fatalf("traffic = %+v, want split через vvarg, а не vpn через удалённый туннель", resp.Traffic)
	}
	for _, c := range resp.Checks {
		if c.CheckName == "tunnel_awg10" {
			t.Fatalf("строка удалённого туннеля не должна попадать и в checks: %+v", c)
		}
	}
}

// awg-manager не ответил в последнем цикле: агент шлёт только tunnels=fail и
// ни одной строки туннеля. Описи нет -- последние известные туннели остаются,
// иначе экран соврал бы «VPN-туннелей нет».
func TestMiniappEventsKeepsTunnelsWhenInventoryFailed(t *testing.T) {
	d, ownedID, _, tgID := seedMiniappFleet(t)
	now := time.Now().UTC().Truncate(time.Second)
	prev := now.Add(-time.Minute)
	for _, e := range []struct {
		check, status, details string
		ts                     time.Time
	}{
		{"tunnel_awg11", "ok", `{"tunnel_id":"awg11","tunnel_name":"vvarg","status":"running","active_default_known":true}`, prev},
		{"agent_heartbeat", "ok", "", now},
		{"tunnels", "fail", "", now},
	} {
		if err := d.Events().Insert(ownedID, e.check, e.status, e.details, e.ts); err != nil {
			t.Fatal(err)
		}
	}
	resp := miniappEventsFor(t, Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999}, ownedID, tgID)
	if got := tunnelIDs(resp.Tunnels); len(got) != 1 || got[0] != "awg11" {
		t.Fatalf("туннели = %v, want [awg11]: без описи отсеивать нечем", got)
	}
}

// Без строки tunnels вовсе (агент, не дошедший до сводной проверки) -- тоже
// ничего не отсеивается.
func TestMiniappEventsKeepsTunnelsWithoutInventoryRow(t *testing.T) {
	d, ownedID, _, tgID := seedMiniappFleet(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := d.Events().Insert(ownedID, "tunnel_awg10", "ok", `{"tunnel_id":"awg10","status":"running"}`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(ownedID, "agent_heartbeat", "ok", "", now); err != nil {
		t.Fatal(err)
	}
	resp := miniappEventsFor(t, Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999}, ownedID, tgID)
	if got := tunnelIDs(resp.Tunnels); len(got) != 1 {
		t.Fatalf("туннели = %v, want [awg10]", got)
	}
}

// Форма snekhaev с прода: список DNS-маршрутов перерос потолок чтения агента,
// hydraroute пришёл с routes_hrneo=0 и mechanism_probe_error, у туннелей нет
// routes_dns. Ноль здесь -- «не прочитал», а не «правил нет»: вывод
// «трафик напрямую, заблокированное не откроется» был бы выдумкой.
func TestMiniappDeriveTrafficUnknownWhenRulesUnreadable(t *testing.T) {
	tunnels := []miniappTunnel{
		{TunnelID: "awg12", Name: "MyVPN", RunState: "running", DefaultRouteIntent: true, ActiveDefaultKnown: true},
		{TunnelID: "awg13", Name: "MyVPN_Backup", RunState: "running", DefaultRouteIntent: true, ActiveDefaultKnown: true},
	}
	byCheck := miniappByCheck(`{"singbox_router_active":false,"running":true,"routes_hrneo":0,"mechanism_probe_error":"awgmgr /api/dns-routes/list: decode: unexpected end of JSON input"}`)
	got := miniappDeriveTraffic(tunnels, byCheck)
	if got.Mode != miniappTrafficUnknown {
		t.Fatalf("правила не прочитаны -- want %q, got %+v", miniappTrafficUnknown, got)
	}
	if got.Reason != miniappTrafficReasonRulesUnreadable {
		t.Fatalf("reason = %q, want %q: экрану надо сказать, ЧЕГО не узнали", got.Reason, miniappTrafficReasonRulesUnreadable)
	}
}

// Правила прочитались и их правда нет -- «напрямую» остаётся честным выводом.
func TestMiniappDeriveTrafficDirectWhenRulesReadAndAbsent(t *testing.T) {
	tunnels := []miniappTunnel{
		{TunnelID: "awg12", RunState: "running", ActiveDefaultKnown: true},
	}
	got := miniappDeriveTraffic(tunnels, miniappByCheck(`{"singbox_router_active":false,"running":true,"routes_hrneo":0}`))
	if got.Mode != miniappTrafficDirect || got.Reason != "" {
		t.Fatalf("want direct без причины, got %+v", got)
	}
}
