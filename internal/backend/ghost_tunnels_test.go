package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// GHOST-01: awg-manager не ответил в последнем отчёте (tunnels=fail, строк
// туннелей нет). Инвентарь -- последняя УСПЕШНАЯ строка tunnels, а не просто
// последняя: иначе фильтр выключается целиком и удалённый туннель
// возвращается на экран со своим старым «ok».
func TestMiniappEventsGhostTunnelStaysHiddenWhenAwgmFails(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	now := time.Now().UTC().Truncate(time.Second)
	t0, t1, t2 := now.Add(-20*time.Minute), now.Add(-10*time.Minute), now.Add(-time.Minute)
	ins := func(check, status, details string, ts time.Time) {
		t.Helper()
		if err := d.Events().Insert(ownedID, check, status, details, ts); err != nil {
			t.Fatal(err)
		}
	}
	// t0: два туннеля; удалённый awg12 -- выход.
	ins("tunnels", "ok", `{}`, t0)
	ins("tunnel_awg12", "ok", `{"tunnel_id":"awg12","tunnel_name":"gone","status":"running","enabled":true,"default_route_intent":true,"active_default_known":true,"is_active_default":true}`, t0)
	ins("tunnel_awg13", "ok", `{"tunnel_id":"awg13","tunnel_name":"live","status":"running","enabled":true}`, t0)
	// t1: awg12 удалён.
	ins("tunnels", "ok", `{}`, t1)
	ins("tunnel_awg13", "ok", `{"tunnel_id":"awg13","tunnel_name":"live","status":"running","enabled":true}`, t1)
	// t2: awg-manager молчит.
	ins("tunnels", "fail", `{}`, t2)
	ins("agent_heartbeat", "ok", `{}`, t2)

	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var resp miniappRouterEventsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Tunnels) != 1 || resp.Tunnels[0].TunnelID != "awg13" {
		t.Fatalf("tunnels=%+v, want only awg13 (awg12 удалён в t1)", resp.Tunnels)
	}
	for _, c := range resp.Checks {
		if c.CheckName == "tunnel_awg12" {
			t.Fatal("призрак tunnel_awg12 в списке проверок")
		}
	}
	if resp.Traffic.EgressTunnelName == "gone" {
		t.Fatalf("удалённый туннель назван выходом: %+v", resp.Traffic)
	}
}

// LIST-01: список роутеров фильтрует строки так же, как экран роутера.
// Несущий awg14 удалён (нет в последнем инвентаре), его старая строка «ok»
// -- призрак; живого несущего нет, и плашка «резерв не работает» была бы
// ложью: список обязан сказать «тревога», как и экран.
func TestMiniappRoutersIgnoreGhostCarrier(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	seedWorkrouterEvents(t, d, ownedID, "awg14", "awg10")
	newer := time.Now().UTC().Add(-30 * time.Second)
	for _, r := range []struct{ check, status, details string }{
		{"agent_heartbeat", "ok", ""},
		{"tunnels", "ok", `{"tunnel_count":1}`},
		{"tunnel_awg10", "fail", `{"tunnel_id":"awg10","tunnel_name":"nl2","status":"running","enabled":true,"handshake_age_sec":1440,"active_default_known":true}`},
	} {
		if err := d.Events().Insert(ownedID, r.check, r.status, r.details, newer); err != nil {
			t.Fatal(err)
		}
	}
	seedHardIncident(t, d, ownedID, "tunnel_awg10")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	body, rows := miniappRoutersRows(t, h, ownerTG)
	if _, ok := rows["router-owned"]["reserve_only_alert"]; ok {
		t.Fatalf("несущий-призрак засчитан живым: %s", body)
	}
}
