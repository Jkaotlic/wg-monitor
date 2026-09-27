package backend

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
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
