package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func getMiniapp(t *testing.T, h http.Handler, path string, telegramUserID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
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

	// Назначена та же версия, что раскатывается по умолчанию (последняя):
	// повтор -- отказ. Более старую назначенную свежая вытесняет.
	if err := d.Users().MarkPendingDeploy(ownedID, "v0.33.0", "2026-09-15T10:00:00Z"); err != nil {
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

// B5d: версии новее самого бэкенда среди выпусков быть не может -- зеркало
// раздаёт то же, что зеркалит бэкенд. Раньше проверялся только формат тега.
func TestMiniappAgentUpdateRefusesTargetNewerThanBackend(t *testing.T) {
	_, ownedID, h, sink := agentUpdateTestMux(t)
	rec := postMiniappJSON(t, h, fmt.Sprintf("/v1/miniapp/routers/%d/agent/update", ownedID),
		`{"confirm":"router-owned","target_version":"v0.34.0"}`, 999)
	if rec.Code != http.StatusConflict {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	if code, errField, _ := decodeDeployError(t, rec); code != deployErrNoRelease || errField != deployErrNoRelease {
		t.Fatalf("code=%q error=%q, ждали %q", code, errField, deployErrNoRelease)
	}
	if len(sink.snapshotEnqueued()) != 0 {
		t.Fatal("версия впереди бэкенда поставила команду")
	}
}

// B5a: "не настроено" бывает по трём разным причинам, и админу нужно знать,
// по какой именно -- код not_configured/503 у всех троих общий, текст разный.
func TestMiniappAgentUpdateNotConfiguredReasons(t *testing.T) {
	cases := []struct {
		name    string
		build   func(d *db.DB) Deps
		wantMsg string
	}{
		{"нет базы", func(*db.DB) Deps {
			return Deps{TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999}
		}, "база данных"},
		{"нет очереди команд", func(d *db.DB) Deps {
			return Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999}
		}, "очередь команд"},
		{"нет публичного адреса", func(d *db.DB) Deps {
			return Deps{DB: d, CommandSink: &fakeCmdSink{}, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999}
		}, "публичный адрес"},
	}
	old := serverVersion
	SetVersion("v0.33.0")
	t.Cleanup(func() { SetVersion(old) })
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, ownedID, _, _ := seedMiniappFleet(t)
			if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.31.0"); err != nil {
				t.Fatal(err)
			}
			h := NewMux(c.build(d))
			rec := postMiniappJSON(t, h, fmt.Sprintf("/v1/miniapp/routers/%d/agent/update", ownedID), `{"confirm":"router-owned"}`, 999)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
			}
			code, errField, msg := decodeDeployError(t, rec)
			if code != "not_configured" || errField != "not_configured" {
				t.Fatalf("code=%q error=%q, ждали not_configured в обоих", code, errField)
			}
			if !strings.Contains(msg, c.wantMsg) {
				t.Fatalf("текст %q не называет причину %q", msg, c.wantMsg)
			}
		})
	}
}

