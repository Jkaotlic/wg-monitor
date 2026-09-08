package backend

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
)

func linkRepairDeps(t *testing.T) (Deps, int64, int64) {
	t.Helper()
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg11", "")
	sink := &replaceSink{}
	store := provision.NewStore()
	eng := &replace.Deps{
		Store:          store,
		Commands:       sink,
		Cabinet:        stubCabinet{},
		BaseCtx:        context.Background(),
		AwaitStep:      50 * time.Millisecond,
		HandshakeTries: 1,
		HandshakeWait:  time.Millisecond,
		Sleep:          func(context.Context, time.Duration) {},
	}
	repair := &linkrepair.Deps{
		Store:      store,
		Replace:    *eng,
		Origin:     LinkRepairOrigin(d),
		Attempts:   linkrepair.Attempts{KV: d.KV()},
		AutoRepair: func(int64) bool { return true },
		Commands:   sink,
		BaseCtx:    context.Background(),
		AwaitStep:  50 * time.Millisecond,
	}
	return Deps{
		DB:                  d,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		CommandSink:         sink,
		Replace:             eng,
		LinkRepair:          repair,
	}, ownedID, telegramUserID
}

func doRepair(t *testing.T, h http.Handler, method, path string, tgUser int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tgUser))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Пока идёт замена конфига, починку начинать нельзя -- замок общий.
func TestMiniappRepair_ConflictWithReplace(t *testing.T) {
	d, routerID, tgUser := linkRepairDeps(t)
	u, err := d.DB.Users().GetByID(routerID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !d.LinkRepair.Store.TryLock(u.Nickname) {
		t.Fatal("замок должен браться")
	}

	rr := doRepair(t, NewMux(d), http.MethodPost,
		fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), tgUser,
		`{"check_name":"tunnel_awg11"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("код %d, хотим 409: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "уже идёт") {
		t.Fatalf("отказ обязан объясняться словами: %s", rr.Body.String())
	}
}

// Нечинибельная поломка -- 422 со словами, а не 500 и не молчание.
func TestMiniappRepair_NoScenario(t *testing.T) {
	d, routerID, tgUser := linkRepairDeps(t)
	rr := doRepair(t, NewMux(d), http.MethodPost,
		fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), tgUser,
		`{"check_name":"external_reach"}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код %d, хотим 422: %s", rr.Code, rr.Body.String())
	}
}

func TestMiniappRepair_AutoToggle(t *testing.T) {
	d, routerID, tgUser := linkRepairDeps(t)
	rr := doRepair(t, NewMux(d), http.MethodPut,
		fmt.Sprintf("/v1/miniapp/routers/%d/repair/auto", routerID), tgUser,
		`{"enabled":false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код %d, хотим 200: %s", rr.Code, rr.Body.String())
	}
	on, err := d.DB.RepairSettings().AutoRepair(routerID)
	if err != nil {
		t.Fatalf("AutoRepair: %v", err)
	}
	if on {
		t.Fatal("выключение полуавтомата обязано доехать до базы")
	}
}

// Чужой роутер не виден даже для отказа: 404, а не 409 и не 422.
func TestMiniappRepair_StrangerGets404(t *testing.T) {
	d, routerID, _ := linkRepairDeps(t)
	rr := doRepair(t, NewMux(d), http.MethodPost,
		fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), 4242,
		`{"check_name":"tunnel_awg11"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("код %d, хотим 404", rr.Code)
	}
}
