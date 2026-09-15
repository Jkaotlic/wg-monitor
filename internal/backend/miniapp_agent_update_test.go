package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func agentUpdateTestMux(t *testing.T) (*db.DB, int64, http.Handler, *fakeCmdSink) {
	t.Helper()
	old := serverVersion
	SetVersion("v0.33.0")
	t.Cleanup(func() { SetVersion(old) })
	d, ownedID, _, _ := seedMiniappFleet(t)
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.31.0"); err != nil {
		t.Fatal(err)
	}
	sink := &fakeCmdSink{}
	h := NewMux(Deps{
		DB:                  d,
		CommandSink:         sink,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		PublicBaseURL:       "https://backend.example.com",
		PublicIP:            "203.0.113.5",
	})
	return d, ownedID, h, sink
}

func postMiniappJSON(t *testing.T, h http.Handler, path, body string, telegramUserID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeDeployError(t *testing.T, rec *httptest.ResponseRecorder) (code, errField, message string) {
	t.Helper()
	var body struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("тело ошибки: %v (%s)", err, rec.Body.String())
	}
	return body.Code, body.Error, body.Message
}

// Владелец роутера -- не админ: 404 до всего, даже с верным подтверждением
// и даже на несуществующий роутер.
func TestMiniappAgentUpdateHiddenFromNonAdmin(t *testing.T) {
	_, ownedID, h, sink := agentUpdateTestMux(t)
	for _, path := range []string{
		fmt.Sprintf("/v1/miniapp/routers/%d/agent/update", ownedID),
		fmt.Sprintf("/v1/miniapp/routers/%d/agent/update/cancel", ownedID),
		"/v1/miniapp/routers/424242/agent/update",
	} {
		rec := postMiniappJSON(t, h, path, `{"confirm":"router-owned"}`, 100)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s владельцу: код %d, ждали 404", path, rec.Code)
		}
	}
	if len(sink.snapshotEnqueued()) != 0 {
		t.Fatal("не-админ поставил команду")
	}
}

