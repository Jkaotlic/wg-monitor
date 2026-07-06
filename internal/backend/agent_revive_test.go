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
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/provision"
)

func TestBuildReviveScriptRewritesBackendURLAndRestarts(t *testing.T) {
	s := buildReviveScript("https://wg.snekhaev.example")
	for _, want := range []string{
		"NEW='https://wg.snekhaev.example'",
		"/opt/etc/wg-monitor/config.yaml",
		"bak-revive-",
		"/^backend:[ \\t]*$/", // section-aware
		"/opt/etc/init.d/S99wg-monitor restart",
		provision.StepMarker + " " + provision.StepBackendURLRewrite,
		provision.StepMarker + " " + provision.StepServiceRestarted,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("revive script missing %q:\n%s", want, s)
		}
	}

	// The repair-repoint job checklist is terminal_connected (relay-emitted,
	// Task 7) -> backend_url_rewritten -> service_restarted -> verify_online
	// (engine, runner.go). The script itself must emit its two markers in
	// that order, straddling the config rewrite and the agent restart
	// respectively — otherwise the dashboard's checklist stalls forever on a
	// step nothing ever completes (see runner.go's onLine: a step only
	// advances once its marker line arrives).
	mvIdx := strings.Index(s, `mv "$CFG.tmp" "$CFG"`)
	rewriteMarkerIdx := strings.Index(s, provision.StepMarker+" "+provision.StepBackendURLRewrite)
	restartIdx := strings.Index(s, "/opt/etc/init.d/S99wg-monitor restart")
	restartMarkerIdx := strings.Index(s, provision.StepMarker+" "+provision.StepServiceRestarted)
	if mvIdx < 0 || rewriteMarkerIdx < 0 || restartIdx < 0 || restartMarkerIdx < 0 {
		t.Fatalf("script missing one of the expected markers/lines:\n%s", s)
	}
	if !(mvIdx < rewriteMarkerIdx && rewriteMarkerIdx < restartIdx && restartIdx < restartMarkerIdx) {
		t.Fatalf("step markers out of order: mv=%d rewriteMarker=%d restart=%d restartMarker=%d\n%s",
			mvIdx, rewriteMarkerIdx, restartIdx, restartMarkerIdx, s)
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

// newReviveMux builds a real backend mux (NewMux/registerDashboardRoutes)
// with the provisioning engine wired to the given fake relay — Task 10 routes
// dashboardReviveAgentHandler through provision.Deps.Start (KindRepairRepoint)
// instead of the old synchronous runAWGMRelayJob, so every test needs a
// non-nil Provision.Store or the handler's own 503 guard trips (a nil Store
// would otherwise panic the moment the engine's single-flight lock is
// acquired).
func newReviveMux(t *testing.T, awgmURL string, relay *fakeProvisionRelay) (*db.DB, *provision.Store, http.Handler) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	seedReviveAgent(t, d, awgmURL)
	store := provision.NewStore()
	h := NewMux(Deps{
		DB:             d,
		DashboardToken: "secret",
		PublicBaseURL:  "https://wgmonitor.snekhaev.crazedns.ru",
		Provision: provision.Deps{
			Store:    store,
			BaseCtx:  context.Background(),
			Relay:    relay.run,
			LastSeen: freshLastSeen,
		},
	})
	return d, store, h
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

// TestDashboardReviveDrivesRelayWithTransientPassword pins the async
// {job_id, steps} contract dashboardReviveAgentHandler now shares with
// dashboardHandleRepairRepoint (Task 10): the old handler used to run the
// relay synchronously and return {ok, output, new_backend_url}; it now starts
// a KindRepairRepoint engine job and returns 202 immediately, with the job
// reaching StateSuccess once the (stubbed) relay completes.
func TestDashboardReviveDrivesRelayWithTransientPassword(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	_, store, h := newReviveMux(t, "https://awg.snekhaev.crazedns.ru", relay)

	rec := postRevive(t, h, "snekhaev", `{"root_password":"s3cr3t-root"}`, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp dashboardJobStartResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.JobID == "" {
		t.Fatal("job_id missing")
	}
	if len(resp.Steps) == 0 {
		t.Error("steps missing from the start response")
	}

	job := waitForProvisionTerminal(t, store, resp.JobID, time.Second)
	if job.State != provision.StateSuccess {
		t.Fatalf("job state = %q, want success (hint=%q)", job.State, job.Hint)
	}

	if got := relay.callCount(); got != 1 {
		t.Fatalf("relay calls = %d, want 1", got)
	}
	var captured awgmReviveJob
	if err := json.Unmarshal(relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
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
	if relay.capturedRelayPath() != defaultAWGMRelayPath {
		t.Fatalf("relay path: %q", relay.capturedRelayPath())
	}
}

func TestDashboardReviveRequiresRootPassword(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, h := newReviveMux(t, "https://awg.snekhaev.crazedns.ru", relay)

	rec := postRevive(t, h, "snekhaev", `{"root_password":"  "}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if relay.callCount() != 0 {
		t.Fatal("relay must not run without a root password")
	}
}

func TestDashboardReviveNeedsAWGMURL(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, h := newReviveMux(t, "", relay) // no awgm_url
	rec := postRevive(t, h, "snekhaev", `{"root_password":"x"}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardReviveUnknownAgent(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, h := newReviveMux(t, "https://awg.snekhaev.crazedns.ru", relay)
	rec := postRevive(t, h, "ghost", `{"root_password":"x"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardReviveRequiresAuth(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, h := newReviveMux(t, "https://awg.snekhaev.crazedns.ru", relay)
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
