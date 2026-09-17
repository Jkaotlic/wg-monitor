package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Список правил HydraRoute Neo -- только чтение: видят все, у кого есть
// доступ к роутеру, и до агента не доезжает ни один клиентский аргумент.
func TestMiniappHRNeoInventoryOpenToOperator(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"hrneo_inventory","args":{"rule":"evil"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("оператор: %d %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 || sink.enqueued[0].Action != "hrneo_inventory" || len(sink.enqueued[0].Args) != 0 {
		t.Fatalf("очередь: %+v", sink.enqueued)
	}
	if rec := postMiniappCommand(t, h, ownedID, 777, `{"action":"hrneo_inventory"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("посторонний: %d", rec.Code)
	}
}

// Цикл 4, решение 1: запуск и остановка HydraRoute Neo -- админ и владелец.
// Остановка выключает правила по имени сайта на весь роутер. Оператору --
// 403 owner_only, как у всех owner-only действий /commands; перезапуск
// остаётся ему доступен.
func TestMiniappHRNeoStartStopOwnerOnly(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	for _, name := range []string{"hrneo_start", "hrneo_stop"} {
		body := `{"action":"service_restart","args":{"name":"` + name + `","extra":"x"}}`
		if rec := postMiniappCommand(t, h, ownedID, 555, body); rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte("owner_only")) {
			t.Errorf("оператор %s: %d %s", name, rec.Code, rec.Body.String())
		}
		for _, who := range []int64{100, 999} {
			if rec := postMiniappCommand(t, h, ownedID, who, body); rec.Code != http.StatusAccepted {
				t.Errorf("%d %s: %d %s", who, name, rec.Code, rec.Body.String())
			}
		}
	}
	if len(sink.enqueued) != 4 {
		t.Fatalf("очередь: %+v", sink.enqueued)
	}
	for _, c := range sink.enqueued {
		name := c.Args["name"]
		if c.Action != "service_restart" || (name != "hrneo_start" && name != "hrneo_stop") || len(c.Args) != 1 {
			t.Errorf("команда: %+v", c)
		}
	}
	if rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"service_restart","args":{"name":"hrneo"}}`); rec.Code != http.StatusAccepted {
		t.Fatalf("перезапуск оператором: %d %s", rec.Code, rec.Body.String())
	}
}

// Гейт стоит и на опросе результата: оператор, знающий идентификатор чужой
// остановки, не читает её ответ.
func TestMiniappHRNeoStopResultOwnerOnly(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	sink.commands = map[string]wire.Command{"cmd-stop": {ID: "cmd-stop", Action: "service_restart", Args: map[string]any{"name": "hrneo_stop"}}}
	sink.results = map[string]wire.CommandResult{"cmd-stop": {ID: "cmd-stop", Status: "ok", Output: "hrneo stop sent"}}
	get := func(user int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/commands/cmd-stop?wait_sec=0", ownedID), nil)
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", user))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(555); rec.Code != http.StatusForbidden {
		t.Fatalf("оператор: %d %s", rec.Code, rec.Body.String())
	}
	rec := get(100)
	var res wire.CommandResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.Status != "ok" {
		t.Fatalf("владелец: %d %s", rec.Code, rec.Body.String())
	}
}

// Решение 5: правила без туннеля («Напрямую (WAN)») переносятся тем же кругом,
// что и прочий перенос. Перенести «в WAN» агент не умеет -- отказ до очереди.
func TestMiniappRouteRebindFromWAN(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.32.0")
	rec := postMiniappCommand(t, h, ownedID, 555, `{"action":"route_rebind","args":{"src_tunnel_id":"`+wire.RouteOtherID+`","dst_tunnel_id":"awg12"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("из WAN: %d %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 || fmt.Sprint(sink.enqueued[0].Args) != fmt.Sprint(map[string]any{"src_tunnel_id": wire.RouteOtherID, "dst_tunnel_id": "awg12"}) {
		t.Fatalf("очередь: %+v", sink.enqueued)
	}
	rec = postMiniappCommand(t, h, ownedID, 555, `{"action":"route_rebind","args":{"src_tunnel_id":"awg12","dst_tunnel_id":"`+wire.RouteOtherID+`"}}`)
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("invalid_route_id")) {
		t.Fatalf("в WAN: %d %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 {
		t.Fatalf("перенос в WAN ушёл агенту: %+v", sink.enqueued)
	}
}
