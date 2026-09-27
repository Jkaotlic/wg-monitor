package backend

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// PROV-02: пароль root с пробелом по краю -- тоже пароль; дашборд передаёт
// его как набран, как мини-апп.
func TestDashboardProvisionInstall_KeepsRootPasswordVerbatim(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	_, store, mux := newProvisionTestHandler(t, relay, freshLastSeen)
	stubLatestVersion(t, "v0.13.9")
	stubVerifiedChecksums(t, map[string]string{"wg-monitor-agent-linux-arm64": "deadbeef"})
	body := `{"kind":"provision","nickname":"spacerouter","agent_kind":"static",` +
		`"awgm_url":"https://awg.example","root_password":" pw with edges "}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp dashboardJobStartResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	waitForProvisionTerminal(t, store, resp.JobID, time.Second)
	relay.mu.Lock()
	raw := append([]byte(nil), relay.jobJSON...)
	relay.mu.Unlock()
	var job awgmInstallJob
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if job.TerminalPassword != " pw with edges " {
		t.Fatalf("пароль обрезан: %q", job.TerminalPassword)
	}
}

// Та же беда у ремонта из дашборда (переустановка/перенастройка).
func TestDashboardRepairReinstall_KeepsRootPasswordVerbatim(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0, lines: []string{"__WG_STEP__ config_written"}}
	database, store, mux := newProvisionTestHandler(t, relay, freshLastSeen)
	if _, err := database.Users().UpsertEnrollment("client-g", "tok-client-g-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Users().UpdateDeployInfo("client-g", db.DeployInfo{AWGMURL: "https://awg.example", LastDeployedVersion: "v0.13.5"}); err != nil {
		t.Fatal(err)
	}
	stubVerifiedChecksums(t, map[string]string{"a": "b"})
	body := `{"mode":"reinstall","root_password":" pw with edges ","version":"v0.13.9"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/agents/client-g/repair", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp dashboardJobStartResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	waitForProvisionTerminal(t, store, resp.JobID, time.Second)
	relay.mu.Lock()
	raw := append([]byte(nil), relay.jobJSON...)
	relay.mu.Unlock()
	var job awgmInstallJob
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if job.TerminalPassword != " pw with edges " {
		t.Fatalf("пароль обрезан: %q", job.TerminalPassword)
	}
}
