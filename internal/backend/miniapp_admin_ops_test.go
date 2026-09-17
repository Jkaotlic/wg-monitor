package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// adminOpsEnv -- стенд админских операций цикла 2: настоящий mux, движок
// установки на фейковом relay, файл заявки раскатки во временном каталоге,
// журнал в буфере (сторож секретов читает его).
type adminOpsEnv struct {
	d          *db.DB
	ownedID    int64
	h          http.Handler
	relay      *fakeProvisionRelay
	store      *provision.Store
	logs       *bytes.Buffer
	sink       *dashboardActionSink
	updatePath string
}

func newAdminOpsEnv(t *testing.T, mods ...func(*Deps)) *adminOpsEnv {
	t.Helper()
	old := serverVersion
	SetVersion("v0.36.0")
	t.Cleanup(func() { SetVersion(old) })
	stubLatestVersion(t, "v0.36.0")
	stubVerifiedChecksums(t, manualInstallSums)
	// Адрес зеркала резолвится так же, как у дашборда: сеть в тестах не нужна.
	stubRepoResolve(t, map[string]string{"backend.example.com": "203.0.113.5"})
	d, ownedID, _, _ := seedMiniappFleet(t)
	env := &adminOpsEnv{
		d:          d,
		ownedID:    ownedID,
		relay:      &fakeProvisionRelay{rc: 0, lines: []string{"__WG_STEP__ terminal_connected", "__WG_STEP__ config_written"}},
		store:      provision.NewStore(),
		logs:       &bytes.Buffer{},
		sink:       &dashboardActionSink{},
		updatePath: filepath.Join(t.TempDir(), "backend-update.json"),
	}
	deps := Deps{
		DB:                  d,
		Logger:              slog.New(slog.NewTextHandler(env.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		CommandSink:         env.sink,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		DashboardToken:      webTestDash,
		PublicBaseURL:       "https://backend.example.com",
		PublicIP:            "203.0.113.5",
		BackendUpdatePath:   env.updatePath,
		Provision: provision.Deps{
			Store:    env.store,
			BaseCtx:  context.Background(),
			Relay:    env.relay.run,
			LastSeen: freshLastSeen,
		},
	}
	for _, m := range mods {
		m(&deps)
	}
	env.h = NewMux(deps)
	return env
}

// miniappDo -- запрос сессией Telegram от имени uid (999 -- админ, 100 --
// владелец router-owned).
func miniappDo(t *testing.T, h http.Handler, method, path, body string, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", uid))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var cyrillic = regexp.MustCompile(`[А-Яа-яЁё]`)

// assertOpsError -- статус, код в обоих ключах и русский текст из таблицы.
func assertOpsError(t *testing.T, name string, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("%s: код %d, ждали %d (%s)", name, rec.Code, status, rec.Body.String())
	}
	gotCode, errField, msg := decodeDeployError(t, rec)
	if gotCode != code || errField != code {
		t.Fatalf("%s: code=%q error=%q, ждали %q", name, gotCode, errField, code)
	}
	if !cyrillic.MatchString(msg) {
		t.Fatalf("%s: текст не по-русски: %q", name, msg)
	}
}

// Тексты из спеки -- дословно: их читает человек, и клиент показывает их как есть.
func TestMiniappAdminOpsErrorTextMatchesSpec(t *testing.T) {
	want := map[string]string{
		"confirm_mismatch":              "Подтверждение не совпало",
		"provision_not_configured":      "Установка агентов на сервере не настроена",
		"root_password_required":        "Нужен пароль root",
		"no_awgm_url":                   "Нужен адрес панели awg-manager",
		"invalid_nickname":              "Имя роутера: латиница, цифры и дефис",
		"provision_already_running":     "Установка на этот роутер уже идёт",
		"latest_version_failed":         "Не удалось узнать последнюю версию — повторите через минуту",
		"checksums_failed":              "Не удалось скачать контрольные суммы релиза",
		"downgrade_rejected":            "Это откат версии — подтвердите откат",
		"backend_update_not_configured": "Раскатка бэкенда на этом сервере не настроена",
		"job_not_found":                 "Задание не найдено или истекло",
	}
	for code, text := range want {
		if got := miniappAdminOpsErrorText(code); got != text {
			t.Errorf("%s: %q, ждали %q", code, got, text)
		}
	}
	// Каждый код, который отдают маршруты цикла 2, -- со своим текстом, а не
	// с запасным «не получилось».
	fallback := miniappAdminOpsErrorText("no-such-code")
	for _, code := range []string{
		errCodeBadJSON, "not_found", "invalid_kind", "invalid_awgm_url", "nickname_taken", "no_public_base_url",
		"router_offline", "invalid_backend_url", "invalid_arch", "db_not_configured", "backend_downgrade_rejected",
	} {
		if got := miniappAdminOpsErrorText(code); got == fallback || !cyrillic.MatchString(got) {
			t.Errorf("%s: нет своего русского текста (%q)", code, got)
		}
	}
	if !cyrillic.MatchString(fallback) {
		t.Errorf("запасной текст не по-русски: %q", fallback)
	}
}

func TestWriteMiniappStartErrorMapsEngineConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	writeMiniappStartError(rec, &repairStartError{http.StatusConflict, "already_running", "provision already running for bronya"})
	assertOpsError(t, "движок занят", rec, http.StatusConflict, "provision_already_running")
	if strings.Contains(rec.Body.String(), "bronya") || strings.Contains(rec.Body.String(), "already running") {
		t.Fatalf("английский текст ядра утёк: %s", rec.Body.String())
	}
}

func TestMiniappAgentVersionOrServer(t *testing.T) {
	old := serverVersion
	t.Cleanup(func() { SetVersion(old) })
	SetVersion("v0.36.0")
	if got := miniappAgentVersionOrServer(" v0.35.1 "); got != "v0.35.1" {
		t.Fatalf("явная версия: %q", got)
	}
	if got := miniappAgentVersionOrServer(""); got != "v0.36.0" {
		t.Fatalf("по умолчанию: %q", got)
	}
	SetVersion("dev")
	if got := miniappAgentVersionOrServer(""); got != "" {
		t.Fatalf("бэкенд без тега: %q, ждали пусто (последний выпуск)", got)
	}
}

func TestMiniappBackendDeploy(t *testing.T) {
	env := newAdminOpsEnv(t)
	const path = "/v1/miniapp/backend/deploy"

	rec := miniappDo(t, env.h, http.MethodPost, path, `{"target_version":"v0.37.0","confirm":"v0.37.0"}`, 100)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("владельцу: код %d", rec.Code)
	}
	assertOpsError(t, "битое тело", miniappDo(t, env.h, http.MethodPost, path, `{`, 999), http.StatusBadRequest, errCodeBadJSON)
	assertOpsError(t, "подтверждение", miniappDo(t, env.h, http.MethodPost, path, `{"target_version":"v0.37.0","confirm":"v0.37"}`, 999), http.StatusBadRequest, "confirm_mismatch")
	assertOpsError(t, "тег", miniappDo(t, env.h, http.MethodPost, path, `{"target_version":"latest","confirm":"latest"}`, 999), http.StatusBadRequest, errCodeBadJSON)
	// Откат бэкенда из приложения не делается: allow_downgrade не читается.
	rec = miniappDo(t, env.h, http.MethodPost, path, `{"target_version":"v0.35.0","confirm":"v0.35.0","allow_downgrade":true}`, 999)
	assertOpsError(t, "откат", rec, http.StatusBadRequest, "downgrade_rejected")
	if _, _, msg := decodeDeployError(t, rec); msg != miniappAdminOpsErrorText("backend_downgrade_rejected") {
		t.Fatalf("текст отката бэкенда: %q", msg)
	}
	if _, err := os.Stat(env.updatePath); !os.IsNotExist(err) {
		t.Fatalf("отказы записали заявку: %v", err)
	}

	rec = miniappDo(t, env.h, http.MethodPost, path, `{"target_version":"v0.37.0","confirm":" v0.37.0 "}`, 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("раскатка: код %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Accepted      bool   `json:"accepted"`
		TargetVersion string `json:"target_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !resp.Accepted || resp.TargetVersion != "v0.37.0" {
		t.Fatalf("ответ %s err=%v", rec.Body.String(), err)
	}
	raw, err := os.ReadFile(env.updatePath)
	if err != nil {
		t.Fatal(err)
	}
	var queued backendUpdateRequest
	if err := json.Unmarshal(raw, &queued); err != nil {
		t.Fatal(err)
	}
	if queued.TargetVersion != "v0.37.0" || queued.RepoBase != "https://backend.example.com/v1/releases/download" ||
		queued.RepoResolveIP != "203.0.113.5" || queued.TrustedBackendURL != "https://backend.example.com" || queued.AllowDowngrade {
		t.Fatalf("заявка: %+v", queued)
	}
}

func TestMiniappBackendDeployNotConfigured(t *testing.T) {
	env := newAdminOpsEnv(t, func(d *Deps) { d.BackendUpdatePath = "" })
	rec := miniappDo(t, env.h, http.MethodPost, "/v1/miniapp/backend/deploy", `{"target_version":"v0.37.0","confirm":"v0.37.0"}`, 999)
	assertOpsError(t, "не настроено", rec, http.StatusServiceUnavailable, "backend_update_not_configured")

	env = newAdminOpsEnv(t, func(d *Deps) { d.PublicBaseURL = "http://127.0.0.1:8080" })
	rec = miniappDo(t, env.h, http.MethodPost, "/v1/miniapp/backend/deploy", `{"target_version":"v0.37.0","confirm":"v0.37.0"}`, 999)
	assertOpsError(t, "нет публичного адреса", rec, http.StatusInternalServerError, "no_public_base_url")
}

// Браузерный вход -- тот же маршрут: кука веб-управления, JSON обязателен.
func TestMiniappBackendDeployFromWebEntry(t *testing.T) {
	env := newAdminOpsEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/miniapp/backend/deploy", strings.NewReader(`{"target_version":"v0.37.0","confirm":"v0.37.0"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(webDashCookie(t))
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("из браузера: код %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestMiniappJob(t *testing.T) {
	env := newAdminOpsEnv(t)
	known := env.store.Create(provision.KindProvision, "router-owned", provision.Template(provision.KindProvision))
	fresh := env.store.Create(provision.KindProvision, "brand-new", provision.Template(provision.KindProvision))

	if rec := miniappDo(t, env.h, http.MethodGet, "/v1/miniapp/jobs/"+known.ID, "", 100); rec.Code != http.StatusNotFound {
		t.Fatalf("владельцу: код %d", rec.Code)
	}
	assertOpsError(t, "нет задания", miniappDo(t, env.h, http.MethodGet, "/v1/miniapp/jobs/0000000000000000", "", 999), http.StatusNotFound, "job_not_found")

	rec := miniappDo(t, env.h, http.MethodGet, "/v1/miniapp/jobs/"+known.ID, "", 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("задание: код %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Форма постоянная: пустые строки есть, detail у каждого шага есть.
	for _, key := range []string{`"version":""`, `"hint":""`, `"tail":""`, `"detail":""`, `"state":"running"`, `"kind":"provision"`, `"status":"pending"`} {
		if !strings.Contains(body, key) {
			t.Errorf("в ответе нет %s: %s", key, body)
		}
	}
	var got struct {
		ID       string `json:"id"`
		Nickname string `json:"nickname"`
		RouterID *int64 `json:"router_id"`
		Steps    []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != known.ID || got.Nickname != "router-owned" || got.RouterID == nil || *got.RouterID != env.ownedID || len(got.Steps) != 8 {
		t.Fatalf("задание: %+v", got)
	}

	rec = miniappDo(t, env.h, http.MethodGet, "/v1/miniapp/jobs/"+fresh.ID, "", 999)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"router_id":null`) {
		t.Fatalf("новый роутер: код %d тело %s", rec.Code, rec.Body.String())
	}

	noEngine := newAdminOpsEnv(t, func(d *Deps) { d.Provision = provision.Deps{} })
	assertOpsError(t, "без движка", miniappDo(t, noEngine.h, http.MethodGet, "/v1/miniapp/jobs/"+known.ID, "", 999), http.StatusServiceUnavailable, "provision_not_configured")
}

// stubRepoResolve -- DNS для адреса зеркала раскатки бэкенда.
func stubRepoResolve(t *testing.T, hosts map[string]string) {
	t.Helper()
	old := lookupHostForRepoResolve
	lookupHostForRepoResolve = func(host string) ([]string, error) {
		if ip, ok := hosts[host]; ok {
			return []string{ip}, nil
		}
		return nil, errors.New("no such host")
	}
	t.Cleanup(func() { lookupHostForRepoResolve = old })
}

// Мини-апп и дашборд пишут одну и ту же заявку: адрес зеркала и IP для него
// считает ядро queueBackendUpdate, а не каждый обработчик по-своему.
func TestMiniappBackendDeployMatchesDashboardRequest(t *testing.T) {
	env := newAdminOpsEnv(t, func(d *Deps) { d.PublicIP = "198.51.100.77"; d.WizardToken = "wiz-secret" })
	rec := miniappDo(t, env.h, http.MethodPost, "/v1/miniapp/backend/deploy", `{"target_version":"v0.37.0","confirm":"v0.37.0"}`, 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("мини-апп: код %d (%s)", rec.Code, rec.Body.String())
	}
	fromMiniapp := readBackendUpdate(t, env.updatePath)

	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/backend/deploy", strings.NewReader(`{"target_version":"v0.37.0"}`))
	req.Header.Set("Authorization", "Bearer wiz-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Host = "192.168.0.87"
	rec = httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("дашборд: код %d (%s)", rec.Code, rec.Body.String())
	}
	fromDashboard := readBackendUpdate(t, env.updatePath)

	if fromMiniapp.RepoResolveIP != "203.0.113.5" {
		t.Fatalf("мини-апп взял не DNS адреса зеркала: %+v", fromMiniapp)
	}
	fromMiniapp.RequestedAt, fromDashboard.RequestedAt = "", ""
	if fromMiniapp != fromDashboard {
		t.Fatalf("заявки разошлись:\nмини-апп %+v\nдашборд  %+v", fromMiniapp, fromDashboard)
	}
}

func readBackendUpdate(t *testing.T, path string) backendUpdateRequest {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out backendUpdateRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
