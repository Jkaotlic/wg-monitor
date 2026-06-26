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

func newDeployRouterMux(t *testing.T, awgmURL string) (*db.DB, http.Handler) {
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
	h := NewMux(Deps{DB: d, DashboardToken: "secret", PublicBaseURL: "https://wgmon.example"})
	return d, h
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

func TestDeployRouterBuildsInstallJobWithTransientCreds(t *testing.T) {
	d, h := newDeployRouterMux(t, "https://awg.example")

	origVer := lookupDashboardLatestVersion
	lookupDashboardLatestVersion = func(_ context.Context) (string, error) { return "v0.13.8", nil }
	t.Cleanup(func() { lookupDashboardLatestVersion = origVer })

	origSums := releaseChecksumsFetcher
	releaseChecksumsFetcher = func(_ context.Context, version string) (map[string]string, error) {
		return map[string]string{"wg-monitor-agent-linux-arm64": "aa11"}, nil
	}
	t.Cleanup(func() { releaseChecksumsFetcher = origSums })

	beforeHash := tokenHashOf(t, d, "bronya")

	var captured awgmInstallJob
	origRun := runAWGMInstallJob
	runAWGMInstallJob = func(_ context.Context, _ string, job awgmInstallJob) (string, error) {
		captured = job
		return "install bootstrap complete for bronya at v0.13.8 (arm64)\n", nil
	}
	t.Cleanup(func() { runAWGMInstallJob = origRun })

	rec := postDeployRouter(t, h, "bronya",
		`{"root_password":"rootpw","awgm_api_key":"key-123"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
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
	if h := tokenHashOf(t, d, "bronya"); h == beforeHash {
		t.Error("expected token to be re-minted (hash should change)")
	}
}

func TestDeployRouterRequiresRootPassword(t *testing.T) {
	_, h := newDeployRouterMux(t, "https://awg.example")
	rec := postDeployRouter(t, h, "bronya", `{"awgm_api_key":"k"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDeployRouterRequiresStoredAWGMURL(t *testing.T) {
	_, h := newDeployRouterMux(t, "") // no awgm_url
	rec := postDeployRouter(t, h, "bronya", `{"root_password":"rootpw"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDeployRouterRelayFailureReturns502(t *testing.T) {
	_, h := newDeployRouterMux(t, "https://awg.example")
	lookupDashboardLatestVersion = func(_ context.Context) (string, error) { return "v0.13.8", nil }
	releaseChecksumsFetcher = func(_ context.Context, _ string) (map[string]string, error) {
		return map[string]string{"wg-monitor-agent-linux-arm64": "aa11"}, nil
	}
	origRun := runAWGMInstallJob
	runAWGMInstallJob = func(_ context.Context, _ string, _ awgmInstallJob) (string, error) {
		return "checksum mismatch\n", context.DeadlineExceeded
	}
	t.Cleanup(func() { runAWGMInstallJob = origRun })

	rec := postDeployRouter(t, h, "bronya", `{"root_password":"rootpw"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.OK || !strings.Contains(resp.Output, "checksum mismatch") {
		t.Fatalf("bad 502 body: %s", rec.Body.String())
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
