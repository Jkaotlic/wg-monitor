package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// Сохранённый пароль root (v0.45, задача B): пишется, когда админ вводит его
// при добавлении, переустановке и оживлении; наружу не отдаётся никогда; в
// парке -- только признак и «Забыть пароль».

var credTestKey = bytes.Repeat([]byte{9}, revive.KeySize)

type nopReviveEngine struct{}

func (nopReviveEngine) Launch(context.Context, int64, revive.Secrets, string) (string, error) {
	return "", &revive.LaunchError{Text: "тест"}
}
func (nopReviveEngine) Outcome(string) (revive.Outcome, bool) { return revive.Outcome{}, false }

// withRealRevive -- настоящий сервис оживления с известным ключом; панель
// всегда «не отвечает», чтобы проверка после постановки не шла в сеть.
func withRealRevive(t *testing.T) func(*Deps) {
	return func(d *Deps) {
		svc, err := revive.New(revive.Config{
			DB: d.DB, Key: append([]byte(nil), credTestKey...), Engine: nopReviveEngine{},
			Probe: func(context.Context, string) string { return awgmstate.Offline },
			Sleep: func(context.Context, time.Duration) bool { return false },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Wait)
		d.Revive = svc
	}
}

// storedCreds -- расшифрованное сохранённое; ok=false -- ничего нет.
func storedCreds(t *testing.T, d *db.DB, routerID int64) (revive.Secrets, bool) {
	t.Helper()
	nonce, ct, _, ok, err := d.RouterCredentials().Get(routerID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return revive.Secrets{}, false
	}
	box, err := revive.NewBox(credTestKey)
	if err != nil {
		t.Fatal(err)
	}
	s, err := box.Open(routerID, nonce, ct)
	if err != nil {
		t.Fatalf("сохранённое не расшифровывается: %v", err)
	}
	return s, true
}

func assertFixtureStored(t *testing.T, d *db.DB, routerID int64) {
	t.Helper()
	s, ok := storedCreds(t, d, routerID)
	if !ok {
		t.Fatal("пароль root не сохранён")
	}
	if !s.Equal(revive.NewSecrets(miniappReviveRoot, miniappReviveLogin, miniappRevivePanel, miniappReviveKey)) {
		t.Fatal("сохранено не то, что ввёл админ")
	}
}

func TestMiniappReinstallSavesRootPassword(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	if err := env.d.Users().UpdateDeployInfo("router-owned", db.DeployInfo{AWGMURL: "https://panel.example.com", LastDeployedVersion: "v0.35.0"}); err != nil {
		t.Fatal(err)
	}
	if err := env.d.Users().UpdateLastSeen(env.ownedID); err != nil {
		t.Fatal(err)
	}
	rec := miniappDo(t, env.h, http.MethodPost, reinstallPath(env.ownedID), secretsBody(map[string]any{"confirm": "router-owned"}), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("переустановка: код %d (%s)", rec.Code, rec.Body.String())
	}
	waitForProvisionTerminal(t, env.store, decodeJobStart(t, "переустановка", rec.Body.Bytes()), time.Second)
	assertFixtureStored(t, env.d, env.ownedID)
	assertNoReviveSecrets(t, "журнал", env.logs.String())
}

func TestMiniappReinstallRefusedSavesNothing(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	if err := env.d.Users().UpdateDeployInfo("router-owned", db.DeployInfo{AWGMURL: "https://panel.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := env.d.Users().UpdateLastSeen(env.ownedID); err != nil {
		t.Fatal(err)
	}
	rec := miniappDo(t, env.h, http.MethodPost, reinstallPath(env.ownedID), secretsBody(map[string]any{"confirm": "не-то-имя"}), 999)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("код %d", rec.Code)
	}
	if _, ok := storedCreds(t, env.d, env.ownedID); ok {
		t.Fatal("отказанный запрос сохранил пароль")
	}
}

func TestMiniappProvisionSavesRootPassword(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	rec := miniappDo(t, env.h, http.MethodPost, provisionPath, provisionBody(nil), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("установка: код %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	waitForProvisionTerminal(t, env.store, resp.JobID, time.Second)
	u, err := env.d.Users().GetByNickname("router-new")
	if err != nil {
		t.Fatal(err)
	}
	assertFixtureStored(t, env.d, u.ID)
	assertNoReviveSecrets(t, "журнал", env.logs.String())
}

func TestMiniappReviveSavesRootPassword(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	body := reviveBody("router-owned", map[string]any{"awgm_url": "https://router.example.com"})
	rec := miniappDo(t, env.h, http.MethodPost, revivePath(env.ownedID), body, 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("оживление: код %d (%s)", rec.Code, rec.Body.String())
	}
	assertFixtureStored(t, env.d, env.ownedID)
	assertNoReviveSecrets(t, "журнал", env.logs.String())
}

// Без ключа оживления хранить нечем: запрос проходит, строки нет.
func TestMiniappReinstallWithoutReviveKeySavesNothing(t *testing.T) {
	env, _ := reinstallEnv(t)
	rec := miniappDo(t, env.h, http.MethodPost, reinstallPath(env.ownedID), secretsBody(map[string]any{"confirm": "router-owned"}), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("код %d", rec.Code)
	}
	waitForProvisionTerminal(t, env.store, decodeJobStart(t, "переустановка", rec.Body.Bytes()), time.Second)
	if _, _, _, ok, _ := env.d.RouterCredentials().Get(env.ownedID); ok {
		t.Fatal("без ключа строка появилась")
	}
}

func credentialsPath(id int64) string { return fmt.Sprintf("/v1/miniapp/routers/%d/credentials", id) }

func saveFixtureCreds(t *testing.T, d *db.DB, routerID int64) {
	t.Helper()
	box, _ := revive.NewBox(credTestKey)
	nonce, ct, err := box.Seal(routerID, revive.NewSecrets(miniappReviveRoot, miniappReviveLogin, miniappRevivePanel, miniappReviveKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RouterCredentials().Put(routerID, nonce, ct, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestMiniappForgetCredentials(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	saveFixtureCreds(t, env.d, env.ownedID)

	// Владельцу -- 404, и пароль на месте.
	if rec := miniappDo(t, env.h, http.MethodDelete, credentialsPath(env.ownedID), "", 100); rec.Code != http.StatusNotFound {
		t.Fatalf("владельцу: код %d", rec.Code)
	}
	if _, ok := storedCreds(t, env.d, env.ownedID); !ok {
		t.Fatal("не-админ стёр пароль")
	}

	rec := miniappDo(t, env.h, http.MethodDelete, credentialsPath(env.ownedID), "", 999)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cleared":true`) {
		t.Fatalf("админу: код %d тело %s", rec.Code, rec.Body.String())
	}
	if _, ok := storedCreds(t, env.d, env.ownedID); ok {
		t.Fatal("пароль остался")
	}
	rec = miniappDo(t, env.h, http.MethodDelete, credentialsPath(env.ownedID), "", 999)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cleared":false`) {
		t.Fatalf("повтор: код %d тело %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(env.logs.String(), "router credentials forgotten") {
		t.Fatalf("журнал молчит: %s", env.logs.String())
	}
}

// «Забыть пароль» снимает и ждущее авто-оживление: оно поставлено из этого
// же пароля, и копия в его секрете иначе прожила бы до 30 дней. Оживление,
// поставленное админом руками, -- его собственное, у него своя отмена.
func TestMiniappForgetCredentialsCancelsAutoRevive(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	if _, err := env.d.SQL().Exec(`UPDATE users SET awgm_url = 'https://router.example.com' WHERE id = ?`, env.ownedID); err != nil {
		t.Fatal(err)
	}
	saveFixtureCreds(t, env.d, env.ownedID)
	svc := reviveForTest(t, env)
	if out, err := svc.AutoSchedule(context.Background(), env.ownedID); err != nil || out != revive.AutoScheduled {
		t.Fatalf("авто-постановка: %v %v", out, err)
	}
	svc.Wait()
	rec := miniappDo(t, env.h, http.MethodDelete, credentialsPath(env.ownedID), "", 999)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"revive_cancelled":true`) {
		t.Fatalf("код %d тело %s", rec.Code, rec.Body.String())
	}
	if in, _ := env.d.Revive().Get(env.ownedID); in == nil || in.Status != revive.StatusCancelled {
		t.Fatalf("авто-оживление не снято: %+v", in)
	}
	if _, _, ok, _ := env.d.Revive().Secret(env.ownedID); ok {
		t.Fatal("копия пароля в секрете оживления осталась")
	}
}

func TestMiniappForgetCredentialsKeepsManualRevive(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	body := reviveBody("router-owned", map[string]any{"awgm_url": "https://router.example.com"})
	if rec := miniappDo(t, env.h, http.MethodPost, revivePath(env.ownedID), body, 999); rec.Code != http.StatusAccepted {
		t.Fatalf("оживление: %d %s", rec.Code, rec.Body.String())
	}
	reviveForTest(t, env).Wait()
	rec := miniappDo(t, env.h, http.MethodDelete, credentialsPath(env.ownedID), "", 999)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"revive_cancelled":false`) {
		t.Fatalf("код %d тело %s", rec.Code, rec.Body.String())
	}
	if in, _ := env.d.Revive().Get(env.ownedID); in == nil || in.Status != revive.StatusWaiting {
		t.Fatalf("ручное оживление снято: %+v", in)
	}
}

// Без ключа оживления «Забыть» всё равно стирает: для удаления ключ не нужен.
func TestMiniappForgetCredentialsWithoutReviveKey(t *testing.T) {
	env := newAdminOpsEnv(t)
	saveFixtureCreds(t, env.d, env.ownedID)
	rec := miniappDo(t, env.h, http.MethodDelete, credentialsPath(env.ownedID), "", 999)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cleared":true`) {
		t.Fatalf("код %d тело %s", rec.Code, rec.Body.String())
	}
	if _, _, _, ok, _ := env.d.RouterCredentials().Get(env.ownedID); ok {
		t.Fatal("пароль остался")
	}
}

// reviveForTest достаёт настоящий сервис, собранный withRealRevive.
func reviveForTest(t *testing.T, env *adminOpsEnv) *revive.Service {
	t.Helper()
	if env.revive == nil {
		t.Fatal("сервис оживления не собран")
	}
	return env.revive
}

func fleetRowOf(t *testing.T, resp miniappFleetResp, id int64) miniappFleetRouter {
	t.Helper()
	for _, r := range resp.Routers {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("роутера %d нет в парке", id)
	return miniappFleetRouter{}
}

func TestMiniappFleetShowsSavedPasswordAndAutoRevive(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	// Слишком старый агент, молчит: «давно не обновлялся».
	if _, err := env.d.SQL().Exec(`UPDATE users SET last_deployed_version = 'v0.12.0' WHERE id = ?`, env.ownedID); err != nil {
		t.Fatal(err)
	}
	row := fleetRowOf(t, fleetResponse(t, fleetRequest(t, env.h, 999)), env.ownedID)
	if row.RootPasswordSaved || row.AutoReviveBlocked != "для авто-оживления нужен пароль root" {
		t.Fatalf("без пароля: saved=%v blocked=%q", row.RootPasswordSaved, row.AutoReviveBlocked)
	}

	saveFixtureCreds(t, env.d, env.ownedID)
	row = fleetRowOf(t, fleetResponse(t, fleetRequest(t, env.h, 999)), env.ownedID)
	if !row.RootPasswordSaved || row.AutoReviveBlocked != "для авто-оживления нужен внешний адрес панели" {
		t.Fatalf("без адреса: saved=%v blocked=%q", row.RootPasswordSaved, row.AutoReviveBlocked)
	}

	if _, err := env.d.SQL().Exec(`UPDATE users SET awgm_url = 'https://router.example.com' WHERE id = ?`, env.ownedID); err != nil {
		t.Fatal(err)
	}
	row = fleetRowOf(t, fleetResponse(t, fleetRequest(t, env.h, 999)), env.ownedID)
	if row.AutoReviveBlocked != "" {
		t.Fatalf("всё есть, а строка жалуется: %q", row.AutoReviveBlocked)
	}
	if _, err := env.revive.AutoSchedule(context.Background(), env.ownedID); err != nil {
		t.Fatal(err)
	}
	env.revive.Wait()
	row = fleetRowOf(t, fleetResponse(t, fleetRequest(t, env.h, 999)), env.ownedID)
	if row.Revive == nil || !row.Revive.Auto || row.Revive.Status != revive.StatusWaiting {
		t.Fatalf("строка не видит авто-оживления: %+v", row.Revive)
	}

	// Свежий агент -- не «давно не обновлялся»: причин нет.
	other := fleetRowOf(t, fleetResponse(t, fleetRequest(t, env.h, 999)), otherRouterID(t, env.d))
	if other.RootPasswordSaved || other.AutoReviveBlocked != "" {
		t.Fatalf("соседний роутер: %+v", other)
	}
}

// Без ключа оживления авто-оживления нет вовсе -- строке не о чем жаловаться.
func TestMiniappFleetNoAutoReviveBlockerWhenDisabled(t *testing.T) {
	env := newAdminOpsEnv(t)
	if _, err := env.d.SQL().Exec(`UPDATE users SET last_deployed_version = 'v0.12.0' WHERE id = ?`, env.ownedID); err != nil {
		t.Fatal(err)
	}
	row := fleetRowOf(t, fleetResponse(t, fleetRequest(t, env.h, 999)), env.ownedID)
	if row.AutoReviveBlocked != "" {
		t.Fatalf("оживление выключено, а строка просит пароль: %q", row.AutoReviveBlocked)
	}
}

func otherRouterID(t *testing.T, d *db.DB) int64 {
	t.Helper()
	u, err := d.Users().GetByNickname("router-other")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// Сторож «не течёт»: сохранённый пароль не появляется ни в одном ответе
// мини-аппа и дашборда -- ни значением, ни шифртекстом, ни именем поля, в
// которое он мог бы приехать завтра. Смотрят и админ, и владелец.
func TestStoredRouterCredentialsNeverLeak(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	if _, err := env.d.SQL().Exec(`UPDATE users SET last_deployed_version = 'v0.12.0', awgm_url = 'https://router.example.com' WHERE id = ?`, env.ownedID); err != nil {
		t.Fatal(err)
	}
	saveFixtureCreds(t, env.d, env.ownedID)
	if _, err := env.revive.AutoSchedule(context.Background(), env.ownedID); err != nil {
		t.Fatal(err)
	}
	env.revive.Wait()
	nonce, ct, _, _, _ := env.d.RouterCredentials().Get(env.ownedID)
	rNonce, rCT, _, _ := env.d.Revive().Secret(env.ownedID)

	forbidden := append([]string(nil), reviveFixtureSecrets...)
	for _, b := range [][]byte{nonce, ct, rNonce, rCT} {
		if len(b) == 0 {
			t.Fatal("сторожу нечего ловить: шифртекста нет")
		}
		forbidden = append(forbidden, base64.StdEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b), hex.EncodeToString(b))
	}
	// Имена полей: "root_password_saved" -- признак, его можно; ключ
	// "root_password": с значением -- нельзя.
	fields := []string{`"root_password":`, `"awgm_password"`, `"awgm_api_key"`, `"awgm_login"`, `"ciphertext"`, `"nonce"`, `"secret"`, `"credentials"`}

	get := func(path string, uid int64, dash bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if dash {
			req.AddCookie(webDashCookie(t))
		} else {
			req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", uid))
		}
		rec := httptest.NewRecorder()
		env.h.ServeHTTP(rec, req)
		return rec
	}
	id := env.ownedID
	paths := []string{
		"/v1/miniapp/fleet",
		"/v1/miniapp/routers",
		fmt.Sprintf("/v1/miniapp/routers/%d", id),
		fmt.Sprintf("/v1/miniapp/routers/%d/settings", id),
		fmt.Sprintf("/v1/miniapp/routers/%d/versions", id),
		fmt.Sprintf("/v1/miniapp/routers/%d/agent/connection", id),
		fmt.Sprintf("/v1/miniapp/routers/%d/access", id),
	}
	checked := 0
	check := func(where string, rec *httptest.ResponseRecorder) {
		t.Helper()
		body := rec.Body.String()
		for _, s := range forbidden {
			if strings.Contains(body, s) {
				t.Errorf("%s: утекло %q: %s", where, s, body)
			}
		}
		for _, f := range fields {
			if strings.Contains(body, f) {
				t.Errorf("%s: есть поле %s -- пароль уедет в него завтра", where, f)
			}
		}
		if rec.Code == http.StatusOK {
			checked++
		}
	}
	for _, uid := range []int64{999, 100} {
		for _, p := range paths {
			check(fmt.Sprintf("tg %d %s", uid, p), get(p, uid, false))
		}
	}
	for _, p := range append(paths, "/v1/dashboard/summary") {
		check("дашборд "+p, get(p, 0, true))
	}
	// Ответ «Забыть пароль» -- тоже ответ.
	check("забыть пароль", miniappDo(t, env.h, http.MethodDelete, credentialsPath(id), "", 999))
	if checked < 10 {
		t.Fatalf("сторож проверил только %d ответов 200 -- маршруты не отвечают, проверка вхолостую", checked)
	}
	assertNoReviveSecrets(t, "журнал", env.logs.String())
}