// B5e: пустое и битое тело -- отказ 400 bad_request, а не 500 и не паника.
func TestMiniappAgentUpdateBadRequestOnBrokenBody(t *testing.T) {
	_, ownedID, h, _ := agentUpdateTestMux(t)
	path := fmt.Sprintf("/v1/miniapp/routers/%d/agent/update", ownedID)
	for _, body := range []string{"", "{", `{"confirm":`, "not json at all"} {
		rec := postMiniappJSON(t, h, path, body, 999)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("тело %q: код %d, ждали 400", body, rec.Code)
		}
		if code, errField, _ := decodeDeployError(t, rec); code != "bad_request" || errField != "bad_request" {
			t.Fatalf("тело %q: code=%q error=%q", body, code, errField)
		}
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

// B5b, B5e: отмена несуществующего роутера -- 404 с русским текстом, а не
// 500, не паника и не английское "router not found".
func TestMiniappAgentUpdateCancelNonexistentRouter(t *testing.T) {
	_, _, h, _ := agentUpdateTestMux(t)
	rec := postMiniappJSON(t, h, "/v1/miniapp/routers/424242/agent/update/cancel", `{}`, 999)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	if _, _, msg := decodeDeployError(t, rec); msg != "Роутер не найден." {
		t.Fatalf("текст = %q, ждали «Роутер не найден.»", msg)
	}
}

// B5e: владелец роутера (не админ) не может отменить обновление через этот
// маршрут -- 404, как у остальных поверхностей мини-аппа, и отметка обязана
// остаться нетронутой: отказ в доступе не должен иметь побочных эффектов.
func TestMiniappAgentUpdateCancelByOwnerDeniedMarkStays(t *testing.T) {
	d, ownedID, h, _ := agentUpdateTestMux(t)
	if err := d.Users().MarkPendingDeploy(ownedID, "v0.33.0", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	rec := postMiniappJSON(t, h, fmt.Sprintf("/v1/miniapp/routers/%d/agent/update/cancel", ownedID), `{}`, 100)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("владельцу: код %d, ждали 404", rec.Code)
	}
	st, err := d.Users().PendingDeploy(ownedID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "v0.33.0" {
		t.Fatalf("отмена не-админом изменила отметку: %+v", st)
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
	code, errField, msg := decodeDeployError(t, rec)
	if code != "confirm_mismatch" || errField != "confirm_mismatch" {
		t.Fatalf("code=%q error=%q", code, errField)
	}
	// B5c: текст массового маршрута -- про слово «обновить», а не про имя
	// роутера (тот текст живёт у одиночного маршрута confirm_mismatch).
	if !strings.Contains(msg, "обновить") || strings.Contains(msg, "Имя роутера") {
		t.Fatalf("текст несовпадения %q не про слово подтверждения", msg)
	}
	if len(sink.snapshotEnqueued()) != 0 {
		t.Fatal("без подтверждения поставлены команды")
	}
}

// B5e: 503 not_configured на массовом маршруте тоже, и тоже с текстом причины.
func TestMiniappFleetAgentUpdateNotConfigured(t *testing.T) {
	h := NewMux(Deps{TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	rec := fleetUpdate(t, h, `{"confirm":"обновить"}`, 999)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	code, errField, msg := decodeDeployError(t, rec)
	if code != "not_configured" || errField != "not_configured" {
		t.Fatalf("code=%q error=%q", code, errField)
	}
	if !strings.Contains(msg, "база данных") {
		t.Fatalf("текст %q не называет причину", msg)
	}
}

// B5e: пустое и битое тело массового маршрута -- тоже 400 bad_request.
func TestMiniappFleetAgentUpdateBadRequestOnBrokenBody(t *testing.T) {
	_, _, h, _ := agentUpdateTestMux(t)
	for _, body := range []string{"", "{", "not json at all"} {
		rec := fleetUpdate(t, h, body, 999)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("тело %q: код %d, ждали 400", body, rec.Code)
		}
		if code, errField, _ := decodeDeployError(t, rec); code != "bad_request" || errField != "bad_request" {
			t.Fatalf("тело %q: code=%q error=%q", body, code, errField)
		}
	}
}

func TestMiniappFleetAgentUpdateWithoutTargetsIsConflict(t *testing.T) {
	d, ownedID, h, _ := agentUpdateTestMux(t)
	if err := d.Users().UpdateLastSeenAgentVersion(ownedID, "v0.33.0"); err != nil {
		t.Fatal(err)
	}
	rec := fleetUpdate(t, h, `{"confirm":" Обновить "}`, 999)
	if rec.Code != http.StatusConflict {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}
	if code, _, _ := decodeDeployError(t, rec); code != "no_targets" {
		t.Fatalf("ждали no_targets, тело %s", rec.Body.String())
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
	// B6: ниже agentSelfUpdateFloor -- self_update не умеет вовсе, Behind
	// теперь false, и в массовом обновлении такой роутер не участвует
	// (ни в счётчике, ни в этом отчёте) -- ему нужна переустановка, не
	// кнопка «Обновить всех отставших».
	add("router-ancient", "2", "v0.13.0-rc4")
	pendingID := add("router-pending", "3", "v0.31.0")
	if err := d.Users().MarkPendingDeploy(pendingID, "v0.32.0", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// Назначена та же версия, что раскатывается: повтор -- пропуск.
	sameID := add("router-pending-same", "5", "v0.31.0")
	if err := d.Users().MarkPendingDeploy(sameID, "v0.33.0", "2026-09-15T10:00:00Z"); err != nil {
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
	if resp.Job.State != "canary_pending" || len(resp.Targets) != 3 {
		t.Fatalf("canary rollout: job=%+v targets=%+v", resp.Job, resp.Targets)
	}
	if resp.Targets[0].RouterID != onlineID || resp.Targets[0].Role != "canary" || resp.Targets[0].Status != "queued" {
		t.Fatalf("canary selected incorrectly: %+v", resp.Targets[0])
	}
	if n := len(sink.snapshotEnqueued()); n != 1 {
		t.Fatalf("поставлено %d команд, ждали только canary", n)
	}
	if st, _ := d.Users().PendingDeploy(pendingID); st.Version != "v0.32.0" {
		t.Fatalf("fleet phase touched before observation gate: %+v", st)
	}
	_ = ownedID
}

func TestMiniappFleetRolloutIdleIsNormalEmptyResponse(t *testing.T) {
	_, _, h, _ := agentUpdateTestMux(t)
	rec := getMiniapp(t, h, "/v1/miniapp/fleet/agent/rollout", 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappFleetAgentUpdateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Job.ID != "" || resp.Targets == nil || len(resp.Targets) != 0 {
		t.Fatalf("idle rollout response=%+v", resp)
	}
}

func TestMiniappFleetUpdateSupersedesOlderPausedRollout(t *testing.T) {
	d, onlineID, h, sink := agentUpdateTestMux(t)
	if err := d.Users().UpdateLastSeen(onlineID); err != nil {
		t.Fatal(err)
	}
	offlineID, err := d.Users().Insert("router-offline", "token-rollout-offline", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(offlineID, "v0.30.0"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := d.Users().MarkPendingDeploy(offlineID, "v0.32.0", now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	old := db.RolloutJob{ID: "old-rollout", TargetVersion: "v0.31.0", State: "rolling", CreatedAt: now, UpdatedAt: now}
	if err := d.Rollouts().Create(old, []int64{onlineID, offlineID}, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.Rollouts().Transition(old.ID, "rolling", "paused", "report_gap", now); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		rec := fleetUpdate(t, h, `{"confirm":"обновить"}`, 999)
		var resp miniappFleetAgentUpdateResp
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
		}
		if resp.Job.TargetVersion != "v0.33.0" || resp.Job.State != "canary_pending" || len(resp.Targets) != 2 || resp.Targets[0].RouterID != onlineID {
			t.Fatalf("wrong rollout: job=%+v targets=%+v", resp.Job, resp.Targets)
		}
	}
	if got := len(sink.snapshotEnqueued()); got != 1 {
		t.Fatalf("enqueued %d updates, want one canary", got)
	}
	rec := postMiniappJSON(t, h, "/v1/miniapp/fleet/agent/rollout/cancel?job_id="+old.ID, `{}`, 999)
	var active miniappFleetAgentUpdateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &active); err != nil || rec.Code != http.StatusOK || active.Job.State != "canary_pending" {
		t.Fatalf("stale cancellation changed the active rollout: code=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
	retired, _, err := d.Rollouts().Get(old.ID)
	if err != nil || retired.State != "cancelled" {
		t.Fatalf("old rollout=%+v err=%v", retired, err)
	}
	pending, err := d.Users().PendingDeploy(offlineID)
	if err != nil || pending.Version != "v0.32.0" {
		t.Fatalf("pending update before observation=%+v err=%v", pending, err)
	}

	engine, ok := miniappRolloutEngine(Deps{
		DB: d, CommandSink: sink, PublicBaseURL: "https://backend.example.com",
	}, context.Background(), 999)
	if !ok {
		t.Fatal("rollout engine not configured")
	}
	engine.ObservationWindow = time.Second
	now = time.Now().UTC()
	engine.Now = func() time.Time { return now }
	job, _, err := d.Rollouts().Active()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(onlineID, serverVersion); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"observing", "rolling", "complete"} {
		now = now.Add(time.Second)
		got, err := engine.Tick(job.ID)
		if err != nil || got.State != want {
			t.Fatalf("rollout=%+v err=%v, want %s", got, err, want)
		}
	}
	pending, err = d.Users().PendingDeploy(offlineID)
	if err != nil || pending.Version != serverVersion {
		t.Fatalf("deferred update after rollout=%+v err=%v", pending, err)
	}

	rec = getMiniapp(t, h, "/v1/miniapp/fleet/agent/rollout?job_id="+job.ID, 999)
	var resp miniappFleetAgentUpdateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK || resp.Job.State != "complete" {
		t.Fatalf("code=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
}

func TestMiniappFleetUpdateWaitsForAnOnlineCanary(t *testing.T) {
	d, _, h, sink := agentUpdateTestMux(t)
	rec := fleetUpdate(t, h, `{"confirm":"обновить"}`, 999)
	code, _, _ := decodeDeployError(t, rec)
	if rec.Code != http.StatusConflict || code != "no_online_canary" {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if job, _, err := d.Rollouts().Active(); job.ID != "" || err == nil || len(sink.snapshotEnqueued()) != 0 {
		t.Fatalf("created a rollout without an online canary: job=%+v err=%v", job, err)
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
	if err := d.Users().UpdateLastSeen(ownedID); err != nil {
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
	if resp.Job.State != "paused" || resp.Job.PauseReason != "deploy_failed" {
		t.Fatalf("итог: %+v", resp.Job)
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

// Своя версия ниже текущей -- откат: без явного allow_downgrade отказ словами,
// с ним -- обычное назначение (спека цикла 2, п. 6).
func TestMiniappAgentUpdateAllowsDowngradeOnlyWhenAsked(t *testing.T) {
	_, ownedID, h, sink := agentUpdateTestMux(t)
	path := fmt.Sprintf("/v1/miniapp/routers/%d/agent/update", ownedID)

	rec := postMiniappJSON(t, h, path, `{"confirm":"router-owned","target_version":"v0.30.0"}`, 999)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("без разрешения: код %d (%s)", rec.Code, rec.Body.String())
	}
	if code, _, msg := decodeDeployError(t, rec); code != "downgrade_rejected" || msg != miniappAdminOpsErrorText("downgrade_rejected") {
		t.Fatalf("отказ: code=%q msg=%q", code, msg)
	}
	if len(sink.snapshotEnqueued()) != 0 {
		t.Fatal("отказ поставил команду")
	}

	rec = postMiniappJSON(t, h, path, `{"confirm":"router-owned","target_version":"v0.30.0","allow_downgrade":true}`, 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("с разрешением: код %d (%s)", rec.Code, rec.Body.String())
	}
	var resp miniappAgentUpdateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.TargetVersion != "v0.30.0" {
		t.Fatalf("ответ %s err=%v", rec.Body.String(), err)
	}
	if q := sink.snapshotEnqueued(); len(q) != 1 || q[0].Args["version"] != "v0.30.0" {
		t.Fatalf("очередь: %+v", q)
	}
}
