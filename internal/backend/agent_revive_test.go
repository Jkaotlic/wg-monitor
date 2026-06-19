package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// The relay is embedded in the backend and self-provisioned, so revive needs no
// wizard step: a missing path yields a temp copy of the real relay python.
func TestResolveRelayScriptSelfProvisionsEmbedded(t *testing.T) {
	path, cleanup, err := resolveRelayScript(filepath.Join(t.TempDir(), "absent.py"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("temp relay not readable: %v", err)
	}
	if !strings.HasPrefix(string(b), "#!/usr/bin/env python3") {
		t.Fatalf("temp file is not the embedded relay: %.40q", string(b))
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove the temp relay, stat err=%v", err)
	}
}

// A wizard-installed copy at the override path is honored as-is (wizard fallback).
func TestResolveRelayScriptUsesInstalledOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "awgm-relay.py")
	if err := os.WriteFile(override, []byte("# installed copy\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := resolveRelayScript(override)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if path != override {
		t.Fatalf("should use the installed override, got %q", path)
	}
}