func TestMiniappAgentUpdateQueuesDeferredForOfflineRouter(t *testing.T) {
	d, ownedID, h, sink := agentUpdateTestMux(t)
	rec := postMiniappJSON(t, h, fmt.Sprintf("/v1/miniapp/routers/%d/agent/update", ownedID), `{"confirm":"Router-Owned "}`, 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	var resp miniappAgentUpdateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// Роутер ни разу не выходил на связь -- выключен: не ошибка, а отложено.
	if !resp.Queued || !resp.Deferred || resp.TargetVersion != "v0.33.0" {
		t.Fatalf("ответ: %+v", resp)
	}
	q := sink.snapshotEnqueued()
	if len(q) != 1 || q[0].Args["version"] != "v0.33.0" ||
		q[0].Args["repo_base"] != "https://backend.example.com/v1/releases/download" ||
		q[0].Args["repo_resolve_ip"] != "203.0.113.5" {
		t.Fatalf("очередь: %+v", q)
	}
	st, _ := d.Users().PendingDeploy(ownedID)
	if st.Version != "v0.33.0" {
		t.Fatalf("отметка: %+v", st)
	}
}

func TestMiniappAgentUpdateOnlineRouterNotDeferred(t *testing.T) {
	d, ownedID, h, _ := agentUpdateTestMux(t)
	if err := d.Users().UpdateLastSeen(ownedID); err != nil {
		t.Fatal(err)
	}
	rec := postMiniappJSON(t, h, fmt.Sprintf("/v1/miniapp/routers/%d/agent/update", ownedID),
		`{"confirm":"router-owned","target_version":"v0.32.0"}`, 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	var resp miniappAgentUpdateResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Deferred || resp.TargetVersion != "v0.32.0" {
		t.Fatalf("ответ: %+v", resp)
	}
}

func TestMiniappAgentUpdateRefusals(t *testing.T) {
	d, ownedID, h, sink := agentUpdateTestMux(t)
	path := fmt.Sprintf("/v1/miniapp/routers/%d/agent/update", ownedID)

	check := func(name, body string, wantStatus int, wantCode string) {
		t.Helper()
		rec := postMiniappJSON(t, h, path, body, 999)
		if rec.Code != wantStatus {
			t.Fatalf("%s: код %d, ждали %d (%s)", name, rec.Code, wantStatus, rec.Body.String())
		}
		code, errField, msg := decodeDeployError(t, rec)
		if code != wantCode || errField != wantCode {
			t.Fatalf("%s: code=%q error=%q, ждали %q в обоих", name, code, errField, wantCode)
		}
		if msg == "" || strings.Contains(msg, "pending") || strings.Contains(msg, "self_update") {
			t.Fatalf("%s: текст для людей %q", name, msg)
		}
	}

	check("подтверждение", `{"confirm":"router-other"}`, http.StatusBadRequest, "confirm_mismatch")
	check("нет выпуска", `{"confirm":"router-owned","target_version":"latest"}`, http.StatusConflict, "no_release")
	check("даунгрейд", `{"confirm":"router-owned","target_version":"v0.30.0"}`, http.StatusBadRequest, "downgrade_rejected")

	if err := d.Users().MarkPendingDeploy(ownedID, "v0.32.0", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	check("уже назначено", `{"confirm":"router-owned"}`, http.StatusConflict, "deploy_pending")
	if _, err := d.Users().ClearPendingDeploy(ownedID); err != nil {
		t.Fatal(err)
	}

	if _, err := d.SQL().Exec(`UPDATE users SET last_deployed_version = 'v0.13.0-rc4' WHERE id = ?`, ownedID); err != nil {
		t.Fatal(err)
	}
	check("слишком старый", `{"confirm":"router-owned"}`, http.StatusConflict, "agent_too_old")
	if _, err := d.SQL().Exec(`UPDATE users SET last_deployed_version = NULL WHERE id = ?`, ownedID); err != nil {
		t.Fatal(err)
	}
	check("версия неизвестна", `{"confirm":"router-owned"}`, http.StatusConflict, "agent_too_old")

	if len(sink.snapshotEnqueued()) != 0 {
		t.Fatalf("отказы поставили команды: %+v", sink.snapshotEnqueued())
	}
	rec := postMiniappJSON(t, h, "/v1/miniapp/routers/424242/agent/update", `{"confirm":"x"}`, 999)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("нет роутера: код %d", rec.Code)
	}
}

func TestMiniappAgentUpdateCancel(t *testing.T) {
	d, ownedID, h, _ := agentUpdateTestMux(t)
	path := fmt.Sprintf("/v1/miniapp/routers/%d/agent/update/cancel", ownedID)
	if err := d.Users().MarkPendingDeploy(ownedID, "v0.33.0", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		rec := postMiniappJSON(t, h, path, `{}`, 999)
		if rec.Code != http.StatusOK {
			t.Fatalf("отмена %d: код %d, тело %s", i, rec.Code, rec.Body.String())
		}
		var resp struct {
			Cleared *bool `json:"cleared"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Cleared == nil || *resp.Cleared != want {
			t.Fatalf("отмена %d: тело %s, ждали cleared=%v", i, rec.Body.String(), want)
		}
	}
	if st, _ := d.Users().PendingDeploy(ownedID); st.Version != "" {
		t.Fatalf("отметка осталась: %+v", st)
	}
}

func fleetUpdate(t *testing.T, h http.Handler, body string, tg int64) *httptest.ResponseRecorder {
	t.Helper()
	return postMiniappJSON(t, h, "/v1/miniapp/fleet/agent/update", body, tg)
}

func TestMiniappFleetAgentUpdateHiddenFromNonAdmin(t *testing.T) {
	_, _, h, sink := agentUpdateTestMux(t)
	if rec := fleetUpdate(t, h, `{"confirm":"обновить"}`, 100); rec.Code != http.StatusNotFound {
		t.Fatalf("владельцу: код %d, ждали 404", rec.Code)
	}
	if len(sink.snapshotEnqueued()) != 0 {
		t.Fatal("не-админ поставил команды")
	}
}

func TestMiniappFleetAgentUpdateNeedsConfirmWord(t *testing.T) {
	_, _, h, sink := agentUpdateTestMux(t)
	rec := fleetUpdate(t, h, `{"confirm":"обновит"}`, 999)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("код %d", rec.Code)
	}
	if code, errField, _ := decodeDeployError(t, rec); code != "confirm_mismatch" || errField != "confirm_mismatch" {
		t.Fatalf("code=%q error=%q", code, errField)
	}
	if len(sink.snapshotEnqueued()) != 0 {
		t.Fatal("без подтверждения поставлены команды")
	}
}

func TestMiniappFleetAgentUpdateEmptyIsArray(t *testing.T) {
	d, ownedID, h, _ := agentUpdateTestMux(t)
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.33.0"); err != nil {
		t.Fatal(err)
	}
	rec := fleetUpdate(t, h, `{"confirm":" Обновить "}`, 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Fatalf("пустой итог обязан быть [], тело %s", rec.Body.String())
	}
}

func TestMiniappFleetAgentUpdateOutcomes(t *testing.T) {
	d, ownedID, h, sink := agentUpdateTestMux(t)
	// router-owned: v0.31.0, ни разу не на связи -> отложено.
	// router-other: версии нет -> в итоге не упоминается.
	add := func(nick, tokChar, version string) int64 {
		t.Helper()
		id, err := d.Users().Insert(nick, strings.Repeat(tokChar, 64), "198.51.100.9", "awg0")
		if err != nil {
			t.Fatal(err)
		}
		if version != "" {
			if err := d.Users().UpdateLastSeenAgentVersion(id, version); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	onlineID := add("router-online", "1", "v0.31.0")
	if err := d.Users().UpdateLastSeen(onlineID); err != nil {
		t.Fatal(err)
	}
	add("router-ancient", "2", "v0.13.0-rc4")
	pendingID := add("router-pending", "3", "v0.31.0")
	if err := d.Users().MarkPendingDeploy(pendingID, "v0.32.0", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	add("router-fresh", "4", "v0.33.0")

	rec := fleetUpdate(t, h, `{"confirm":"обновить"}`, 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	var resp miniappFleetAgentUpdateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]miniappFleetAgentUpdateResult{}
	for _, r := range resp.Results {
		got[r.Nickname] = r
		for _, banned := range []string{"pending", "self_update"} {
			if strings.Contains(r.ReasonText, banned) {
				t.Errorf("%s: внутреннее имя в тексте %q", r.Nickname, r.ReasonText)
			}
		}
	}
	want := map[string][2]string{
		"router-owned":   {"deferred", "router_asleep"},
		"router-online":  {"queued", ""},
		"router-ancient": {"skipped", "agent_too_old"},
		"router-pending": {"skipped", "deploy_pending"},
	}
	if len(got) != len(want) {
		t.Fatalf("в итоге %d роутеров, ждали %d: %+v", len(got), len(want), resp.Results)
	}
	for nick, w := range want {
		r, ok := got[nick]
		if !ok || r.Outcome != w[0] || r.ReasonCode != w[1] || r.ReasonText == "" {
			t.Errorf("%s: %+v, ждали outcome=%s reason_code=%q", nick, r, w[0], w[1])
		}
	}
	if got["router-owned"].RouterID != ownedID {
		t.Errorf("router_id: %+v", got["router-owned"])
	}
	if n := len(sink.snapshotEnqueued()); n != 2 {
		t.Fatalf("поставлено %d команд, ждали 2", n)
	}
}

func TestMiniappFleetAgentUpdateReportsEnqueueError(t *testing.T) {
	old := serverVersion
	SetVersion("v0.33.0")
	t.Cleanup(func() { SetVersion(old) })
	d, ownedID, _, _ := seedMiniappFleet(t)
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.31.0"); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{
		DB:                  d,
		CommandSink:         failingEnqueueSink{err: errors.New("queue closed")},
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		PublicBaseURL:       "https://backend.example.com",
	})
	rec := fleetUpdate(t, h, `{"confirm":"обновить"}`, 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	var resp miniappFleetAgentUpdateResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Results) != 1 || resp.Results[0].Outcome != "error" || resp.Results[0].ReasonText == "" {
		t.Fatalf("итог: %+v", resp.Results)
	}
}

func TestMiniappFleetAgentUpdateWithoutReleaseVersion(t *testing.T) {
	_, _, h, _ := agentUpdateTestMux(t)
	SetVersion("unknown") // agentUpdateTestMux вернёт прежнюю в Cleanup
	rec := fleetUpdate(t, h, `{"confirm":"обновить"}`, 999)
	if rec.Code != http.StatusConflict {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	if code, _, _ := decodeDeployError(t, rec); code != "no_release" {
		t.Fatalf("code=%q", code)
	}
}
