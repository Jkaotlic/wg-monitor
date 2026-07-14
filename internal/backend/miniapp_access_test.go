package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiniappAccessReadComposesOwnerAndOperators(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	if err := d.RouterOperators().Add(ownedID, 555, 999); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/access", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999)) // admin
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappAccessResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Owner == nil || resp.Owner.TelegramUserID != 100 {
		t.Fatalf("owner = %+v, want telegram_user_id 100", resp.Owner)
	}
	if len(resp.Operators) != 1 || resp.Operators[0].TelegramUserID != 555 || resp.Operators[0].GrantedBy != 999 {
		t.Fatalf("operators = %+v, want one op tgid=555 granted_by=999", resp.Operators)
	}
}

func TestMiniappAccessReadForbiddenForNonAdmin(t *testing.T) {
	d, ownedID, _, ownerTGID := seedMiniappFleet(t) // ownerTGID=100, owner (not admin)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/access", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", ownerTGID)) // owner, not admin
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 for non-admin, got %d", rec.Code)
	}
}

func TestMiniappAccessReadForbiddenBeforeExistenceCheck(t *testing.T) {
	d, _, _, ownerTGID := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	// Non-admin hitting a nonexistent router id must get 403 (admin gate first),
	// never a 404 that would confirm the router doesn't exist.
	req := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers/999999/access", nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", ownerTGID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 (admin gate before existence), got %d", rec.Code)
	}
}

func TestMiniappAccessReadUnknownRouterForAdmin404(t *testing.T) {
	d, _, _, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers/999999/access", nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999)) // admin
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for admin+unknown router, got %d", rec.Code)
	}
}

func TestMiniappRouterDetailOmitsActiveIncidentsOnRouter(t *testing.T) {
	d, ownedID, _, ownerTGID := seedMiniappFleet(t)
	// Seed a HARD incident so active_incidents WOULD be non-empty without the
	// cleanup — otherwise omitempty hides it regardless and the test is vacuous.
	// seedHardIncident is defined in miniapp_actions_test.go (same package).
	seedHardIncident(t, d, ownedID, "tunnel_a")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", ownerTGID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// active_incidents must be ABSENT from router (nil'd + omitempty), while the
	// enriched top-level incidents IS present (supersedes it on the detail).
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	router, _ := raw["router"].(map[string]any)
	if _, present := router["active_incidents"]; present {
		t.Errorf("router.active_incidents should be omitted on the detail, got: %v", router["active_incidents"])
	}
	incidents, _ := raw["incidents"].([]any)
	if len(incidents) == 0 {
		t.Errorf("expected the enriched top-level incidents to be present and non-empty")
	}
}

func TestMiniappAddOperatorPersistsAndReturnsAccess(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	body := `{"telegram_user_id": 555}`
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/access/operators", ownedID), strings.NewReader(body))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappAccessResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Operators) != 1 || resp.Operators[0].TelegramUserID != 555 {
		t.Fatalf("operators = %+v, want tgid 555", resp.Operators)
	}
	// Persistence + granted_by = admin.
	ops, _ := d.RouterOperators().List(ownedID)
	if len(ops) != 1 || ops[0].TelegramUserID != 555 || ops[0].GrantedBy != 999 {
		t.Fatalf("persisted ops = %+v", ops)
	}
}

func TestMiniappAddOperatorRejectsBadID(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/access/operators", ownedID), strings.NewReader(`{"telegram_user_id": 0}`))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for id<=0, got %d", rec.Code)
	}
}

func TestMiniappAddOperatorForbiddenForNonAdmin(t *testing.T) {
	d, ownedID, _, ownerTGID := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/access/operators", ownedID), strings.NewReader(`{"telegram_user_id": 555}`))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", ownerTGID)) // owner, not admin
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestMiniappRemoveOperator(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	_ = d.RouterOperators().Add(ownedID, 555, 999)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/v1/miniapp/routers/%d/access/operators/555", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	ops, _ := d.RouterOperators().List(ownedID)
	if len(ops) != 0 {
		t.Fatalf("operator not removed: %+v", ops)
	}
}

func TestMiniappUnbindOwner(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t) // owned by TG 100
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/v1/miniapp/routers/%d/access/owner", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappAccessResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Owner != nil {
		t.Fatalf("owner should be nil after unbind, got %+v", resp.Owner)
	}
	u, _ := d.Users().GetByID(ownedID)
	if u.TelegramUserID != nil {
		t.Fatalf("users.telegram_user_id should be NULL after unbind, got %v", *u.TelegramUserID)
	}
}
