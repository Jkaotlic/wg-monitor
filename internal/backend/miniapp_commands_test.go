package backend

import (
	"bytes"
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
		"tunnel_enable",      // config change, not a fix
		"tunnel_disable",     // config change, not a fix
		"self_update",        // audited deploy flow
		"route_rebind",       // route mutation
		"tunnel_import",      // route/config mutation
		"dns_reset",          // router-global; stays on the dashboard
		"agent_config_get",
		"update_agent_config",
		"opkg_upgrade",
		"entware_clean_run",
		"router_doctor", // free-text diagnostic aimed at the admin, not an owner
	}
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

// seedMiniappTunnelEvent inserts a tunnel_<tunnelID> event for routerID whose
// details carry ndms_name -- the only way miniappResolveTunnelNDMSName can
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
