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

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// SEC-02: гейт роли на результат команды пропускался, пока команда ждала в
// очереди (ещё не выдана агенту): действие не было известно, опрос ждал -- и
// отдавал владельцу пришедший за время ожидания ответ админской команды с
// адресом панели.
func TestMiniappResultRoleGateCoversPendingCommand(t *testing.T) {
	_, ownedID, ownerTG, q, h := miniappRealQueueFleet(t)
	rec := miniappAgentConfigPost(t, h, ownedID, 999, `{"action":"agent_config_get"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("админ: %d %s", rec.Code, rec.Body.String())
	}
	var issued struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &issued)

	const panelURL = "http://198.51.100.7:8080"
	go func() {
		time.Sleep(150 * time.Millisecond)
		got, ok := q.Dequeue(context.Background(), ownedID, 0)
		if !ok || got.ID != issued.CmdID {
			return
		}
		_ = q.RecordResult(ownedID, wire.CommandResult{ID: issued.CmdID, Status: "ok",
			Output: `{"config_kind":"agent","awgm_base_url":"` + panelURL + `"}`})
	}()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/v1/miniapp/routers/%d/commands/%s?wait_sec=3", ownedID, issued.CmdID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", ownerTG))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound || bytes.Contains(res.Body.Bytes(), []byte("198.51.100.7")) {
		t.Fatalf("владелец дочитал ответ админской команды: %d %s", res.Code, res.Body.String())
	}
}

// Неизвестный cmd_id не у админа -- 404 сразу, а не ожидание: роль по нему
// не проверить.
func TestMiniappResultUnknownCmdIsNotFoundForNonAdmin(t *testing.T) {
	_, ownedID, ownerTG, _, h := miniappRealQueueFleet(t)
	start := time.Now()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/v1/miniapp/routers/%d/commands/%s?wait_sec=3", ownedID, "no-such-cmd"), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", ownerTG))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound || time.Since(start) > time.Second {
		t.Fatalf("неизвестная команда: %d за %s (%s)", res.Code, time.Since(start), res.Body.String())
	}
	if bytes.Contains(res.Body.Bytes(), []byte("result_not_ready")) {
		t.Fatalf("неизвестная команда выдана за «ещё не готово»: %s", res.Body.String())
	}
}

// Ответ не выдаёт, существует ли команда: неизвестная и админская для
// не-админа -- одно и то же тело 404.
func TestMiniappResultUnknownAndAdminOnlyLookTheSame(t *testing.T) {
	_, ownedID, ownerTG, _, h := miniappRealQueueFleet(t)
	rec := miniappAgentConfigPost(t, h, ownedID, 999, `{"action":"agent_config_get"}`)
	var issued struct {
		CmdID string `json:"cmd_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &issued)
	adminOnly := miniappPollResult(t, h, ownedID, ownerTG, issued.CmdID)
	unknown := miniappPollResult(t, h, ownedID, ownerTG, "no-such-cmd")
	if adminOnly.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound ||
		adminOnly.Body.String() != unknown.Body.String() {
		t.Fatalf("различимы: %d %q против %d %q", adminOnly.Code, adminOnly.Body.String(), unknown.Code, unknown.Body.String())
	}
}
