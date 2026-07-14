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
)

// seedHardIncident adds a HARD incident for the owned router so mutating
// endpoints have something to act on.
func seedHardIncident(t *testing.T, d *db.DB, routerID int64, check string) {
	t.Helper()
	hardSince := time.Now().Add(-time.Hour).UTC()
	if err := d.State().Save(routerID, check, db.IncidentState{
		UserID: routerID, CheckName: check, CurrentStatus: "hard", HardSince: &hardSince,
	}); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
}

func TestMiniappSilencePersistsAndReturnsState(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedHardIncident(t, d, ownedID, "tunnel_a")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	body, _ := json.Marshal(map[string]string{"ttl": "1h"})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/tunnel_a/silence", ownedID), bytes.NewReader(body))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappIncidentResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Incident.SilencedUntil == nil {
		t.Fatal("expected silenced_until in response")
	}
	// Verify persistence.
	st, _ := d.State().Get(ownedID, "tunnel_a")
	if st.SilencedUntil == nil {
		t.Fatal("silence not persisted to DB")
	}
}

func TestMiniappSilenceRejectsBadTTL(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedHardIncident(t, d, ownedID, "tunnel_a")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	body, _ := json.Marshal(map[string]string{"ttl": "7h"})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/tunnel_a/silence", ownedID), bytes.NewReader(body))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for bad ttl, got %d", rec.Code)
	}
}

func TestMiniappSilenceDeniedForStranger(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	seedHardIncident(t, d, ownedID, "tunnel_a")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	body, _ := json.Marshal(map[string]string{"ttl": "1h"})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/tunnel_a/silence", ownedID), bytes.NewReader(body))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 777)) // unrelated TG user
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for stranger, got %d", rec.Code)
	}
}

func TestMiniappSilenceSyncIsBestEffort(t *testing.T) {
	// No MiniappTG wired and no LastAlertMsgID → the sync is skipped, but the
	// DB write must still land and the request must still succeed.
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedHardIncident(t, d, ownedID, "tunnel_a")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999}) // MiniappTG nil

	body, _ := json.Marshal(map[string]string{"ttl": "4h"})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/tunnel_a/silence", ownedID), bytes.NewReader(body))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with nil MiniappTG, got %d body=%s", rec.Code, rec.Body.String())
	}
	st, _ := d.State().Get(ownedID, "tunnel_a")
	if st.SilencedUntil == nil {
		t.Fatal("silence must persist even when TG sync is skipped")
	}
}

func TestMiniappAckPersists(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedHardIncident(t, d, ownedID, "dns")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/dns/ack", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappIncidentResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Incident.Acked {
		t.Fatal("expected acked=true in response")
	}
	st, _ := d.State().Get(ownedID, "dns")
	if !st.Acked {
		t.Fatal("ack not persisted")
	}
}

func TestMiniappMutePersistsSilencedUntil(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedHardIncident(t, d, ownedID, "tunnel_b")
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, MuteCutoffHour: 9})

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/tunnel_b/mute", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	st, _ := d.State().Get(ownedID, "tunnel_b")
	if st.SilencedUntil == nil {
		t.Fatal("mute did not set silenced_until")
	}
}

func TestMiniappHistoryReturnsTransitions(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	base := time.Now().Add(-2 * time.Hour)
	// ok → fail → ok, plus a duplicate that must be compressed away.
	_ = d.Events().Insert(ownedID, "tunnel_c", "ok", "{}", base)
	_ = d.Events().Insert(ownedID, "tunnel_c", "ok", "{}", base.Add(time.Minute))
	_ = d.Events().Insert(ownedID, "tunnel_c", "fail", "{}", base.Add(2*time.Minute))
	_ = d.Events().Insert(ownedID, "tunnel_c", "ok", "{}", base.Add(3*time.Minute))
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/tunnel_c/history", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappHistoryResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Transitions) != 3 {
		t.Fatalf("got %d transitions, want 3 (ok→fail→ok): %+v", len(resp.Transitions), resp.Transitions)
	}
	if resp.Transitions[0].Status != "ok" || resp.Transitions[1].Status != "fail" {
		t.Errorf("transition order = %+v", resp.Transitions)
	}
}

func TestMiniappHistoryDeniedForStranger(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/tunnel_c/history", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 777))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for stranger, got %d", rec.Code)
	}
}

func TestMiniappSilenceRejectsNonHardIncident(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	// Don't seed a hard incident; the check will have zero-value status "ok"
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	body, _ := json.Marshal(map[string]string{"ttl": "1h"})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/tunnel_a/silence", ownedID), bytes.NewReader(body))
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for non-hard incident, got %d body=%s", rec.Code, rec.Body.String())
	}
	// Verify no silenced_until was persisted
	st, _ := d.State().Get(ownedID, "tunnel_a")
	if st.SilencedUntil != nil {
		t.Fatal("unexpected silenced_until was persisted for non-hard incident")
	}
}
