package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// The allowlist IS the security boundary. This test pins both halves: what a
// mini-app session may dispatch, and -- more importantly -- what it may not.
// Mirrors TestDashboardCommandDispatchRejectsHiddenBackendURLUpdate.
func TestMiniappCommandAllowlistContents(t *testing.T) {
	allowed := []string{
		"force_recheck", "diag_now", "tunnels_status", "route_status",
		"check_via_tunnel", "check_direct", "tunnel_restart",
		// Управление маршрутами (фаза C3). Каждое router-local и обратимо:
		// add/delete идут через план с хешем черновика, rebind -- с превью и
		// результатом по категориям, promote переупорядочивает уже состоящие
		// в цепочке интерфейсы и отменяется тем же действием. Аргументы всех
		// семи проверяются явными ветками sanitizeWizardCommandArgs.
		"route_templates", "route_add_plan", "route_add",
		"route_delete_plan", "route_delete", "route_rebind",
		"route_policy_promote",
		// Обслуживание (фаза D1). Четыре читающих: версии, два доктора и
		// разовый прогон проверки связи. Аргументов не берут вовсе.
		"version_audit", "router_doctor", "hrneo_doctor", "pingcheck_now",
		// Три мутирующих, все router-local и обратимые своей же парой.
		// Радиус ограничен тем же резолвером, что у tunnel_restart: клиент
		// присылает tunnel_id, ndms_name сервер достаёт из событий этого
		// роутера, и присланный клиентом никогда не доезжает до агента.
		"tunnel_enable", "tunnel_disable", "pingcheck_toggle",
		// Обмен по туннелю (фаза F): читающее, ряд ведёт сам роутер.
		"tunnel_traffic",
		// Прошивка (фаза D2): чтение -- всем с доступом, установка -- только
		// владельцу (проверяется отдельно, miniappOwnerOnlyActions).
		"firmware_status", "firmware_install",
	}
	for _, a := range allowed {
		if !miniappCommandAllowlist[a] {
			t.Errorf("%q must be dispatchable from the mini app", a)
		}
	}

	// Each of these is denied for a specific reason -- see the comments on the
	// allowlist. If a future change adds one, it must be a deliberate decision
	// with its own justification, not a silent widening.
	denied := []string{
		"update_backend_url", // fleet takeover
		"tunnel_delete",      // irreversible
		"self_update",        // audited deploy flow
		"tunnel_import",      // route/config mutation
		"dns_reset",          // router-global; stays on the dashboard
		"agent_config_get",
		"update_agent_config",
		"opkg_upgrade",
		"entware_clean_run",
		// Ответ несёт ndms_name каждого туннеля -- топологию, которую белый
		// список туннелей клиенту не отдаёт. Состояние проверки связи экран
		// берёт из проекции туннеля, а не из этого ответа.
		"pingcheck_status",
	}

	// tunnel_enable/tunnel_disable и router_doctor раньше лежали в denied.
	// Оба запрета сняты осознанно, и вот чем:
	//
	//   - выключение туннеля -- не «изменение конфига», а переключатель:
	//     обратное действие стоит рядом на том же экране, и радиус у него тот
	//     же, что у перезапуска, -- один туннель одного роутера, чьё имя
	//     интерфейса подставил сервер. Приложение, умеющее только чинить, но
	//     не умеющее выключить упавшую линию, оставляет человека в боте.
	//   - router_doctor закрывали как «простыню текста для админа». Закрыт был
	//     не радиус поражения (он читающий), а вёрстка: экран разбирает его
	//     вывод строками данных, а сырой ответ прячет под спойлер.
	for _, a := range denied {
		if miniappCommandAllowlist[a] {
			t.Errorf("%q must NOT be dispatchable from a mini-app session", a)
		}
	}

	// Pin the allowlist's exact size too, not just spot-checked membership --
	// otherwise an entry added outside both `allowed` and `denied` (e.g. by a
	// careless merge) would pass this test silently. If this count changes,
	// add the new action to `allowed` or `denied` above with its own
	// justification; don't just bump the number.
	if len(miniappCommandAllowlist) != len(allowed) {
		t.Fatalf("miniappCommandAllowlist has %d entries, want exactly the %d in `allowed`: %v",
			len(miniappCommandAllowlist), len(allowed), miniappCommandAllowlist)
	}
}

