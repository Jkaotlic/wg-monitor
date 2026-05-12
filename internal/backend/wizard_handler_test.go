package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}
