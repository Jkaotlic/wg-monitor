package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

func provisionBody(fields map[string]any) string {
	body := map[string]any{
		"kind":          "provision",
		"nickname":      "router-new",
		"agent_kind":    "static",
		"awgm_url":      "https://panel.example.com",
		"awgm_auth":     "web",
		"root_password": miniappReviveRoot,
		"awgm_login":    miniappReviveLogin,
		"awgm_password": miniappRevivePanel,
		"awgm_api_key":  miniappReviveKey,
		"version":       "",
		"confirm":       "router-new",
	}
	for k, v := range fields {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

const provisionPath = "/v1/miniapp/provision"

func TestMiniappProvisionHiddenFromNonAdmin(t *testing.T) {
	env := newAdminOpsEnv(t)
	for _, kind := range []string{"provision", "register"} {
		rec := miniappDo(t, env.h, http.MethodPost, provisionPath, provisionBody(map[string]any{"kind": kind}), 100)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s владельцу: код %d", kind, rec.Code)
		}
		assertNoReviveSecrets(t, "ответ владельцу", rec.Body.String())
	}
	if _, err := env.d.Users().GetByNickname("router-new"); !errors.Is(err, db.ErrUserNotFound) {
		t.Fatalf("роутер создан не-админом: %v", err)
	}
	if env.relay.callCount() != 0 {
		t.Fatal("не-админ дошёл до роутера")
	}
}

func TestMiniappProvisionInstallRunsJobAndHidesSecrets(t *testing.T) {
	env := newAdminOpsEnv(t)
	rec := miniappDo(t, env.h, http.MethodPost, provisionPath, provisionBody(nil), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("установка: код %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		JobID    string `json:"job_id"`
		Nickname string `json:"nickname"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.JobID == "" || resp.Nickname != "router-new" {
		t.Fatalf("ответ %s err=%v", rec.Body.String(), err)
	}
	job := waitForProvisionTerminal(t, env.store, resp.JobID, time.Second)
	if job.State != provision.StateSuccess || job.Version != "v0.36.0" {
		t.Fatalf("задание: %+v (версия по умолчанию -- версия бэкенда)", job)
	}
	var captured awgmInstallJob
	if err := json.Unmarshal(env.relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.TerminalPassword != miniappReviveRoot || captured.Password != miniappRevivePanel || captured.APIKey != miniappReviveKey {
		t.Fatalf("учётные данные не дошли до движка")
	}
	u, err := env.d.Users().GetByNickname("router-new")
	if err != nil || stringValue(u.AWGMURL) != "https://panel.example.com" {
		t.Fatalf("роутер после установки: %+v err=%v", u, err)
	}
	if int64Value(u.TelegramChatID) != 0 || int64Value(u.TelegramThreadID) != 0 {
		t.Fatalf("мини-апп тронул группу/тему: chat=%d thread=%d", int64Value(u.TelegramChatID), int64Value(u.TelegramThreadID))
	}
	assertNoReviveSecrets(t, "ответ", rec.Body.String())
	assertNoReviveSecrets(t, "журнал", env.logs.String())
	if !strings.Contains(env.logs.String(), "credentials=root+panel_login+panel_key") {
		t.Fatalf("в журнале нет видов входа: %s", env.logs.String())
	}
}

func TestMiniappProvisionRegisterReturnsTokenOnce(t *testing.T) {
	env := newAdminOpsEnv(t)
	body := provisionBody(map[string]any{"kind": "register", "agent_kind": "mobile", "awgm_auth": "api-key",
		"root_password": nil, "awgm_login": nil, "awgm_password": nil, "awgm_api_key": nil})
	rec := miniappDo(t, env.h, http.MethodPost, provisionPath, body, 999)
	if rec.Code != http.StatusCreated {
		t.Fatalf("приглашение: код %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Nickname       string `json:"nickname"`
		RawToken       string `json:"raw_token"`
		BackendURL     string `json:"backend_url"`
		InstallCommand string `json:"install_command"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Nickname != "router-new" || len(resp.RawToken) != 64 || resp.BackendURL != "https://backend.example.com" {
		t.Fatalf("ответ: %+v", resp)
	}
	if !strings.Contains(resp.InstallCommand, resp.RawToken) || !strings.Contains(resp.InstallCommand, "/v0.36.0'") {
		t.Fatalf("команда установки без токена или версии: %s", resp.InstallCommand)
	}
	// Суммы -- проверенные сервером (подмена verifiedChecksumsFetcher в стенде),
	// вписанные в скрипт; checksums.txt на роутере не качается.
	for _, sum := range manualInstallSums {
		if !strings.Contains(resp.InstallCommand, sum) {
			t.Fatalf("в команде нет проверенной суммы %s", sum)
		}
	}
	if strings.Contains(resp.InstallCommand, "checksums.txt") {
		t.Fatal("команда качает checksums.txt")
	}
	if strings.Contains(env.logs.String(), resp.RawToken) {
		t.Fatal("токен попал в журнал")
	}
	u, err := env.d.Users().GetByNickname("router-new")
	if err != nil || u.Kind != db.KindMobile || stringValue(u.AWGMAuth) != "api-key" {
		t.Fatalf("роутер: %+v err=%v", u, err)
	}
	if env.relay.callCount() != 0 {
		t.Fatal("приглашение ходило на роутер")
	}

	// Приглашённый, но ни разу не вышедший на связь -- не помеха установке:
	// агента, чей токен сломался бы, ещё нет.
	rec = miniappDo(t, env.h, http.MethodPost, provisionPath, provisionBody(nil), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("установка приглашённого: код %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestMiniappProvisionRefusals(t *testing.T) {
	env := newAdminOpsEnv(t)
	if err := env.d.Users().UpdateLastSeen(env.ownedID); err != nil {
		t.Fatal(err)
	}
	check := func(name string, fields map[string]any, status int, code string) {
		t.Helper()
		rec := miniappDo(t, env.h, http.MethodPost, provisionPath, provisionBody(fields), 999)
		assertOpsError(t, name, rec, status, code)
		assertNoReviveSecrets(t, name, rec.Body.String())
	}

	assertOpsError(t, "битое тело", miniappDo(t, env.h, http.MethodPost, provisionPath, `{"kind":`, 999), http.StatusBadRequest, errCodeBadJSON)
	check("подтверждение", map[string]any{"confirm": "router-other"}, http.StatusBadRequest, "confirm_mismatch")
	check("вид запроса", map[string]any{"kind": "adopt"}, http.StatusBadRequest, "invalid_kind")
	check("имя", map[string]any{"nickname": "Router New", "confirm": "Router New"}, http.StatusBadRequest, "invalid_nickname")
	check("тип роутера", map[string]any{"agent_kind": "boat"}, http.StatusBadRequest, "invalid_kind")
	check("имя занято", map[string]any{"nickname": "router-owned", "confirm": "router-owned"}, http.StatusConflict, "nickname_taken")
	check("приглашение: адрес панели", map[string]any{"kind": "register", "awgm_url": "ftp://panel.example.com"}, http.StatusBadRequest, "invalid_awgm_url")
	check("нет адреса панели", map[string]any{"awgm_url": ""}, http.StatusBadRequest, "no_awgm_url")
	check("нет пароля", map[string]any{"root_password": "  "}, http.StatusBadRequest, "root_password_required")

	if !env.store.TryLock("router-new") {
		t.Fatal("замок занят до теста")
	}
	check("уже идёт", nil, http.StatusConflict, "provision_already_running")
	env.store.Unlock("router-new")

	orig := verifiedChecksumsFetcher
	verifiedChecksumsFetcher = func(context.Context, string, string) (map[string]string, error) {
		return nil, errors.New("fetch checksums: HTTP 404 for https://github.example.com/x")
	}
	check("контрольные суммы", nil, http.StatusBadGateway, "checksums_failed")
	verifiedChecksumsFetcher = orig

	SetVersion("dev") // стенд вернёт прежнюю версию в Cleanup
	origLatest := lookupDashboardLatestVersion
	lookupDashboardLatestVersion = func(context.Context) (string, error) { return "", errors.New("github down") }
	t.Cleanup(func() { lookupDashboardLatestVersion = origLatest })
	check("последняя версия", nil, http.StatusBadGateway, "latest_version_failed")

	if env.relay.callCount() != 0 {
		t.Fatalf("отказы дошли до роутера: %d", env.relay.callCount())
	}
	if _, err := env.d.Users().GetByNickname("router-new"); !errors.Is(err, db.ErrUserNotFound) {
		t.Fatalf("отказы создали роутер: %v", err)
	}
}

func TestMiniappProvisionNotConfigured(t *testing.T) {
	noEngine := newAdminOpsEnv(t, func(d *Deps) { d.Provision = provision.Deps{} })
	assertOpsError(t, "без движка", miniappDo(t, noEngine.h, http.MethodPost, provisionPath, provisionBody(nil), 999),
		http.StatusServiceUnavailable, "provision_not_configured")
	// Приглашению движок не нужен -- только база.
	if rec := miniappDo(t, noEngine.h, http.MethodPost, provisionPath, provisionBody(map[string]any{"kind": "register"}), 999); rec.Code != http.StatusCreated {
		t.Fatalf("приглашение без движка: код %d (%s)", rec.Code, rec.Body.String())
	}

	noURL := newAdminOpsEnv(t, func(d *Deps) { d.PublicBaseURL = "" })
	for _, kind := range []string{"provision", "register"} {
		assertOpsError(t, kind+" без адреса", miniappDo(t, noURL.h, http.MethodPost, provisionPath, provisionBody(map[string]any{"kind": kind}), 999),
			http.StatusInternalServerError, "no_public_base_url")
	}
}

func TestMiniappProvisionReqHidesSecretsWhenPrinted(t *testing.T) {
	req := miniappProvisionReq{RootPassword: miniappReviveRoot, AWGMLogin: miniappReviveLogin, AWGMPassword: miniappRevivePanel, AWGMAPIKey: miniappReviveKey}
	for _, s := range []string{req.String(), req.GoString(), req.LogValue().String()} {
		assertNoReviveSecrets(t, "miniappProvisionReq", s)
	}
}

// Проверенные суммы не получены -- токен выдаётся, команды нет: скрипт без
// сверки с подписанным выпуском экран не показывает.
func TestMiniappProvisionRegisterWithoutVerifiedSumsGivesTokenOnly(t *testing.T) {
	env := newAdminOpsEnv(t)
	orig := verifiedChecksumsFetcher
	verifiedChecksumsFetcher = func(context.Context, string, string) (map[string]string, error) {
		return nil, errors.New("signature mismatch")
	}
	t.Cleanup(func() { verifiedChecksumsFetcher = orig })
	rec := miniappDo(t, env.h, http.MethodPost, provisionPath, provisionBody(map[string]any{"kind": "register"}), 999)
	if rec.Code != http.StatusCreated {
		t.Fatalf("код %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		RawToken       string `json:"raw_token"`
		InstallCommand string `json:"install_command"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.RawToken) != 64 {
		t.Fatalf("ответ %s err=%v", rec.Body.String(), err)
	}
	if resp.InstallCommand != "" || !strings.Contains(rec.Body.String(), `"install_command":""`) {
		t.Fatalf("без проверенных сумм ждали пустую команду: %q", resp.InstallCommand)
	}
}

// Приглашение перевыпускает токен -- под тем же замком, что установка: иначе
// идущая установка этого роутера закоммитит токен, который приглашение уже
// переписало.
func TestMiniappProvisionRegisterRefusedWhileInstallRuns(t *testing.T) {
	env := newAdminOpsEnv(t)
	if !env.store.TryLock("router-new") {
		t.Fatal("замок занят до теста")
	}
	rec := miniappDo(t, env.h, http.MethodPost, provisionPath, provisionBody(map[string]any{"kind": "register"}), 999)
	assertOpsError(t, "приглашение при идущей установке", rec, http.StatusConflict, "provision_already_running")
	env.store.Unlock("router-new")
	if _, err := env.d.Users().GetByNickname("router-new"); !errors.Is(err, db.ErrUserNotFound) {
		t.Fatalf("отказ создал роутер: %v", err)
	}
	if rec := miniappDo(t, env.h, http.MethodPost, provisionPath, provisionBody(map[string]any{"kind": "register"}), 999); rec.Code != http.StatusCreated {
		t.Fatalf("после снятия замка: код %d (%s)", rec.Code, rec.Body.String())
	}
}

// Повторное приглашение ещё не выходившего на связь роутера без типа не
// сбрасывает «в машине» в «дома».
func TestMiniappProvisionReinviteKeepsKind(t *testing.T) {
	env := newAdminOpsEnv(t)
	first := provisionBody(map[string]any{"kind": "register", "agent_kind": "mobile"})
	if rec := miniappDo(t, env.h, http.MethodPost, provisionPath, first, 999); rec.Code != http.StatusCreated {
		t.Fatalf("первое приглашение: код %d (%s)", rec.Code, rec.Body.String())
	}
	again := provisionBody(map[string]any{"kind": "register", "agent_kind": ""})
	if rec := miniappDo(t, env.h, http.MethodPost, provisionPath, again, 999); rec.Code != http.StatusCreated {
		t.Fatalf("повторное приглашение: код %d (%s)", rec.Code, rec.Body.String())
	}
	u, err := env.d.Users().GetByNickname("router-new")
	if err != nil || u.Kind != db.KindMobile {
		t.Fatalf("тип после повторного приглашения: %+v err=%v", u, err)
	}
}
