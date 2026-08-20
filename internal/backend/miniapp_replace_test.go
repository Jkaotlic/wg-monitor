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

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// replaceSink -- очередь команд, которая ничего не отвечает: задание при этом
// зависает на первом же шаге, и тесты ниже смотрят на границы запроса, а не
// на прогон целиком (он проверен в internal/backend/replace).
type replaceSink struct{ dashboardActionSink }

func (s *replaceSink) AwaitResult(ctx context.Context, _ int64, _ string, timeout time.Duration) (*wire.CommandResult, bool) {
	select {
	case <-ctx.Done():
	case <-time.After(timeout):
	}
	return nil, false
}

type stubCabinet struct{}

func (stubCabinet) IssueConfig(context.Context, int64, string, string) (replace.Issued, error) {
	return replace.Issued{TunnelName: "amnezia_nl", Conf: []byte("[Interface]\n")}, nil
}

func replaceDeps(t *testing.T) (Deps, int64, int64) {
	t.Helper()
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg11", "")
	sink := &replaceSink{}
	eng := &replace.Deps{
		Store:          provision.NewStore(),
		Commands:       sink,
		Cabinet:        stubCabinet{},
		BaseCtx:        context.Background(),
		AwaitStep:      50 * time.Millisecond,
		HandshakeTries: 1,
		HandshakeWait:  time.Millisecond,
		Sleep:          func(context.Context, time.Duration) {},
	}
	return Deps{
		DB:                  d,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		CommandSink:         sink,
		Replace:             eng,
	}, ownedID, telegramUserID
}

func postReplace(t *testing.T, h http.Handler, routerID, tgUser int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/replace", routerID), bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tgUser))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiniappReplaceStartsAndReportsProgress(t *testing.T) {
	deps, ownedID, tgUser := replaceDeps(t)
	h := NewMux(deps)

	rec := postReplace(t, h, ownedID, tgUser,
		`{"provider":"amnezia","option_id":"nl","old_tunnel_id":"awg11","policy_name":"HydraRoute"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var started miniappReplaceResp
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.JobID == "" || !started.Running {
		t.Fatalf("ответ = %+v", started)
	}

	// Статус спрашивается ПРО РОУТЕР: идентификатора задания у спрашивающего
	// может не быть -- операцию могли запустить с другого устройства.
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/replace", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tgUser))
	statusRec := httptest.NewRecorder()
	h.ServeHTTP(statusRec, req)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status: %d", statusRec.Code)
	}
	var status miniappReplaceResp
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.JobID != started.JobID || len(status.Steps) != 6 {
		t.Fatalf("статус = %+v", status)
	}
}

// Параллельные замены запрещены: вторая получает не ошибку валидации, а
// «уже идёт» -- и экран показывает идущую операцию вместо кнопки.
func TestMiniappReplaceRefusesSecondRun(t *testing.T) {
	deps, ownedID, tgUser := replaceDeps(t)
	h := NewMux(deps)

	if rec := postReplace(t, h, ownedID, tgUser, `{"provider":"amnezia","option_id":"nl","old_tunnel_id":"awg11","policy_name":"HydraRoute"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("первая замена: %d", rec.Code)
	}
	rec := postReplace(t, h, ownedID, tgUser, `{"provider":"amnezia","option_id":"se","old_tunnel_id":"awg11","policy_name":"HydraRoute"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Заменяемый туннель проверяется по событиям этого роутера -- выдуманный
// идентификатор не проходит.
func TestMiniappReplaceRejectsUnknownTunnel(t *testing.T) {
	deps, ownedID, tgUser := replaceDeps(t)
	h := NewMux(deps)

	rec := postReplace(t, h, ownedID, tgUser, `{"provider":"amnezia","option_id":"nl","old_tunnel_id":"awg99","policy_name":"HydraRoute"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMiniappReplaceRejectsUnknownProvider(t *testing.T) {
	deps, ownedID, tgUser := replaceDeps(t)
	h := NewMux(deps)

	rec := postReplace(t, h, ownedID, tgUser, `{"provider":"кто-то","option_id":"nl","old_tunnel_id":"awg11","policy_name":"HydraRoute"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// Чужому роутеру -- 404 до того, как выяснится, существует ли он.
func TestMiniappReplaceStrangerGets404(t *testing.T) {
	deps, ownedID, _ := replaceDeps(t)
	h := NewMux(deps)

	rec := postReplace(t, h, ownedID, 777, `{"provider":"amnezia","option_id":"nl","old_tunnel_id":"awg11","policy_name":"HydraRoute"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// Замен не было -- это ответ, а не ошибка.
func TestMiniappReplaceStatusEmptyWhenNeverRun(t *testing.T) {
	deps, ownedID, tgUser := replaceDeps(t)
	h := NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/replace", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tgUser))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var status miniappReplaceResp
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.JobID != "" || status.Running {
		t.Fatalf("статус = %+v", status)
	}
}
