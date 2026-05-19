package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestWizardAuth_MissingHeader_401(t *testing.T) {
	h := WizardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
	))
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestWizardAuth_WrongToken_401(t *testing.T) {
	h := WizardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
	))
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestWizardAuth_RightToken_200(t *testing.T) {
	h := WizardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
	))
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestWizardList_Empty(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := wizardListAgentsHandler(Deps{DB: d})
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got wizardAgentList
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 0 {
		t.Fatalf("want 0 agents, got %d", len(got.Agents))
	}
}

func TestWizardList_OneAgent(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("alyaba", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("alyaba", db.DeployInfo{
		SSHHost: "192.168.1.1", SSHPort: 222, SSHUser: "root",
		Arch: "mips", LastDeployedVersion: "v0.10.3",
		Ring: "canary", PendingVersion: "v0.11.0", PendingSince: "2026-05-19T10:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h := wizardListAgentsHandler(Deps{DB: d})
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got wizardAgentList
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 1 || got.Agents[0].Nickname != "alyaba" || got.Agents[0].SSHHost != "192.168.1.1" {
		t.Fatalf("unexpected: %+v", got)
	}
	if got.Agents[0].Ring != "canary" || got.Agents[0].PendingVersion != "v0.11.0" {
		t.Fatalf("rollout metadata missing: %+v", got.Agents[0])
	}
}

func TestWizardPut_404OnUnknown(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := wizardPutAgentHandler(Deps{DB: d})
	body := `{"ssh_host":"1.2.3.4","ssh_port":22,"ssh_user":"root","arch":"mips","last_deployed_version":"v0.1"}`
	req := httptest.NewRequest("PUT", "/v1/wizard/agents/ghost", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "ghost")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWizardPut_204Updates(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("alyaba", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	h := wizardPutAgentHandler(Deps{DB: d})
	body := `{"ssh_host":"10.0.0.1","ssh_port":222,"ssh_user":"root","arch":"mips","last_deployed_version":"v0.10.3","ring":"beta","pending_version":"v0.11.0","pending_since":"2026-05-19T10:00:00Z"}`
	req := httptest.NewRequest("PUT", "/v1/wizard/agents/alyaba", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "alyaba")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	u, err := d.Users().GetByNickname("alyaba")
	if err != nil || u.SSHHost == nil || *u.SSHHost != "10.0.0.1" {
		t.Fatalf("not persisted: u=%+v err=%v", u, err)
	}
	if u.Ring == nil || *u.Ring != "beta" || u.PendingVersion == nil || *u.PendingVersion != "v0.11.0" {
		t.Fatalf("rollout metadata not persisted: u=%+v", u)
	}
}

// Locks in the contract that wizard MUST satisfy: any of ssh_host/ssh_port/
// ssh_user/arch missing → 400. Real-world trigger: alyaba/de4ddy entered
// wizard.toml via [5] VPS Sync with ssh_user="" (remote DB had NULL), the
// update flow connected via userOrDefault("root") but never wrote that
// back to ag.User, so post-deploy push sent ssh_user="" → 400. The wizard
// fix backfills ag.User/ag.Port after a successful SSH; this guard ensures
// the backend's rejection contract doesn't drift.
func TestWizardPut_400OnEmptyRequiredFields(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("alyaba", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		body string
	}{
		{"empty ssh_user", `{"ssh_host":"10.0.0.1","ssh_port":222,"ssh_user":"","arch":"arm64"}`},
		{"empty ssh_host", `{"ssh_host":"","ssh_port":222,"ssh_user":"root","arch":"arm64"}`},
		{"zero ssh_port", `{"ssh_host":"10.0.0.1","ssh_port":0,"ssh_user":"root","arch":"arm64"}`},
		{"empty arch", `{"ssh_host":"10.0.0.1","ssh_port":222,"ssh_user":"root","arch":""}`},
	}
	h := wizardPutAgentHandler(Deps{DB: d})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/v1/wizard/agents/alyaba", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("nickname", "alyaba")
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestWizardList_IncludesLastSeenAt(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id, err := d.Users().Insert("smith", "raw-token-xx", "0.0.0.0", "awg0")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := d.Users().UpdateLastSeen(id); err != nil {
		t.Fatalf("update: %v", err)
	}
	h := wizardListAgentsHandler(Deps{DB: d})
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: %d, body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Agents []struct {
			Nickname   string  `json:"nickname"`
			LastSeenAt *string `json:"last_seen_at,omitempty"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Agents) != 1 || got.Agents[0].Nickname != "smith" {
		t.Fatalf("agents: %+v", got.Agents)
	}
	if got.Agents[0].LastSeenAt == nil {
		t.Fatal("expected non-nil last_seen_at after UpdateLastSeen")
	}
}
