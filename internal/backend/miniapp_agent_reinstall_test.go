package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// reinstallEnv -- router-owned на связи, с адресом панели и агентом v0.35.0;
// router-other ни разу не выходил на связь (у него адреса панели пока нет).
func reinstallEnv(t *testing.T) (*adminOpsEnv, int64) {
	t.Helper()
	env := newAdminOpsEnv(t)
	if err := env.d.Users().UpdateDeployInfo("router-owned", db.DeployInfo{AWGMURL: "https://panel.example.com", LastDeployedVersion: "v0.35.0"}); err != nil {
		t.Fatal(err)
	}
	if err := env.d.Users().UpdateLastSeen(env.ownedID); err != nil {
		t.Fatal(err)
	}
	other, err := env.d.Users().GetByNickname("router-other")
	if err != nil {
		t.Fatal(err)
	}
	return env, other.ID
}

func reinstallPath(id int64) string { return fmt.Sprintf("/v1/miniapp/routers/%d/agent/reinstall", id) }
func repointPath(id int64) string   { return fmt.Sprintf("/v1/miniapp/routers/%d/agent/repoint", id) }

func secretsBody(fields map[string]any) string {
	body := map[string]any{
		"root_password": miniappReviveRoot,
		"awgm_login":    miniappReviveLogin,
		"awgm_password": miniappRevivePanel,
		"awgm_api_key":  miniappReviveKey,
	}
	for k, v := range fields {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func decodeJobStart(t *testing.T, name string, raw []byte) string {
	t.Helper()
	var resp miniappJobStartResp
	if err := json.Unmarshal(raw, &resp); err != nil || resp.JobID == "" {
		t.Fatalf("%s: ответ %s err=%v", name, raw, err)
	}
	return resp.JobID
}

func TestMiniappAgentReinstallAndRepointHiddenFromNonAdmin(t *testing.T) {
	env, _ := reinstallEnv(t)
	for _, path := range []string{reinstallPath(env.ownedID), repointPath(env.ownedID), reinstallPath(424242)} {
		rec := miniappDo(t, env.h, http.MethodPost, path, secretsBody(map[string]any{"confirm": "router-owned"}), 100)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s владельцу: код %d", path, rec.Code)
		}
		assertNoReviveSecrets(t, path, rec.Body.String())
	}
	if env.relay.callCount() != 0 {
		t.Fatal("не-админ дошёл до роутера")
	}
}

func TestMiniappAgentReinstallStartsJob(t *testing.T) {
	env, _ := reinstallEnv(t)
	rec := miniappDo(t, env.h, http.MethodPost, reinstallPath(env.ownedID), secretsBody(map[string]any{"confirm": "Router-Owned "}), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("переустановка: код %d (%s)", rec.Code, rec.Body.String())
	}
	jobID := decodeJobStart(t, "переустановка", rec.Body.Bytes())
	job := waitForProvisionTerminal(t, env.store, jobID, time.Second)
	if job.Kind != provision.KindRepairReinstall || job.State != provision.StateSuccess || job.Version != "v0.36.0" {
		t.Fatalf("задание: %+v", job)
	}
	var captured awgmInstallJob
	if err := json.Unmarshal(env.relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.TerminalPassword != miniappReviveRoot || captured.BaseURL != "https://panel.example.com" {
		t.Fatalf("задание relay: base=%q", captured.BaseURL)
	}
	assertNoReviveSecrets(t, "ответ", rec.Body.String())
	assertNoReviveSecrets(t, "журнал", env.logs.String())
}

func TestMiniappAgentReinstallRefusals(t *testing.T) {
	env, otherID := reinstallEnv(t)
	path := reinstallPath(env.ownedID)
	check := func(name, p, body string, status int, code string) {
		t.Helper()
		rec := miniappDo(t, env.h, http.MethodPost, p, body, 999)
		assertOpsError(t, name, rec, status, code)
		assertNoReviveSecrets(t, name, rec.Body.String())
	}
	check("битое тело", path, `{"confirm":`, http.StatusBadRequest, errCodeBadJSON)
	check("подтверждение", path, secretsBody(map[string]any{"confirm": "router-other"}), http.StatusBadRequest, "confirm_mismatch")
	check("нет пароля", path, secretsBody(map[string]any{"confirm": "router-owned", "root_password": " "}), http.StatusBadRequest, "root_password_required")
	check("откат", path, secretsBody(map[string]any{"confirm": "router-owned", "version": "v0.34.0"}), http.StatusBadRequest, "downgrade_rejected")
	// Не на связи -- для него «Оживить агент», а не немедленная переустановка.
	check("не на связи", reinstallPath(otherID), secretsBody(map[string]any{"confirm": "router-other"}), http.StatusConflict, "router_offline")
	check("нет роутера", reinstallPath(424242), secretsBody(map[string]any{"confirm": "x"}), http.StatusNotFound, "not_found")

	if _, err := env.d.SQL().Exec(`UPDATE users SET awgm_url = NULL WHERE id = ?`, env.ownedID); err != nil {
		t.Fatal(err)
	}
	check("нет адреса панели", path, secretsBody(map[string]any{"confirm": "router-owned"}), http.StatusBadRequest, "no_awgm_url")

	if env.relay.callCount() != 0 {
		t.Fatalf("отказы дошли до роутера: %d", env.relay.callCount())
	}
	assertNoReviveSecrets(t, "журнал", env.logs.String())
}

func TestMiniappAgentRepoint(t *testing.T) {
	env, otherID := reinstallEnv(t)

	rec := miniappDo(t, env.h, http.MethodPost, repointPath(env.ownedID),
		secretsBody(map[string]any{"confirm": "router-owned", "new_backend_url": "https://other.example.com"}), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("перенаправление: код %d (%s)", rec.Code, rec.Body.String())
	}
	jobID := decodeJobStart(t, "перенаправление", rec.Body.Bytes())
	job := waitForProvisionTerminal(t, env.store, jobID, time.Second)
	if job.Kind != provision.KindRepairRepoint || len(job.Steps) != 4 {
		t.Fatalf("задание: %+v", job)
	}
	var captured awgmReviveJob
	if err := json.Unmarshal(env.relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.TerminalPassword != miniappReviveRoot || !strings.Contains(captured.BootstrapScript, "'https://other.example.com'") {
		t.Fatalf("скрипт не на новый адрес")
	}
	assertNoReviveSecrets(t, "ответ", rec.Body.String())
	assertNoReviveSecrets(t, "журнал", env.logs.String())

	// Пустой адрес -- публичный адрес этого сервера. Роутер не на связи --
	// не помеха: агент как раз и стучится не туда.
	if err := env.d.Users().UpdateDeployInfo("router-other", db.DeployInfo{AWGMURL: "https://panel-other.example.com"}); err != nil {
		t.Fatal(err)
	}
	rec = miniappDo(t, env.h, http.MethodPost, repointPath(otherID), secretsBody(map[string]any{"confirm": "router-other"}), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("перенаправление на себя: код %d (%s)", rec.Code, rec.Body.String())
	}
	waitForProvisionTerminal(t, env.store, decodeJobStart(t, "на себя", rec.Body.Bytes()), time.Second)
	if err := json.Unmarshal(env.relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured.BootstrapScript, "'https://backend.example.com'") {
		t.Fatalf("пустой адрес не стал публичным адресом сервера")
	}
}

func TestMiniappAgentRepointRefusals(t *testing.T) {
	env, otherID := reinstallEnv(t)
	path := repointPath(env.ownedID)
	check := func(name, p, body string, status int, code string) {
		t.Helper()
		rec := miniappDo(t, env.h, http.MethodPost, p, body, 999)
		assertOpsError(t, name, rec, status, code)
		assertNoReviveSecrets(t, name, rec.Body.String())
	}
	check("подтверждение", path, secretsBody(map[string]any{"confirm": "nope"}), http.StatusBadRequest, "confirm_mismatch")
	check("нет пароля", path, secretsBody(map[string]any{"confirm": "router-owned", "root_password": ""}), http.StatusBadRequest, "root_password_required")
	check("http-адрес", path, secretsBody(map[string]any{"confirm": "router-owned", "new_backend_url": "http://other.example.com"}), http.StatusBadRequest, "invalid_backend_url")
	check("нет адреса панели", repointPath(otherID), secretsBody(map[string]any{"confirm": "router-other"}), http.StatusBadRequest, "no_awgm_url")

	noEngine := newAdminOpsEnv(t, func(d *Deps) { d.Provision = provision.Deps{} })
	if err := noEngine.d.Users().UpdateDeployInfo("router-owned", db.DeployInfo{AWGMURL: "https://panel.example.com"}); err != nil {
		t.Fatal(err)
	}
	assertOpsError(t, "без движка", miniappDo(t, noEngine.h, http.MethodPost, repointPath(noEngine.ownedID),
		secretsBody(map[string]any{"confirm": "router-owned"}), 999), http.StatusServiceUnavailable, "provision_not_configured")
	if env.relay.callCount() != 0 {
		t.Fatalf("отказы дошли до роутера: %d", env.relay.callCount())
	}
}

func TestMiniappReinstallAndRepointReqHideSecrets(t *testing.T) {
	a := miniappAgentReinstallReq{RootPassword: miniappReviveRoot, AWGMPassword: miniappRevivePanel, AWGMAPIKey: miniappReviveKey, AWGMLogin: miniappReviveLogin}
	b := miniappAgentRepointReq{RootPassword: miniappReviveRoot, AWGMPassword: miniappRevivePanel, AWGMAPIKey: miniappReviveKey, AWGMLogin: miniappReviveLogin}
	for _, s := range []string{a.String(), a.GoString(), a.LogValue().String(), b.String(), b.GoString(), b.LogValue().String()} {
		assertNoReviveSecrets(t, "печать запроса", s)
	}
}

// Пароль root с пробелом по краю доходит до задания как есть; одни пробелы --
// root_password_required. И у переустановки, и у перенаправления.
func TestMiniappReinstallAndRepointKeepRootPasswordVerbatim(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(int64) string
		read func(t *testing.T, raw []byte) (root, panel string)
	}{
		{"переустановка", reinstallPath, func(t *testing.T, raw []byte) (string, string) {
			var j awgmInstallJob
			if err := json.Unmarshal(raw, &j); err != nil {
				t.Fatal(err)
			}
			return j.TerminalPassword, j.Password
		}},
		{"перенаправление", repointPath, func(t *testing.T, raw []byte) (string, string) {
			var j awgmReviveJob
			if err := json.Unmarshal(raw, &j); err != nil {
				t.Fatal(err)
			}
			return j.TerminalPassword, j.Password
		}},
	} {
		env, _ := reinstallEnv(t)
		p := tc.path(env.ownedID)
		assertOpsError(t, tc.name+": одни пробелы", miniappDo(t, env.h, http.MethodPost, p,
			secretsBody(map[string]any{"confirm": "router-owned", "root_password": "   "}), 999), http.StatusBadRequest, "root_password_required")
		rec := miniappDo(t, env.h, http.MethodPost, p,
			secretsBody(map[string]any{"confirm": "router-owned", "root_password": " pa ss ", "awgm_password": " panel pw "}), 999)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: код %d (%s)", tc.name, rec.Code, rec.Body.String())
		}
		waitForProvisionTerminal(t, env.store, decodeJobStart(t, tc.name, rec.Body.Bytes()), time.Second)
		if root, panel := tc.read(t, env.relay.capturedJobJSON()); root != " pa ss " || panel != " panel pw " {
			t.Fatalf("%s: пароли изменены по дороге: root=%q panel=%q", tc.name, root, panel)
		}
	}
}
