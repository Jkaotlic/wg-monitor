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