// Every allowlisted action must also be a real wire action -- an allowlist entry
// that the agent would reject is a latent 'nothing happens' bug.
func TestMiniappCommandAllowlistEntriesAreValidWireActions(t *testing.T) {
	for a := range miniappCommandAllowlist {
		if !wire.IsValidCommandAction(a) {
			t.Errorf("%q is allowlisted but not a valid wire action", a)
		}
	}
}

func TestMiniappCommandRejectsDeniedAction(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	body := `{"action":"update_backend_url","args":{"url":"https://evil.example"}}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", ownedID), bytes.NewReader([]byte(body)))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a denied action, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("denied action must not enqueue: %+v", sink.enqueued)
	}
}

// ACL is checked before existence, so a stranger cannot use the endpoint to
// discover which router ids exist: an existing-but-not-theirs router and a
// nonexistent router id must produce the exact same response.
func TestMiniappCommandStrangerGets404BeforeExistence(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	const strangerID = 999001
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", ownedID),
		bytes.NewReader([]byte(`{"action":"force_recheck"}`)))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", strangerID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stranger must get 404 for an existing-but-forbidden router, got %d: %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/miniapp/routers/424242/commands",
		bytes.NewReader([]byte(`{"action":"force_recheck"}`)))
	req2.AddCookie(miniappSessionCookieFor(t, "test-bot-token", strangerID))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != rec.Code {
		t.Fatalf("existing-but-forbidden (%d) and nonexistent (%d) must be indistinguishable", rec.Code, rec2.Code)
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("stranger must never reach enqueue: %+v", sink.enqueued)
	}
}

// A missing result is 404 result_not_ready, not an error -- the agent simply
// hasn't answered yet, and the client is expected to poll again. Same
// contract as wizardCmdResultHandler's timeout branch.
func TestMiniappCommandResultNotReadyIs404(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/v1/miniapp/routers/%d/commands/deadbeef?wait_sec=0", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 result_not_ready so the client just polls again, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "result_not_ready" {
		t.Fatalf("want code=result_not_ready, got %+v", body)
	}
}

// Once the agent has answered, the endpoint hands back the CommandResult
// as-is. AwaitResult must be called with routerID itself -- the mini app's
// routerID IS users.id, so there is no nickname round-trip like the wizard's
// polling endpoint needs.
func TestMiniappCommandResultReturnsResultWhenReady(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	sink := &dashboardActionSink{results: map[string]wire.CommandResult{
		"deadbeef": {ID: "deadbeef", Status: "ok", Output: "reachable", DurationMs: 42},
	}}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/v1/miniapp/routers/%d/commands/deadbeef?wait_sec=0", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got wire.CommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Output != "reachable" || got.DurationMs != 42 {
		t.Fatalf("unexpected result: %+v", got)
	}
	if sink.awaitUserID != ownedID {
		t.Fatalf("AwaitResult must be called with the router's own id (== users.id), got %d", sink.awaitUserID)
	}
}

// ACL is checked before cmd_id is even looked at, so a stranger cannot use
// this endpoint either to read another router's command output or to probe
// which router ids exist. The sink DOES have a ready result for this cmd_id
// -- proving the 404 comes from the ACL gate, not merely from an empty sink.
func TestMiniappCommandResultStrangerGets404BeforeExistence(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	sink := &dashboardActionSink{results: map[string]wire.CommandResult{
		"deadbeef": {ID: "deadbeef", Status: "ok"},
	}}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	const strangerID = 999001
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/v1/miniapp/routers/%d/commands/deadbeef?wait_sec=0", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", strangerID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stranger must not poll another router's command results, got %d: %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet,
		"/v1/miniapp/routers/424242/commands/deadbeef?wait_sec=0", nil)
	req2.AddCookie(miniappSessionCookieFor(t, "test-bot-token", strangerID))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != rec.Code {
		t.Fatalf("existing-but-forbidden (%d) and nonexistent (%d) must be indistinguishable", rec.Code, rec2.Code)
	}
}

// seedMiniappTunnelEvent inserts a tunnel_<tunnelID> event for routerID whose
// details carry ndms_name -- the only way miniappResolveTunnelRestartArgs can
// learn the router-local interface name for that tunnel.
func seedMiniappTunnelEvent(t *testing.T, d *db.DB, routerID int64, tunnelID, ndmsName string) {
	t.Helper()
	details := fmt.Sprintf(`{"tunnel_id":%q,"ndms_name":%q}`, tunnelID, ndmsName)
	if err := d.Events().Insert(routerID, "tunnel_"+tunnelID, "ok", details, time.Now()); err != nil {
		t.Fatalf("seed tunnel event: %v", err)
	}
}

// This is the fix for the "tunnel_restart can restart ANY router interface"
// finding: the client sends a tunnel_id, and the backend resolves it to
// ndms_name from ITS OWN tunnel_* event rows -- the client-supplied value
// never reaches the agent directly.
func TestMiniappCommandEnqueuesAllowedAction(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg12", "Wireguard0")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	body := `{"action":"tunnel_restart","args":{"tunnel_id":"awg12"}}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", ownedID), bytes.NewReader([]byte(body)))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 || sink.enqueuedUsers[0] != ownedID {
		t.Fatalf("unexpected enqueue: users=%v cmds=%+v", sink.enqueuedUsers, sink.enqueued)
	}
	if sink.enqueued[0].Action != "tunnel_restart" || sink.enqueued[0].Args["ndms_name"] != "Wireguard0" {
		t.Fatalf("bad command: the agent must receive the resolved ndms_name, got %+v", sink.enqueued[0])
	}
}

