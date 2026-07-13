package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestMiniappSessionHandler(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	now := time.Now().UTC()
	raw := signTestInitData(t, "test-bot-token", map[string]string{
		"auth_date": strconv.FormatInt(now.Add(-1*time.Minute).Unix(), 10),
		"user":      `{"id":999,"first_name":"Admin","username":"admin_tg"}`,
	})
	body, _ := json.Marshal(miniappSessionReq{InitData: raw})

	req := httptest.NewRequest(http.MethodPost, "/v1/miniapp/session", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappSessionResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.IsAdmin || resp.TelegramUserID != 999 {
		t.Fatalf("resp = %+v, want admin=true telegram_user_id=999", resp)
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("expected session cookie to be set")
	}
}

func TestMiniappSessionHandlerRejectsBadInitData(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token"})

	body, _ := json.Marshal(miniappSessionReq{InitData: "not-valid"})
	req := httptest.NewRequest(http.MethodPost, "/v1/miniapp/session", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMiniappRoutersRequiresSession(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token"})

	req := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without session, got %d", rec.Code)
	}
}

func TestMiniappRoutesNotRegisteredWithoutBotToken(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := NewMux(Deps{DB: d})

	req := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 when TelegramBotToken is empty, got %d", rec.Code)
	}
}

func seedMiniappFleet(t *testing.T) (*db.DB, int64, int64, int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ownedID, err := d.Users().Insert("router-owned", "tok-owned-0000000000000000000000000000000000000000000000000000", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatalf("insert owned: %v", err)
	}
	if err := d.Users().SetTelegramUserID(ownedID, 100); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	otherID, err := d.Users().Insert("router-other", "tok-other-0000000000000000000000000000000000000000000000000000", "1.1.1.2", "awg11")
	if err != nil {
		t.Fatalf("insert other: %v", err)
	}
	if err := d.Users().SetTelegramUserID(otherID, 200); err != nil {
		t.Fatalf("set other owner: %v", err)
	}
	return d, ownedID, otherID, 100
}

func miniappSessionCookieFor(t *testing.T, botToken string, telegramUserID int64) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/miniapp/session", nil)
	return miniappSessionCookie(req, botToken, telegramUserID)
}

func TestMiniappRoutersListScopedToOwnedRouter(t *testing.T) {
	d, ownedID, otherID, telegramUserID := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappRoutersResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Routers) != 1 || resp.Routers[0].ID != ownedID {
		t.Fatalf("routers = %+v, want exactly [%d]", resp.Routers, ownedID)
	}
	_ = otherID
}

func TestMiniappRoutersListAdminSeesAll(t *testing.T) {
	d, ownedID, otherID, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	req := httptest.NewRequest(http.MethodGet, "/v1/miniapp/routers", nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappRoutersResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, r := range resp.Routers {
		got[r.ID] = true
	}
	if !got[ownedID] || !got[otherID] || len(got) != 2 {
		t.Fatalf("admin routers = %+v, want both %d and %d", resp.Routers, ownedID, otherID)
	}
}
