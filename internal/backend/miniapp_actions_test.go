package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
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

// recordingMiniappTG записывает, в какие чаты синхронизация тревоги писала.
type recordingMiniappTG struct {
	mu      sync.Mutex
	edits   map[int64]int
	sends   map[int64]int
	markups map[int64]*tg.InlineKeyboardMarkup // последняя разметка EditMessageReplyMarkup на чат
}

func (f *recordingMiniappTG) EditMessageReplyMarkup(_ context.Context, chatID, _ int64, kb *tg.InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits[chatID]++
	if f.markups != nil {
		f.markups[chatID] = kb
	}
	return nil
}

func (f *recordingMiniappTG) SendMessage(_ context.Context, chatID int64, _ *int64, _, _ string, _ *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends[chatID]++
	return 1, nil
}

// Админ получил тревогу, потом выключил уведомления по роутеру. Владелец
// действует из приложения: админу -- ни правки, ни «(через приложение)»,
// владельцу -- как раньше (spec D: выключивший не получает по роутеру ничего;
// final review M2).
func TestMiniappSyncSkipsChatsThatMutedRouter(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	const adminTG = 999
	seedHardIncident(t, d, ownedID, "dns")
	if err := d.AlertMessages().Put(ownedID, "dns", ownerTG, 501); err != nil {
		t.Fatal(err)
	}
	if err := d.AlertMessages().Put(ownedID, "dns", adminTG, 502); err != nil {
		t.Fatal(err)
	}
	if err := d.NotifyMutes().SetMuted(adminTG, ownedID, true); err != nil {
		t.Fatal(err)
	}
	fake := &recordingMiniappTG{edits: map[int64]int{}, sends: map[int64]int{}}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: adminTG, MiniappTG: fake})

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/dns/ack", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", ownerTG))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.edits[adminTG] != 0 || fake.sends[adminTG] != 0 {
		t.Fatalf("админ выключил уведомления, но синхронизация ему писала: edits=%d sends=%d", fake.edits[adminTG], fake.sends[adminTG])
	}
	if fake.edits[ownerTG] != 1 || fake.sends[ownerTG] != 1 {
		t.Fatalf("владелец обязан получить синхронизацию: edits=%d sends=%d", fake.edits[ownerTG], fake.sends[ownerTG])
	}
}

// B7a: владелец действует из мини-аппа -- miniappSyncAlertMessage снимает
// кнопки со всех сообщений тревоги, включая админское. У всех, кроме
// админа, это правильно очищает клавиатуру целиком (miniappEmptyKeyboard).
// У админа под тревогой всегда есть ряд «Не писать мне про этот роутер»
// (notify.withAdminMuteRow, добавляется при рассылке) -- его синхронизация
// не должна стирать, иначе админ теряет кнопку выключения, пока владелец
// сам не откроет и не закроет тревогу в приложении.
func TestMiniappSyncKeepsAdminMuteRowInsteadOfEmptyKeyboard(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	const adminTG = 999
	seedHardIncident(t, d, ownedID, "dns")
	if err := d.AlertMessages().Put(ownedID, "dns", ownerTG, 501); err != nil {
		t.Fatal(err)
	}
	if err := d.AlertMessages().Put(ownedID, "dns", adminTG, 502); err != nil {
		t.Fatal(err)
	}
	fake := &recordingMiniappTG{edits: map[int64]int{}, sends: map[int64]int{}, markups: map[int64]*tg.InlineKeyboardMarkup{}}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: adminTG, MiniappTG: fake})

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/incidents/dns/ack", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", ownerTG))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	ownerKB := fake.markups[ownerTG]
	if ownerKB == nil || len(ownerKB.InlineKeyboard) != 0 {
		t.Fatalf("у владельца клавиатура обязана стать пустой: %+v", ownerKB)
	}
	adminKB := fake.markups[adminTG]
	if adminKB == nil || len(adminKB.InlineKeyboard) != 1 {
		t.Fatalf("у админа должен остаться ровно один ряд (выключения): %+v", adminKB)
	}
	row := adminKB.InlineKeyboard[0]
	if len(row) != 1 || row[0].Text != notify.AdminMuteButtonText || row[0].CallbackData != notify.AdminMuteCallbackData(ownedID) {
		t.Fatalf("у админа не ряд выключения этого роутера: %+v", adminKB)
	}
}
