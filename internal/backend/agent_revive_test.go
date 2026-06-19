package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestBuildReviveScriptRewritesBackendURLAndRestarts(t *testing.T) {
	s := buildReviveScript("https://wg.snekhaev.example")
	for _, want := range []string{
		"NEW='https://wg.snekhaev.example'",
		"/opt/etc/wg-monitor/config.yaml",
		"bak-revive-",
		"/^backend:[ \\t]*$/", // section-aware
		"/opt/etc/init.d/S99wg-monitor restart",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("revive script missing %q:\n%s", want, s)
		}
	}
}

func seedReviveAgent(t *testing.T, d *db.DB, awgmURL string) {
	t.Helper()
	if _, err := d.Users().UpsertEnrollment("snekhaev", "tok-snekhaev-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if awgmURL != "" {
		if err := d.Users().UpdateDeployInfo("snekhaev", db.DeployInfo{AWGMURL: awgmURL}); err != nil {
			t.Fatal(err)
		}
	}
}

func newReviveMux(t *testing.T, awgmURL string) (*db.DB, http.Handler) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	seedReviveAgent(t, d, awgmURL)
	h := NewMux(Deps{DB: d, DashboardToken: "secret", PublicBaseURL: "https://wgmonitor.snekhaev.crazedns.ru"})
	return d, h
}

func postRevive(t *testing.T, h http.Handler, nick, body string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/"+nick+"/revive", strings.NewReader(body))
	if auth {
		req.Header.Set("Authorization", "Bearer secret")
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDashboardReviveDrivesRelayWithTransientPassword(t *testing.T) {
	_, h := newReviveMux(t, "https://awg.snekhaev.crazedns.ru")

	var captured awgmReviveJob
	var capturedPath string
	orig := runAWGMRelayJob
	runAWGMRelayJob = func(_ context.Context, relayPath string, job awgmReviveJob) (string, error) {
		captured = job
		capturedPath = relayPath
		return "backend.url -> https://wgmonitor.snekhaev.crazedns.ru\nagent restarted\n", nil
	}
	t.Cleanup(func() { runAWGMRelayJob = orig })

	rec := postRevive(t, h, "snekhaev", `{"root_password":"s3cr3t-root"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
		NewURL string `json:"new_backend_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || !strings.Contains(resp.Output, "agent restarted") {
		t.Fatalf("bad response: %+v", resp)
	}
	// Job carries the transient root password + targets the agent's awgm URL,
	// and the script rewrites to the backend's public URL.
	if captured.TerminalPassword != "s3cr3t-root" || captured.TerminalUser != "root" {
		t.Fatalf("terminal creds not passed: %+v", captured)
	}
	if captured.BaseURL != "https://awg.snekhaev.crazedns.ru" {
		t.Fatalf("relay base_url should be the agent awgm_url: %q", captured.BaseURL)
	}
	if !strings.Contains(captured.BootstrapScript, "https://wgmonitor.snekhaev.crazedns.ru") {
		t.Fatalf("script should rewrite to PublicBaseURL: %s", captured.BootstrapScript)
	}
	if capturedPath != defaultAWGMRelayPath {
		t.Fatalf("relay path: %q", capturedPath)
	}
	if resp.NewURL != "https://wgmonitor.snekhaev.crazedns.ru" {
		t.Fatalf("new url: %q", resp.NewURL)
	}
}

func TestDashboardReviveRequiresRootPassword(t *testing.T) {
	_, h := newReviveMux(t, "https://awg.snekhaev.crazedns.ru")
	called := false
	orig := runAWGMRelayJob
	runAWGMRelayJob = func(context.Context, string, awgmReviveJob) (string, error) { called = true; return "", nil }
	t.Cleanup(func() { runAWGMRelayJob = orig })

	rec := postRevive(t, h, "snekhaev", `{"root_password":"  "}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("relay must not run without a root password")
	}
}

func TestDashboardReviveNeedsAWGMURL(t *testing.T) {
	_, h := newReviveMux(t, "") // no awgm_url
	rec := postRevive(t, h, "snekhaev", `{"root_password":"x"}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardReviveUnknownAgent(t *testing.T) {
	_, h := newReviveMux(t, "https://awg.snekhaev.crazedns.ru")
	rec := postRevive(t, h, "ghost", `{"root_password":"x"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardReviveRequiresAuth(t *testing.T) {
	_, h := newReviveMux(t, "https://awg.snekhaev.crazedns.ru")
	rec := postRevive(t, h, "snekhaev", `{"root_password":"x"}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}