// The attack this whole fix exists to close: a caller sending the router's
// raw NDM interface name (the old arg shape, or a tunnel_id crafted to look
// like one) must never reach the agent. There is no tunnel_ISP event on this
// router, so "ISP" is not a resolvable tunnel_id -- neither spelling of the
// request may enqueue anything.
func TestMiniappCommandTunnelRestartRejectsRawNDMSName(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg12", "Wireguard0")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	bodies := []string{
		`{"action":"tunnel_restart","args":{"ndms_name":"ISP"}}`,
		`{"action":"tunnel_restart","args":{"tunnel_id":"ISP"}}`,
	}
	for _, body := range bodies {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", ownedID), bytes.NewReader([]byte(body)))
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: want 400, got %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("neither request may enqueue anything: %+v", sink.enqueued)
	}
}

// A tunnel_id that is real, but belongs to a DIFFERENT router, must not
// resolve here -- otherwise a caller who happens to know a sibling router's
// tunnel id could bounce an interface on a router they administer, using
// topology from a router they don't.
func TestMiniappCommandTunnelRestartRejectsOtherRoutersTunnelID(t *testing.T) {
	d, ownedID, otherID, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, otherID, "awg12", "Wireguard0")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	body := `{"action":"tunnel_restart","args":{"tunnel_id":"awg12"}}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", ownedID), bytes.NewReader([]byte(body)))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a tunnel_id that belongs to a different router, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("must not enqueue: %+v", sink.enqueued)
	}
}

func TestMiniappCommandTunnelRestartOpkgTunnelWithoutNDMSName(t *testing.T) {
	// Половина туннелей живого роутера -- opkg, у них ndms_name нет вовсе.
	// Раньше мини-апп отвечал 400 "unknown_tunnel" на существующий туннель.
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg11", "")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	body := `{"action":"tunnel_restart","args":{"tunnel_id":"awg11"}}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", ownedID), bytes.NewReader([]byte(body)))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 {
		t.Fatalf("enqueued = %+v", sink.enqueued)
	}
	if sink.enqueued[0].Args["tunnel_id"] != "awg11" {
		t.Errorf("args = %+v, want tunnel_id", sink.enqueued[0].Args)
	}
	if _, present := sink.enqueued[0].Args["ndms_name"]; present {
		t.Errorf("args must carry no ndms_name for an opkg tunnel: %+v", sink.enqueued[0].Args)
	}
}

