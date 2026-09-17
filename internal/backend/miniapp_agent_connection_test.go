package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func connectionPath(id int64) string {
	return fmt.Sprintf("/v1/miniapp/routers/%d/agent/connection", id)
}

func seedConnection(t *testing.T, env *adminOpsEnv) {
	t.Helper()
	if err := env.d.Users().UpdateDeployInfo("router-owned", db.DeployInfo{
		AWGMURL: "https://panel.example.com", AWGMAuth: "router-admin",
		SSHHost: "198.51.100.20", SSHPort: 2222, SSHUser: "root",
		DeployMode: "awgm", Arch: "arm64", Ring: "rc", ExpectedMAC: "02:00:00:00:00:01",
		LastDeployedVersion: "v0.35.0",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMiniappAgentConnectionHiddenFromNonAdmin(t *testing.T) {
	env := newAdminOpsEnv(t)
	seedConnection(t, env)
	if rec := miniappDo(t, env.h, http.MethodGet, connectionPath(env.ownedID), "", 100); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "panel.example.com") {
		t.Fatalf("GET владельцу: код %d тело %s", rec.Code, rec.Body.String())
	}
	if rec := miniappDo(t, env.h, http.MethodPut, connectionPath(env.ownedID), `{"awgm_url":"https://evil.example.com"}`, 100); rec.Code != http.StatusNotFound {
		t.Fatalf("PUT владельцу: код %d", rec.Code)
	}
	u, _ := env.d.Users().GetByNickname("router-owned")
	if stringValue(u.AWGMURL) != "https://panel.example.com" {
		t.Fatalf("владелец поменял адрес панели: %q", stringValue(u.AWGMURL))
	}
}

func TestMiniappAgentConnectionGet(t *testing.T) {
	env := newAdminOpsEnv(t)
	seedConnection(t, env)
	rec := miniappDo(t, env.h, http.MethodGet, connectionPath(env.ownedID), "", 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d (%s)", rec.Code, rec.Body.String())
	}
	var got miniappAgentConnection
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := miniappAgentConnection{
		AWGMURL: "https://panel.example.com", AWGMAuth: "router-admin", SSHHost: "198.51.100.20", SSHPort: 2222,
		SSHUser: "root", DeployMode: "awgm", Arch: "arm64", Ring: "rc", ExpectedMAC: "02:00:00:00:00:01",
	}
	if got != want {
		t.Fatalf("получили %+v, ждали %+v", got, want)
	}
	other, _ := env.d.Users().GetByNickname("router-other")
	rec = miniappDo(t, env.h, http.MethodGet, connectionPath(other.ID), "", 999)
	for _, key := range []string{`"awgm_url":""`, `"ssh_port":0`, `"expected_mac":""`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("пустой роутер: нет %s в %s", key, rec.Body.String())
		}
	}
	assertOpsError(t, "нет роутера", miniappDo(t, env.h, http.MethodGet, connectionPath(424242), "", 999), http.StatusNotFound, "not_found")
}

func TestMiniappAgentConnectionPutKeepsBlankFields(t *testing.T) {
	env := newAdminOpsEnv(t)
	seedConnection(t, env)
	rec := miniappDo(t, env.h, http.MethodPut, connectionPath(env.ownedID), `{"awgm_url":"https://new-panel.example.com","arch":"aarch64","ssh_port":0,"ring":""}`, 999)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("код %d (%s)", rec.Code, rec.Body.String())
	}
	u, err := env.d.Users().GetByNickname("router-owned")
	if err != nil {
		t.Fatal(err)
	}
	if stringValue(u.AWGMURL) != "https://new-panel.example.com" || stringValue(u.Arch) != "arm64" {
		t.Fatalf("не записалось: url=%q arch=%q", stringValue(u.AWGMURL), stringValue(u.Arch))
	}
	if stringValue(u.Ring) != "rc" || stringValue(u.SSHHost) != "198.51.100.20" || int64Value(u.SSHPort) != 2222 ||
		stringValue(u.AWGMAuth) != "router-admin" || stringValue(u.LastDeployedVersion) != "v0.35.0" {
		t.Fatalf("пустое поле стёрло значение: %+v", u)
	}
}

func TestMiniappAgentConnectionPutRefusals(t *testing.T) {
	env := newAdminOpsEnv(t)
	seedConnection(t, env)
	p := connectionPath(env.ownedID)
	assertOpsError(t, "битое тело", miniappDo(t, env.h, http.MethodPut, p, `{"arch":`, 999), http.StatusBadRequest, errCodeBadJSON)
	assertOpsError(t, "архитектура", miniappDo(t, env.h, http.MethodPut, p, `{"arch":"x86"}`, 999), http.StatusBadRequest, "invalid_arch")
	assertOpsError(t, "адрес панели", miniappDo(t, env.h, http.MethodPut, p, `{"awgm_url":"ftp://panel.example.com"}`, 999), http.StatusBadRequest, "invalid_awgm_url")
	assertOpsError(t, "нет роутера", miniappDo(t, env.h, http.MethodPut, connectionPath(424242), `{"arch":"arm64"}`, 999), http.StatusNotFound, "not_found")
	u, _ := env.d.Users().GetByNickname("router-owned")
	if stringValue(u.Arch) != "arm64" || stringValue(u.AWGMURL) != "https://panel.example.com" {
		t.Fatalf("отказ что-то записал: %+v", u)
	}
}

func TestMiniappAgentConnectionPutFromWebEntry(t *testing.T) {
	env := newAdminOpsEnv(t)
	seedConnection(t, env)
	req := httptest.NewRequest(http.MethodPut, connectionPath(env.ownedID), strings.NewReader(`{"ring":"stable"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(webDashCookie(t))
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("из браузера: код %d (%s)", rec.Code, rec.Body.String())
	}
}
