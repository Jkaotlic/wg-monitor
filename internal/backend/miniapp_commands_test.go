package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
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
		// Включение/выключение по идентификатору: работает и там, где ndmc
		// бессилен (opkg-туннель без имени в NDMS).
		"tunnel_power",
		// «Куда пойдёт сайт»: читающее, до агента доезжает только имя сайта
		// (явная ветка sanitizeWizardCommandArgs).
		"route_lookup",
		// Правка конфига агента (решение оператора № 3, отменяет D4
		// программы мини-аппа). Ушла из denied не потому, что стала
		// безопаснее, а потому, что для неё завели три границы сразу:
		// только админ (miniappAdminOnlyActions), пол версии агента с
		// отказом по умолчанию (miniappActionMinAgentVersion) и
		// подтверждение набором имени роутера на экране. Аргументы
		// проверяет уже написанная ветка sanitizeAgentConfigArgs -- закрытый
		// whitelist полей, куда backend.url не входит намеренно: перенаправить
		// адрес бэкенда значит захватить весь парк.
		"agent_config_get", "update_agent_config",
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
// «Куда пойдёт сайт» открыт мини-аппу, и до агента доезжает только имя сайта
// в одном виде: всё прочее, что пришлёт клиент, отброшено, а негодное имя не
// ставит в очередь ничего.
func TestMiniappCommandsRouteLookupQueuesOnlyDomain(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", ownedID), bytes.NewReader([]byte(body)))
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := post(`{"action":"route_lookup","args":{"domain":" Claude.AI. ","tunnel_id":"awg12","ndms_name":"Wireguard0"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 {
		t.Fatalf("в очереди %d команд, ожидалась одна", len(sink.enqueued))
	}
	cmd := sink.enqueued[0]
	if cmd.Action != "route_lookup" || len(cmd.Args) != 1 || cmd.Args["domain"] != "claude.ai" {
		t.Fatalf("агенту ушло %s %v, ожидалось ровно {domain: claude.ai}", cmd.Action, cmd.Args)
	}

	rec = post(`{"action":"route_lookup","args":{"domain":"x.com/path"}}`)
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("invalid_domain")) {
		t.Fatalf("want 400 invalid_domain, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 {
		t.Fatal("негодное имя сайта ушло в очередь")
	}
}

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
		// Включение/выключение по идентификатору: работает и там, где ndmc
		// бессилен (opkg-туннель без имени в NDMS).
		"tunnel_power",
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
	// update_agent_config ушла отсюда тем же порядком и по тому же образцу:
	// границ у неё три -- только админ бота, пол версии агента с отказом по
	// умолчанию и подтверждение набором имени роутера на экране. Радиус её
	// как был -- конфиг агента и его перезапуск, так и остался, и без любой
	// из трёх защит она сюда вернётся. Тест на каждую лежит рядом:
	// TestMiniappAgentConfigDeniedToOwnerAndOperator,
	// TestMiniappRefusesDangerousActionToOldAgent и agentConfig.test.js.
	//
	// update_backend_url не уезжает НИКОГДА: его белый список живёт на
	// стороне агента, и перенаправление адреса бэкенда -- захват всего парка.
	for _, action := range []string{
		"tunnel_delete", "dns_reset", "update_backend_url", "tunnel_import",
		"opkg_upgrade", "self_update",
		"service_restart", "entware_clean_run",
	} {
		if miniappCommandAllowlist[action] {
			t.Errorf("%s не должен быть доступен мини-аппу", action)
		}
	}
}

// miniappAgentConfigPost -- один вызов команды правки конфига от лица
// конкретного человека. Отдельный помощник, потому что все три теста ниже
// спрашивают одно: что ответил сервер и что после этого легло в очередь.
func miniappAgentConfigPost(t *testing.T, h http.Handler, routerID, telegramUserID int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/commands", routerID), bytes.NewReader([]byte(body)))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ПЕРВАЯ из двух независимых преград (решение оператора п. 10): бэкенд не
// ставит команду в очередь агенту ниже пола версии.
//
// Отказ приходит ДО очереди, а не в её ответе: старый агент не знает про
// новые поля, перепишет config.yaml по своим правилам и перезапустит себя.
// «Принято» о таком было бы обещанием того, что не случится.
func TestMiniappRefusesDangerousActionToOldAgent(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.30.1"); err != nil {
		t.Fatal(err)
	}
	rec := miniappAgentConfigPost(t, h, ownedID, 999, `{"action":"update_agent_config","args":{"interval_sec":300}}`)
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte("agent_too_old")) {
		t.Fatalf("агент v0.30.1: код %d тело %s, ожидался 409 agent_too_old", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("команда старому агенту всё-таки встала в очередь: %+v", sink.enqueued)
	}

	// Версия не сообщалась вовсе -- тот же отказ: «не знаю версию» означает
	// «не знаю, что случится».
	if _, err := d.SQL().Exec(`UPDATE users SET last_deployed_version=NULL WHERE id=?`, ownedID); err != nil {
		t.Fatal(err)
	}
	rec = miniappAgentConfigPost(t, h, ownedID, 999, `{"action":"update_agent_config","args":{"interval_sec":300}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("версия неизвестна: код %d тело %s, ожидался 409", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("команда агенту без версии встала в очередь: %+v", sink.enqueued)
	}

	// Чтение конфига агент умеет с давних версий: пол версии на него не
	// распространяется, иначе экран закрылся бы исправным роутерам.
	rec = miniappAgentConfigPost(t, h, ownedID, 999, `{"action":"agent_config_get"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("чтение конфига у старого агента: код %d тело %s, ожидался 202", rec.Code, rec.Body.String())
	}

	// А на агенте от пола и выше та же запись уходит -- без этой половины
	// тест был бы зелёным и на экране, который не работает ни для кого.
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.31.0"); err != nil {
		t.Fatal(err)
	}
	rec = miniappAgentConfigPost(t, h, ownedID, 999, `{"action":"update_agent_config","args":{"interval_sec":300}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("агент v0.31.0: код %d тело %s, ожидался 202", rec.Code, rec.Body.String())
	}
	var wrote bool
	for _, cmd := range sink.enqueued {
		if cmd.Action == "update_agent_config" {
			wrote = true
			if cmd.Args["interval_sec"] != 300 {
				t.Errorf("агенту ушло %v, ожидался interval_sec=300", cmd.Args)
			}
		}
	}
	if !wrote {
		t.Fatalf("на агенте от пола и выше запись в очередь не встала: %+v", sink.enqueued)
	}
}

// Радиус правки конфига router-global, поэтому круг -- только админ бота.
// Отказ приходит как 404 not_found (как у остальных админских срезов
// мини-аппа), а не 403: владельцу роутера незачем узнавать по коду ответа,
// что действие вообще существует.
func TestMiniappAgentConfigDeniedToOwnerAndOperator(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	if err := d.RouterOperators().Add(ownedID, 555, 999); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.31.0"); err != nil {
		t.Fatal(err)
	}
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	for _, who := range []struct {
		name string
		tgID int64
	}{{"владелец", ownerTG}, {"оператор", 555}} {
		for _, body := range []string{
			`{"action":"agent_config_get"}`,
			`{"action":"update_agent_config","args":{"interval_sec":300}}`,
		} {
			rec := miniappAgentConfigPost(t, h, ownedID, who.tgID, body)
			if rec.Code != http.StatusNotFound || !bytes.Contains(rec.Body.Bytes(), []byte("not_found")) {
				t.Errorf("%s, %s: код %d тело %s, ожидался 404 not_found", who.name, body, rec.Code, rec.Body.String())
			}
		}
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("не-админ поставил команду в очередь: %+v", sink.enqueued)
	}

	// Админу -- проходит: без этой половины тест был бы зелёным и на экране,
	// закрытом вообще для всех.
	rec := miniappAgentConfigPost(t, h, ownedID, 999, `{"action":"update_agent_config","args":{"interval_sec":300}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("админ: код %d тело %s, ожидался 202", rec.Code, rec.Body.String())
	}

	// Адрес панели роутера приезжает на экран ТОЛЬКО в ответе агента на
	// agent_config_get -- в срезах /v1/miniapp/* его нет вовсе. Значит
	// граница по роли обязана стоять и на опросе результата, а не только на
	// постановке команды: иначе владелец, у которого есть идентификатор
	// команды, дочитал бы адрес панели из чужого ответа. Решение
	// координатора: админу адрес показываем (он и так открыт ему в сводке
	// дашборда), владельцу и оператору он не приходит ВОВСЕ -- не «приходит
	// и не рисуется».
	rec = miniappAgentConfigPost(t, h, ownedID, 999, `{"action":"agent_config_get"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("админ, чтение конфига: код %d тело %s, ожидался 202", rec.Code, rec.Body.String())
	}
	var issued struct {
		CmdID string `json:"cmd_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	const panelURL = "http://198.51.100.7:8080"
	// Так это помнит настоящая очередь: результат существует только у
	// команды, которую агент забрал (RecordResult отвергает результат
	// невыданной команды), поэтому действие по идентификатору восстановимо.
	sink.commands = map[string]wire.Command{
		issued.CmdID: {ID: issued.CmdID, Action: "agent_config_get"},
	}
	sink.results = map[string]wire.CommandResult{
		issued.CmdID: {
			ID:     issued.CmdID,
			Status: "ok",
			Output: `{"config_kind":"agent","awgm_base_url":"` + panelURL + `","awgm_login":"admin"}`,
		},
	}
	poll := func(telegramUserID int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/v1/miniapp/routers/%d/commands/%s?wait_sec=0", ownedID, issued.CmdID), nil)
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	for _, who := range []struct {
		name string
		tgID int64
	}{{"владелец", ownerTG}, {"оператор", 555}} {
		got := poll(who.tgID)
		if got.Code != http.StatusNotFound {
			t.Errorf("%s, опрос результата: код %d тело %s, ожидался 404", who.name, got.Code, got.Body.String())
		}
		// Ни значения, ни имени поля: имя поля в ответе означает, что
		// значение приедет туда завтра.
		for _, secret := range []string{panelURL, "198.51.100.7", "awgm_base_url"} {
			if bytes.Contains(got.Body.Bytes(), []byte(secret)) {
				t.Errorf("%s получил %q в ответе опроса: %s", who.name, secret, got.Body.String())
			}
		}
	}
	// Админу тот же результат приходит целиком -- иначе тест был бы зелёным
	// и на экране, который не работает ни для кого.
	mine := poll(999)
	if mine.Code != http.StatusOK || !bytes.Contains(mine.Body.Bytes(), []byte(panelURL)) {
		t.Fatalf("админ, опрос результата: код %d тело %s, ожидались 200 и адрес панели", mine.Code, mine.Body.String())
	}
}

// Соседний срез -- /v1/dashboard/summary -- отдаёт ssh, креды панели и чат
// уведомлений. Экран правки конфига читает настройки роутера, и форму оттуда
// копировать нельзя: поля переписываются поимённо. Тест сторожит, что копии
// не случилось ни в одном срезе, который читает этот экран.
func TestMiniappCommandScreensNeverLeakRouterSecrets(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	const (
		awgmURL     = "https://panel.example.com"
		awgmAuth    = "Basic ZXhhbXBsZQ=="
		sshHost     = "198.51.100.20"
		expectedMAC = "02:00:00:00:00:01"
		chatID      = -1009876543210
	)
	if _, err := d.SQL().Exec(
		`UPDATE users SET awgm_url=?, awgm_auth=?, ssh_host=?, ssh_user=?, expected_mac=?, telegram_chat_id=? WHERE id=?`,
		awgmURL, awgmAuth, sshHost, "root", expectedMAC, chatID, ownedID); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.31.0"); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: &dashboardActionSink{}})

	for _, path := range []string{
		fmt.Sprintf("/v1/miniapp/routers/%d/settings", ownedID),
		fmt.Sprintf("/v1/miniapp/routers/%d", ownedID),
		"/v1/miniapp/routers",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: код %d тело %s", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.Bytes()
		for _, secret := range []string{awgmURL, awgmAuth, sshHost, expectedMAC, "-1009876543210"} {
			if bytes.Contains(body, []byte(secret)) {
				t.Errorf("%s: утекло значение %q: %s", path, secret, body)
			}
		}
		// Ни значений, ни имён полей: имя поля в ответе означает, что
		// значение приедет туда завтра, когда его кто-нибудь заполнит.
		for _, field := range []string{"awgm_url", "awgm_auth", "panel_host", "ssh_host", "ssh_user", "expected_mac", "ndms_name", "telegram_chat_id"} {
			if bytes.Contains(body, []byte(field)) {
				t.Errorf("%s: есть поле %q -- секреты уедут в него завтра", path, field)
			}
		}
	}
}

// miniappRealQueueFleet -- парк с НАСТОЯЩЕЙ очередью вместо тестового стока.
// Нужна там, где проверяется не ответ хендлера на подсунутые данные, а
// поведение на стыке с очередью: срок жизни записей у неё свой, и тестовый
// сток об этом ничего не знает.
func miniappRealQueueFleet(t *testing.T) (*db.DB, int64, int64, *cmdpkg.Queue, http.Handler) {
	t.Helper()
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	if err := d.RouterOperators().Add(ownedID, 555, 999); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.31.0"); err != nil {
		t.Fatal(err)
	}
	q := cmdpkg.New()
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: q})
	return d, ownedID, ownerTG, q, h
}

// miniappPollResult -- опрос результата команды от лица конкретного человека.
func miniappPollResult(t *testing.T, h http.Handler, routerID, telegramUserID int64, cmdID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/v1/miniapp/routers/%d/commands/%s?wait_sec=0", routerID, cmdID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Граница по роли на опросе результата не должна зависеть от записи о выдаче
// команды.
//
// Queue.Sweep чистит issued по issuedAt, а results по recordedAt ОДНИМ
// cutoff, и issuedAt всегда раньше recordedAt: результат записывается после
// выдачи. Значит запись о выдаче вымётывается ПЕРВОЙ, и между ними есть окно
// -- действие уже «неизвестно», а результат ещё жив. У спящего мобильного
// роутера это окно длиной в задержку ответа, то есть минуты, при Sweep(1h) в
// проде.
//
// Если гейт роли опирается на «действие известно», в этом окне он
// превращается в разрешение по умолчанию, и владелец с идентификатором
// команды читает адрес панели из админского ответа. Поэтому действие
// хранится рядом с результатом и живёт ровно столько же.
func TestMiniappResultRoleGateSurvivesSweep(t *testing.T) {
	d, ownedID, ownerTG, q, h := miniappRealQueueFleet(t)
	_ = d

	// Админ читает конфиг агента.
	rec := miniappAgentConfigPost(t, h, ownedID, 999, `{"action":"agent_config_get"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("админ, чтение конфига: код %d тело %s, ожидался 202", rec.Code, rec.Body.String())
	}
	var issued struct {
		CmdID string `json:"cmd_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	// Агент забирает команду -- только теперь она «выдана».
	got, ok := q.Dequeue(context.Background(), ownedID, 0)
	if !ok || got.ID != issued.CmdID {
		t.Fatalf("агент не забрал команду: %+v ok=%v", got, ok)
	}

	// Задержка ответа агента: именно она делает issuedAt раньше recordedAt.
	time.Sleep(100 * time.Millisecond)
	const panelURL = "http://198.51.100.7:8080"
	if err := q.RecordResult(ownedID, wire.CommandResult{
		ID:     issued.CmdID,
		Status: "ok",
		Output: `{"config_kind":"agent","awgm_base_url":"` + panelURL + `","awgm_login":"admin"}`,
	}); err != nil {
		t.Fatal(err)
	}

	// Cutoff между выдачей и ответом: запись о выдаче уходит, результат живёт.
	q.Sweep(50 * time.Millisecond)

	// Положительная половина, и она здесь обязательна: если бы Sweep унёс и
	// результат, отказ владельцу ничего не доказывал бы -- он пришёл бы как
	// result_not_ready, и тест был бы зелёным вхолостую.
	mine := miniappPollResult(t, h, ownedID, 999, issued.CmdID)
	if mine.Code != http.StatusOK || !bytes.Contains(mine.Body.Bytes(), []byte(panelURL)) {
		t.Fatalf("админ после Sweep: код %d тело %s, ожидались 200 и адрес панели (результат должен был выжить)",
			mine.Code, mine.Body.String())
	}

	for _, who := range []struct {
		name string
		tgID int64
	}{{"владелец", ownerTG}, {"оператор", 555}} {
		res := miniappPollResult(t, h, ownedID, who.tgID, issued.CmdID)
		if res.Code != http.StatusNotFound {
			t.Errorf("%s после Sweep: код %d тело %s, ожидался 404", who.name, res.Code, res.Body.String())
		}
		for _, secret := range []string{panelURL, "198.51.100.7", "awgm_base_url"} {
			if bytes.Contains(res.Body.Bytes(), []byte(secret)) {
				t.Errorf("%s после Sweep прочитал %q: %s", who.name, secret, res.Body.String())
			}
		}
	}
}

// Второй набор ролей -- miniappOwnerOnlyActions (firmware_install) -- на
// постановке проверяется (403 owner_only), и на опросе результата проверяется
// тоже.
//
// Решение записано здесь намеренно: сегодня вывод firmware_install -- строка
// «firmware install kicked; router will reboot», и «утечки нет» держится
// только на том, каким этот вывод оказался. Такое обоснование перестаёт быть
// правдой молча -- в тот день, когда агент начнёт возвращать в нём версию,
// путь к прошивке или причину отказа. Граница ставится по роли действия, а не
// по сегодняшней безобидности его вывода.
func TestMiniappFirmwareResultDeniedToOperator(t *testing.T) {
	_, ownedID, ownerTG, q, h := miniappRealQueueFleet(t)

	// Ставит владелец: установка прошивки оператору запрещена и на постановке.
	rec := miniappAgentConfigPost(t, h, ownedID, ownerTG, `{"action":"firmware_install"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("владелец, установка прошивки: код %d тело %s, ожидался 202", rec.Code, rec.Body.String())
	}
	var issued struct {
		CmdID string `json:"cmd_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Dequeue(context.Background(), ownedID, 0); !ok {
		t.Fatal("агент не забрал команду установки прошивки")
	}
	const outcome = "firmware install kicked; router will reboot"
	if err := q.RecordResult(ownedID, wire.CommandResult{ID: issued.CmdID, Status: "ok", Output: outcome}); err != nil {
		t.Fatal(err)
	}

	res := miniappPollResult(t, h, ownedID, 555, issued.CmdID)
	if res.Code != http.StatusForbidden || !bytes.Contains(res.Body.Bytes(), []byte("owner_only")) {
		t.Errorf("оператор, опрос результата прошивки: код %d тело %s, ожидался 403 owner_only", res.Code, res.Body.String())
	}
	if bytes.Contains(res.Body.Bytes(), []byte(outcome)) {
		t.Errorf("оператор прочитал вывод установки прошивки: %s", res.Body.String())
	}

	// Владельцу -- приходит: иначе тест был бы зелёным и на экране, закрытом
	// вообще для всех.
	mine := miniappPollResult(t, h, ownedID, ownerTG, issued.CmdID)
	if mine.Code != http.StatusOK || !bytes.Contains(mine.Body.Bytes(), []byte(outcome)) {
		t.Fatalf("владелец, опрос результата прошивки: код %d тело %s, ожидались 200 и вывод", mine.Code, mine.Body.String())
	}
}
