package backend

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestMiniappCommandEnqueuesAllowedAction(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	sink := &dashboardActionSink{}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CommandSink: sink})

	// tunnel_restart requires a valid ndms_name arg (sanitizeWizardCommandArgs);
	// an empty/missing one is rejected before it ever reaches the sink.
	body := `{"action":"tunnel_restart","args":{"ndms_name":"Wireguard0"}}`
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
		t.Fatalf("bad command: %+v", sink.enqueued[0])
	}
}
