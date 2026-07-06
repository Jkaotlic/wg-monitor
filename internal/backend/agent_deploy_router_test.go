package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/provision"
)

// newDeployRouterMux builds a real backend mux (NewMux/registerDashboardRoutes)
// with the provisioning engine wired to the given fake relay — Task 10 routes
// dashboardDeployRouterHandler through the same engine core
// dashboardHandleProvisionInstall uses (provision.Deps.Start, KindProvision)
// instead of the old synchronous runAWGMInstallJob, so every test needs a
// non-nil Provision.Store or the handler's own 503 guard trips.
func newDeployRouterMux(t *testing.T, awgmURL string, relay *fakeProvisionRelay) (*db.DB, *provision.Store, http.Handler) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Users().UpsertEnrollment("bronya", "tok-bronya-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if awgmURL != "" {
		if err := d.Users().UpdateDeployInfo("bronya", db.DeployInfo{AWGMURL: awgmURL}); err != nil {
			t.Fatal(err)
		}
	}
	store := provision.NewStore()
	h := NewMux(Deps{
		DB:             d,
		DashboardToken: "secret",
		PublicBaseURL:  "https://wgmon.example",
		Provision: provision.Deps{
			Store:    store,
			BaseCtx:  context.Background(),
			Relay:    relay.run,
			LastSeen: freshLastSeen,
		},
	})
	return d, store, h
}

func postDeployRouter(t *testing.T, h http.Handler, nick, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/"+nick+"/deploy-router", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestDeployRouterBuildsInstallJobWithTransientCreds pins the async
// {job_id, steps} contract dashboardDeployRouterHandler now shares with
// dashboardHandleProvisionInstall (Task 10): the old handler ran the relay
// synchronously via the unsigned releaseChecksumsFetcher and returned
// {ok, output, version}; it now must go through the SIGNATURE-VERIFIED
// verifiedChecksumsFetcher, start a KindProvision engine job, and return 202
// immediately (this closes the P0 unsigned-checksums/raw-token/request-ctx
// vuln on this still-registered route).
func TestDeployRouterBuildsInstallJobWithTransientCreds(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	d, store, h := newDeployRouterMux(t, "https://awg.example", relay)

	stubLatestVersion(t, "v0.13.8")

	// Regression pin for the vuln fix: the OLD unsigned fetcher must never be
	// reached now that this route delegates to the engine's shared core.
	origSums := releaseChecksumsFetcher
	releaseChecksumsFetcher = func(context.Context, string) (map[string]string, error) {
		t.Error("deploy-router must use the signature-verified checksums fetcher, not the legacy unsigned one")
		return nil, nil
	}
	t.Cleanup(func() { releaseChecksumsFetcher = origSums })
	stubVerifiedChecksums(t, map[string]string{"wg-monitor-agent-linux-arm64": "aa11"})

	beforeHash := tokenHashOf(t, d, "bronya")

	rec := postDeployRouter(t, h, "bronya",
		`{"root_password":"rootpw","awgm_api_key":"key-123"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
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
	var captured awgmInstallJob
	if err := json.Unmarshal(relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Mode != "bootstrap_install" {
		t.Errorf("Mode = %q", captured.Mode)
	}
	if captured.TerminalPassword != "rootpw" || captured.TerminalUser != "root" {
		t.Errorf("terminal creds = %q/%q", captured.TerminalUser, captured.TerminalPassword)
	}
	if captured.APIKey != "key-123" {
		t.Errorf("api key = %q", captured.APIKey)
	}
	if captured.TargetVersion != "v0.13.8" {
		t.Errorf("version = %q", captured.TargetVersion)
	}
	if captured.BackendURL != "https://wgmon.example" {
		t.Errorf("backend url = %q", captured.BackendURL)
	}
	if captured.ReleaseBase != "https://wgmon.example/v1/releases/download" {
		t.Errorf("release base = %q", captured.ReleaseBase)
	}
	if captured.RawToken == "" || captured.RawToken == "tok-bronya-000000000000000000" {
		t.Errorf("expected a freshly re-minted raw token, got %q", captured.RawToken)
	}
	if captured.Checksums["wg-monitor-agent-linux-arm64"] != "aa11" {
		t.Errorf("checksums not passed: %v", captured.Checksums)
	}
	if captured.InitScript == "" {
		t.Error("init script missing")
	}
	if relay.capturedRelayPath() != defaultAWGMRelayPath {
		t.Errorf("relay path: %q", relay.capturedRelayPath())
	}
	if h := tokenHashOf(t, d, "bronya"); h == beforeHash {
		t.Error("expected token to be re-minted (hash should change)")
	}
}

func TestDeployRouterRequiresRootPassword(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, h := newDeployRouterMux(t, "https://awg.example", relay)
	rec := postDeployRouter(t, h, "bronya", `{"awgm_api_key":"k"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if relay.callCount() != 0 {
		t.Error("relay must not be called without a root password")
	}
}

func TestDeployRouterRequiresStoredAWGMURL(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, h := newDeployRouterMux(t, "", relay) // no awgm_url
	rec := postDeployRouter(t, h, "bronya", `{"root_password":"rootpw"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestDeployRouterRelayFailureMarksJobFailed replaces the old
// TestDeployRouterRelayFailureReturns502: a relay failure can no longer
// surface as an immediate HTTP error, since the handler returns 202 the
// moment the async job starts — the failure now shows up as StateFailed on
// the polled job instead.
func TestDeployRouterRelayFailureMarksJobFailed(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 1, err: context.DeadlineExceeded}
	_, store, h := newDeployRouterMux(t, "https://awg.example", relay)

	stubLatestVersion(t, "v0.13.8")
	stubVerifiedChecksums(t, map[string]string{"wg-monitor-agent-linux-arm64": "aa11"})

	rec := postDeployRouter(t, h, "bronya", `{"root_password":"rootpw"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (relay failures now surface async via the job), body = %s", rec.Code, rec.Body.String())
	}
	var resp dashboardJobStartResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	job := waitForProvisionTerminal(t, store, resp.JobID, time.Second)
	if job.State != provision.StateFailed {
		t.Fatalf("job state = %q, want failed (hint=%q)", job.State, job.Hint)
	}
}

// tokenHashOf reads the stored token_hash for a nickname.
func tokenHashOf(t *testing.T, d *db.DB, nick string) string {
	t.Helper()
	u, err := d.Users().GetByNickname(nick)
	if err != nil {
		t.Fatal(err)
	}
	return u.TokenHash
}
