package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestOperationSummaryNeverIncludesArgsOrSecrets(t *testing.T) {
	summary := operationSummary("tunnel_restart", map[string]any{
		"endpoint": "198.51.100.1:51820", "token": "super-secret", "password": "also-secret",
	})
	if summary != "Перезапуск VPN-туннеля" {
		t.Fatalf("summary = %q", summary)
	}
	for _, secret := range []string{"198.51.100.1", "super-secret", "also-secret"} {
		if strings.Contains(summary, secret) {
			t.Fatalf("summary leaked %q", secret)
		}
	}
}

func TestMiniappOperationsAPIListsOnlyAllowedRouter(t *testing.T) {
	database, ownedID, otherID, telegramUserID := seedMiniappFleet(t)
	for _, routerID := range []int64{ownedID, otherID} {
		if err := database.Operations().Begin(db.Operation{ID: fmt.Sprintf("op-%d", routerID), RouterID: routerID, Action: "tunnel_restart", Source: "miniapp", StartedAt: time.Now(), Summary: "Перезапуск VPN-туннеля"}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewMux(Deps{DB: database, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/operations?limit=10", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Operations []publicOperation `json:"operations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Operations) != 1 || body.Operations[0].Action != "tunnel_restart" {
		t.Fatalf("operations=%+v", body.Operations)
	}
	for _, private := range []string{"actor_id", "request_id", "command_id", "operation_id", "router_id"} {
		if strings.Contains(rec.Body.String(), `"`+private+`"`) {
			t.Fatalf("public operation leaked %s: %s", private, rec.Body.String())
		}
	}
}

func TestBeginAndFinishOperationByCommand(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	uid, _ := database.Users().Insert("router", "token-op-service", "", "")
	ctx := context.WithValue(context.Background(), reqIDKey, "req-77")
	opID, err := BeginOperation(ctx, database, uid, 77, "tunnel_restart", "miniapp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AttachOperationCommand(database, opID, "cmd-77"); err != nil {
		t.Fatal(err)
	}
	if err := FinishOperationByCommand(database, "cmd-77", "ok", "command_completed", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := database.Operations().List(uid, 10, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got[0].RequestID != "req-77" || got[0].Outcome != "ok" || got[0].CommandID != "cmd-77" {
		t.Fatalf("operation=%+v", got[0])
	}
}