func TestMiniappCommandTunnelRestartPassesServerResolvedNDMSName(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg20", "Wireguard0")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	body := `{"action":"tunnel_restart","args":{"tunnel_id":"awg20"}}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", ownedID), bytes.NewReader([]byte(body)))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	// ndms_name по-прежнему приходит ТОЛЬКО от сервера: агенту он нужен как
	// откат на сборках без /api/control/restart.
	if sink.enqueued[0].Args["ndms_name"] != "Wireguard0" || sink.enqueued[0].Args["tunnel_id"] != "awg20" {
		t.Errorf("args = %+v", sink.enqueued[0].Args)
	}
}

func TestMiniappCommandTunnelRestartUnknownTunnelStillRejected(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg11", "")
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	body := `{"action":"tunnel_restart","args":{"tunnel_id":"awg99"}}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", ownedID), bytes.NewReader([]byte(body)))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Errorf("nothing must be enqueued: %+v", sink.enqueued)
	}
}

// Управление маршрутами: шесть готовых действий агента плюс новое
// route_policy_promote. Каждое router-local; add/delete идут через план с
// хешем черновика, rebind -- с превью и результатом по категориям.
func TestMiniappAllowsRouteManagement(t *testing.T) {
	for _, action := range []string{
		"route_templates", "route_add_plan", "route_add",
		"route_delete_plan", "route_delete", "route_rebind",
		"route_policy_promote",
		// Обслуживание (фаза D1). Четыре читающих: версии, два доктора и
		// разовый прогон проверки связи. Аргументов не берут вовсе.
		"version_audit", "router_doctor", "hrneo_doctor", "pingcheck_now",
		// Три мутирующих, все router-local и обратимые своей же парой.
		// Радиус ограничен тем же резолвером, что у tunnel_restart: клиент
		// присылает tunnel_id, ndms_name сервер достаёт из событий этого
		// роутера, и присланный клиентом никогда не доезжает до агента.
		"tunnel_enable", "tunnel_disable", "pingcheck_toggle",
		// Обмен по туннелю (фаза F): читающее, ряд ведёт сам роутер.
		"tunnel_traffic",
		// Прошивка (фаза D2): чтение -- всем с доступом, установка -- только
		// владельцу (проверяется отдельно, miniappOwnerOnlyActions).
		"firmware_status", "firmware_install",
	} {
		if !miniappCommandAllowlist[action] {
			t.Errorf("%s должен быть разрешён мини-аппу", action)
		}
	}
}

// Радиус поражения этих действий шире одного роутера либо необратим, и фаза
// управления маршрутами их не открывает. Список закреплён тестом, потому что
// «добавить ещё одно, раз уж рядом» -- самый частый способ потерять границу.
func TestMiniappStillDeniesDangerousActions(t *testing.T) {
	// firmware_install ушла отсюда в фазе D2 -- не потому, что стала
	// безопаснее, а потому, что для неё завели отдельную границу: только
	// владелец роутера (не оператор, не «кто-то с доступом») и подтверждение
	// набором имени роутера вручную. Радиус её как был -- само устройство и
	// перезагрузка, так и остался, и без обеих защит она сюда вернётся.
	for _, action := range []string{
		"tunnel_delete", "dns_reset", "update_backend_url", "tunnel_import",
		"opkg_upgrade", "self_update", "update_agent_config",
		"service_restart", "entware_clean_run",
	} {
		if miniappCommandAllowlist[action] {
			t.Errorf("%s не должен быть доступен мини-аппу", action)
		}
	}
}
