package backend

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postMiniappCommand(t *testing.T, h http.Handler, routerID, telegramUserID int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", routerID), bytes.NewReader([]byte(body)))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Фаза D1: обслуживание переезжает в приложение. Каждое из этих действий
// читает роутер и ничего в нём не меняет, поэтому аргументов у них нет вовсе
// -- и всё, что прислал клиент, обязано быть выброшено, а не проверено.
func TestMiniappMaintenanceCommandsTakeNoArgs(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	for _, action := range []string{"version_audit", "router_doctor", "hrneo_doctor", "pingcheck_now"} {
		sink.enqueued = nil
		rec := postMiniappCommand(t, h, ownedID, telegramUserID,
			fmt.Sprintf(`{"action":%q,"args":{"ndms_name":"ISP","url":"http://evil"}}`, action))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: want 202, got %d: %s", action, rec.Code, rec.Body.String())
		}
		if len(sink.enqueued) != 1 || sink.enqueued[0].Action != action {
			t.Fatalf("%s: enqueued = %+v", action, sink.enqueued)
		}
		if len(sink.enqueued[0].Args) != 0 {
			t.Fatalf("%s: клиентские аргументы обязаны быть выброшены, got %+v", action, sink.enqueued[0].Args)
		}
	}
}

// Включение и выключение туннеля -- та же топология, что у перезапуска:
// клиент присылает tunnel_id, а ndms_name сервер достаёт из событий ЭТОГО
// роутера. Присланный клиентом ndms_name не доезжает до агента никогда.
func TestMiniappTunnelEnableResolvesNDMSNameServerSide(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg20", "Wireguard0")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	rec := postMiniappCommand(t, h, ownedID, telegramUserID,
		`{"action":"tunnel_disable","args":{"tunnel_id":"awg20","ndms_name":"ISP"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if sink.enqueued[0].Args["ndms_name"] != "Wireguard0" {
		t.Fatalf("агент обязан получить имя, найденное сервером: %+v", sink.enqueued[0].Args)
	}
}

// Opkg-туннель в NDMS не заведён, и «включить интерфейс» ему нечем: агент
// умеет это только через ndmc. Отказ обязан быть внятным, а не 202 с командой,
// которая молча ничего не сделает.
func TestMiniappTunnelEnableRefusesTunnelWithoutNDMSName(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg11", "")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	rec := postMiniappCommand(t, h, ownedID, telegramUserID,
		`{"action":"tunnel_enable","args":{"tunnel_id":"awg11"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("ничего не должно уйти агенту: %+v", sink.enqueued)
	}
}

// Чужой туннель не резолвится и здесь -- та же граница, что у перезапуска.
func TestMiniappTunnelEnableRejectsOtherRoutersTunnel(t *testing.T) {
	d, ownedID, otherID, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, otherID, "awg20", "Wireguard0")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	rec := postMiniappCommand(t, h, ownedID, telegramUserID,
		`{"action":"tunnel_enable","args":{"tunnel_id":"awg20"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Переключение ping-check адресуется тем же tunnel_id, а enable -- это выбор
// человека, единственный аргумент, который клиенту разрешено прислать.
func TestMiniappPingcheckToggleResolvesTunnelAndKeepsChoice(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg20", "Wireguard0")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	rec := postMiniappCommand(t, h, ownedID, telegramUserID,
		`{"action":"pingcheck_toggle","args":{"tunnel_id":"awg20","enable":true,"ndms_name":"ISP"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	args := sink.enqueued[0].Args
	if args["tunnel_id"] != "awg20" || args["ndms_name"] != "Wireguard0" || args["enable"] != true {
		t.Fatalf("args = %+v", args)
	}
}
