package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/pkg/wire"
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

func TestWizardBackendDeploy_WritesPendingUpdate(t *testing.T) {
	dir := t.TempDir()
	pending := filepath.Join(dir, "backend-update.json")
	h := NewMux(Deps{WizardToken: "secret", BackendUpdatePath: pending})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/backend/deploy",
		strings.NewReader(`{"target_version":"v0.13.0-rc109"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "wgmonitor.example.test")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(pending)
	if err != nil {
		t.Fatal(err)
	}
	var got backendUpdateRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.TargetVersion != "v0.13.0-rc109" {
		t.Fatalf("target_version=%q", got.TargetVersion)
	}
	if got.RepoBase != "https://wgmonitor.example.test/v1/releases/download" {
		t.Fatalf("repo_base=%q", got.RepoBase)
	}
	info, err := os.Stat(pending)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o644 {
		t.Fatalf("pending mode=%#o, want 0644", got)
	}
}

func TestWizardBackendDeployRejectsUnsafeReleaseTag(t *testing.T) {
	dir := t.TempDir()
	pending := filepath.Join(dir, "backend-update.json")
	h := NewMux(Deps{WizardToken: "secret", BackendUpdatePath: pending})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/backend/deploy",
		strings.NewReader(`{"target_version":"v0.13.0/../../bad"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "wgmonitor.example.test")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("unsafe backend update should not write pending file, err=%v", err)
	}
}

func TestWizardEnrollmentRejectsOversizedJSONBody(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := NewMux(Deps{DB: d, WizardToken: "secret"})
	body := `{"nickname":"` + strings.Repeat("a", 70*1024) + `","kind":"static"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/enrollments", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413 for oversized wizard body, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWizardEnrollmentUsesConfiguredPublicBaseURLWhenProxyHostIsPrivate(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := NewMux(Deps{
		DB:            d,
		WizardToken:   "secret",
		PublicBaseURL: "https://wgmonitor.example.test",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/enrollments",
		strings.NewReader(`{"nickname":"testkeen","kind":"static"}`))
	req.Host = "192.168.31.87"
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "192.168.31.87")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got wizardEnrollmentResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.BackendURL != "https://wgmonitor.example.test" {
		t.Fatalf("backend_url=%q", got.BackendURL)
	}
}

func TestWizardBackendURLDefaultsToHTTPWithoutTLS(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/wizard/backend/deploy", nil)

	got := wizardBackendURL(req)

	if got != "http://127.0.0.1:8080" {
		t.Fatalf("wizardBackendURL without forwarded proto/TLS=%q, want http://127.0.0.1:8080", got)
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
		LastDeploy: "2026-05-24T10:20:30Z", DeployMode: "awgm",
		AWGMURL: "https://alyaba.keenetic.pro", AWGMAuth: "api-key",
		ExpectedMAC: "aabbccddeeff",
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
	if got.Agents[0].DeployMode != "awgm" || got.Agents[0].AWGMURL != "https://alyaba.keenetic.pro" ||
		got.Agents[0].AWGMAuth != "api-key" || got.Agents[0].ExpectedMAC != "aabbccddeeff" ||
		got.Agents[0].LastDeploy != "2026-05-24T10:20:30Z" {
		t.Fatalf("portable wizard metadata missing: %+v", got.Agents[0])
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
	body := `{"ssh_host":"10.0.0.1","ssh_port":222,"ssh_user":"root","arch":"mips","last_deployed_version":"v0.10.3","ring":"beta","pending_version":"v0.11.0","pending_since":"2026-05-19T10:00:00Z","last_deploy":"2026-05-24T10:20:30Z","deploy_mode":"awgm","awgm_url":"https://alyaba.keenetic.pro","awgm_auth":"api-key","expected_mac":"aabbccddeeff"}`
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
	if u.DeployMode == nil || *u.DeployMode != "awgm" || u.AWGMURL == nil ||
		*u.AWGMURL != "https://alyaba.keenetic.pro" || u.AWGMAuth == nil ||
		*u.AWGMAuth != "api-key" || u.ExpectedMAC == nil || *u.ExpectedMAC != "aabbccddeeff" ||
		u.LastDeploy == nil || *u.LastDeploy != "2026-05-24T10:20:30Z" {
		t.Fatalf("portable wizard metadata not persisted: u=%+v", u)
	}
}

func TestWizardPut_204UpdatesKindAndThread(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("bronya", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	h := wizardPutAgentHandler(Deps{DB: d})
	body := `{"kind":"mobile","thread_id":406}`
	req := httptest.NewRequest("PUT", "/v1/wizard/agents/bronya", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "bronya")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	u, err := d.Users().GetByNickname("bronya")
	if err != nil {
		t.Fatal(err)
	}
	if u.Kind != db.KindMobile {
		t.Fatalf("kind not persisted: %+v", u)
	}
	if u.TelegramThreadID == nil || *u.TelegramThreadID != 406 {
		t.Fatalf("thread id not persisted: %+v", u)
	}
}

// Version and pending metadata must sync even when an AWGM-only router has no
// usable direct SSH coordinates in wizard.toml.
func TestWizardPut_204AllowsVersionSyncWithMissingSSHFields(t *testing.T) {
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
		SSHHost: "10.0.0.1",
		SSHPort: 222,
		SSHUser: "root",
		Arch:    "arm64",
	}); err != nil {
		t.Fatal(err)
	}
	h := wizardPutAgentHandler(Deps{DB: d})
	body := `{"ssh_host":"","ssh_port":0,"ssh_user":"","arch":"","last_deployed_version":"v0.13.0-rc40","pending_version":"","pending_since":"","last_deploy":"2026-05-24T12:40:00Z"}`
	req := httptest.NewRequest("PUT", "/v1/wizard/agents/alyaba", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "alyaba")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	u, err := d.Users().GetByNickname("alyaba")
	if err != nil {
		t.Fatal(err)
	}
	if u.LastDeployedVersion == nil || *u.LastDeployedVersion != "v0.13.0-rc40" {
		t.Fatalf("version metadata not updated: %+v", u)
	}
	if u.SSHHost == nil || *u.SSHHost != "10.0.0.1" || u.SSHPort == nil || *u.SSHPort != 222 ||
		u.SSHUser == nil || *u.SSHUser != "root" || u.Arch == nil || *u.Arch != "arm64" {
		t.Fatalf("missing SSH fields should preserve existing deploy identity: %+v", u)
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

func TestWizardEnrollmentCreatesRawTokenAndUser(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	h := wizardEnrollmentHandler(Deps{DB: d})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/enrollments", strings.NewReader(`{"nickname":"testkeen","kind":"static","thread_id":406}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got wizardEnrollmentResp
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got.Nickname != "testkeen" || got.RawToken == "" || got.BackendURL == "" {
		t.Fatalf("bad response: %+v", got)
	}
	if _, err := d.Users().GetByToken(got.RawToken); err != nil {
		t.Fatalf("token not registered: %v", err)
	}
}

func TestWizardDeployUsesBackendReleaseMirror(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	userID, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &fakeCmdSink{}
	h := wizardDeployHandler(Deps{DB: d, CommandSink: sink})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0-rc18"}`))
	req.Host = "wg.example.test"
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 || len(sink.enqueuedUsers) != 1 || sink.enqueuedUsers[0] != userID {
		t.Fatalf("unexpected enqueue: users=%v cmds=%+v", sink.enqueuedUsers, sink.enqueued)
	}
	cmd := sink.enqueued[0]
	if cmd.Action != "self_update" || cmd.Args["version"] != "v0.13.0-rc18" {
		t.Fatalf("bad command: %+v", cmd)
	}
	if got := cmd.Args["repo_base"]; got != "https://wg.example.test/v1/releases/download" {
		t.Fatalf("repo_base=%v", got)
	}
}

func TestWizardDeployRejectsTrailingJSON(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &fakeCmdSink{}
	h := wizardDeployHandler(Deps{DB: d, CommandSink: sink})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0-rc18"} {}`))
	req.Host = "wg.example.test"
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("trailing JSON must not enqueue deploy: %+v", sink.enqueued)
	}
}

func TestWizardDeployUsesForwardedPublicHostForLocalCalls(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &fakeCmdSink{}
	h := wizardDeployHandler(Deps{DB: d, CommandSink: sink})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0-rc18"}`))
	req.Host = "127.0.0.1:8080"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "wg.example.test")
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := sink.enqueued[0].Args["repo_base"]; got != "https://wg.example.test/v1/releases/download" {
		t.Fatalf("repo_base=%v", got)
	}
}

func TestWizardDeployPublicHostHeaderOverridesPrivateProxyHost(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &fakeCmdSink{}
	h := wizardDeployHandler(Deps{DB: d, CommandSink: sink})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0-rc80"}`))
	req.Host = "192.168.31.87"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "192.168.31.87")
	req.Header.Set("X-WG-Public-Proto", "https")
	req.Header.Set("X-WG-Public-Host", "wg.example.test")
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := sink.enqueued[0].Args["repo_base"]; got != "https://wg.example.test/v1/releases/download" {
		t.Fatalf("repo_base=%v", got)
	}
}

func TestWizardDeployUsesConfiguredPublicBaseURLWhenProxyHostIsPrivate(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &fakeCmdSink{}
	h := wizardDeployHandler(Deps{
		DB:            d,
		CommandSink:   sink,
		PublicBaseURL: "https://wgmonitor.example.test",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0-rc121"}`))
	req.Host = "192.168.31.87"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "192.168.31.87")
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := sink.enqueued[0].Args["repo_base"]; got != "https://wgmonitor.example.test/v1/releases/download" {
		t.Fatalf("repo_base=%v", got)
	}
}

func TestWizardDeployRejectsLoopbackRepoBase(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &fakeCmdSink{}
	h := wizardDeployHandler(Deps{DB: d, CommandSink: sink})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0-rc18"}`))
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("unexpected enqueue: %+v", sink.enqueued)
	}
	u, err := d.Users().GetByNickname("testkeen")
	if err != nil {
		t.Fatal(err)
	}
	if u.PendingVersion != nil || u.PendingSince != nil {
		t.Fatalf("pending set despite rejected deploy: %+v", u)
	}
}

func TestPublicHostChecksRejectTrailingDotLocalHosts(t *testing.T) {
	for _, host := range []string{"localhost.", "127.0.0.1.", "10.0.0.1.", "192.168.31.87."} {
		if !isNonPublicHost(host) {
			t.Fatalf("isNonPublicHost(%q)=false, want true", host)
		}
	}
	for _, host := range []string{"localhost.", "127.0.0.1."} {
		if !isLoopbackHost(host) {
			t.Fatalf("isLoopbackHost(%q)=false, want true", host)
		}
	}
}

func TestWizardDeployMarksPendingVersionVisibleToWizard(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}

	sink := &fakeCmdSink{}
	deploy := wizardDeployHandler(Deps{DB: d, CommandSink: sink})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0-rc18"}`))
	req.Host = "wg.example.test"
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()
	deploy.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	list := wizardListAgentsHandler(Deps{DB: d})
	req = httptest.NewRequest(http.MethodGet, "/v1/wizard/agents", nil)
	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got wizardAgentList
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(got.Agents) != 1 {
		t.Fatalf("agents=%+v", got.Agents)
	}
	if got.Agents[0].PendingVersion != "v0.13.0-rc18" {
		t.Fatalf("pending_version=%q", got.Agents[0].PendingVersion)
	}
	if got.Agents[0].PendingSince == "" {
		t.Fatalf("pending_since empty: %+v", got.Agents[0])
	}
	if _, err := time.Parse(time.RFC3339, got.Agents[0].PendingSince); err != nil {
		t.Fatalf("pending_since is not RFC3339: %q err=%v", got.Agents[0].PendingSince, err)
	}
}

func TestWizardDeployDoesNotMarkPendingWhenEnqueueFails(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}

	h := wizardDeployHandler(Deps{DB: d, CommandSink: failingEnqueueSink{err: errors.New("queue closed")}})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0-rc18"}`))
	req.Host = "wg.example.test"
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	u, err := d.Users().GetByNickname("testkeen")
	if err != nil {
		t.Fatal(err)
	}
	if u.PendingVersion != nil || u.PendingSince != nil {
		t.Fatalf("pending set despite enqueue failure: %+v", u)
	}
}

func TestWizardCommandDispatchEnqueuesSafeAgentCommand(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	userID, err := d.Users().Insert("puzirek", "tok", "1.2.3.4", "awg0")
	if err != nil {
		t.Fatal(err)
	}

	sink := &fakeCmdSink{}
	h := wizardCommandHandler(Deps{DB: d, CommandSink: sink})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/puzirek/commands", strings.NewReader(`{"action":"tunnel_restart","args":{"ndms_name":"Wireguard1"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "puzirek")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 || sink.enqueuedUsers[0] != userID {
		t.Fatalf("unexpected enqueue: users=%v cmds=%+v", sink.enqueuedUsers, sink.enqueued)
	}
	cmd := sink.enqueued[0]
	if cmd.Action != "tunnel_restart" || cmd.Args["ndms_name"] != "Wireguard1" {
		t.Fatalf("bad command: %+v", cmd)
	}
	var got wizardDeployResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got.CmdID == "" {
		t.Fatalf("empty command id in response: %+v", got)
	}
}

func TestWizardCommandDispatchAllowsConnectivityChecks(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	userID, err := d.Users().Insert("gachimikhail", "tok", "1.2.3.4", "awg0")
	if err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"check_via_tunnel", "check_direct"} {
		t.Run(action, func(t *testing.T) {
			sink := &fakeCmdSink{}
			h := wizardCommandHandler(Deps{DB: d, CommandSink: sink})
			req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/gachimikhail/commands",
				strings.NewReader(`{"action":"`+action+`","args":{"extra":"ignored"}}`))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("nickname", "gachimikhail")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(sink.enqueued) != 1 || sink.enqueuedUsers[0] != userID {
				t.Fatalf("unexpected enqueue: users=%v cmds=%+v", sink.enqueuedUsers, sink.enqueued)
			}
			if sink.enqueued[0].Action != action {
				t.Fatalf("action=%q, want %q", sink.enqueued[0].Action, action)
			}
			if len(sink.enqueued[0].Args) != 0 {
				t.Fatalf("no-arg command leaked args: %+v", sink.enqueued[0].Args)
			}
		})
	}
}

func TestWizardCommandDispatchAllowsRouteAndTunnelCleanup(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	userID, err := d.Users().Insert("del", "tok", "1.2.3.4", "awg0")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		body   string
		action string
		want   map[string]any
	}{
		{
			name:   "route rebind",
			body:   `{"action":"route_rebind","args":{"src_tunnel_id":"awg10","dst_tunnel_id":"awg13","extra":"ignored"}}`,
			action: "route_rebind",
			want:   map[string]any{"src_tunnel_id": "awg10", "dst_tunnel_id": "awg13"},
		},
		{
			name:   "tunnel delete",
			body:   `{"action":"tunnel_delete","args":{"tunnel_id":"awg10","extra":"ignored"}}`,
			action: "tunnel_delete",
			want:   map[string]any{"tunnel_id": "awg10"},
		},
		{
			name:   "route rebind from other",
			body:   `{"action":"route_rebind","args":{"src_tunnel_id":"` + wire.RouteOtherID + `","dst_tunnel_id":"nwg1"}}`,
			action: "route_rebind",
			want:   map[string]any{"src_tunnel_id": wire.RouteOtherID, "dst_tunnel_id": "nwg1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &fakeCmdSink{}
			h := wizardCommandHandler(Deps{DB: d, CommandSink: sink})
			req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/del/commands", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("nickname", "del")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(sink.enqueued) != 1 || sink.enqueuedUsers[0] != userID {
				t.Fatalf("unexpected enqueue: users=%v cmds=%+v", sink.enqueuedUsers, sink.enqueued)
			}
			cmd := sink.enqueued[0]
			if cmd.Action != tc.action {
				t.Fatalf("action=%q, want %q", cmd.Action, tc.action)
			}
			for key, want := range tc.want {
				if got := cmd.Args[key]; got != want {
					t.Fatalf("arg %s=%v, want %v; cmd=%+v", key, got, want, cmd)
				}
			}
			if _, ok := cmd.Args["extra"]; ok {
				t.Fatalf("unsafe extra arg leaked: %+v", cmd.Args)
			}
		})
	}
}

func TestWizardCommandDispatchRejectsUnsafeCommand(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("puzirek", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{
		`{"action":"firmware_install"}`,
		`{"action":"service_restart","args":{"name":"router"}}`,
		`{"action":"tunnel_restart","args":{"ndms_name":"Wireguard1; system reboot"}}`,
		`{"action":"route_rebind","args":{"src_tunnel_id":"awg10; reboot","dst_tunnel_id":"awg13"}}`,
		`{"action":"tunnel_delete","args":{"tunnel_id":"../awg10"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			sink := &fakeCmdSink{}
			h := wizardCommandHandler(Deps{DB: d, CommandSink: sink})
			req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/puzirek/commands", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("nickname", "puzirek")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(sink.enqueued) != 0 {
				t.Fatalf("unsafe command was enqueued: %+v", sink.enqueued)
			}
		})
	}
}

func TestWizardDeployAddsRepoResolveIPWhenDomainResolves(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	oldLookup := lookupHostForRepoResolve
	lookupHostForRepoResolve = func(host string) ([]string, error) {
		if host != "wg.example.test" {
			t.Fatalf("lookup host=%q", host)
		}
		return []string{"127.0.0.1", "10.0.0.1", "83.171.224.125"}, nil
	}
	t.Cleanup(func() { lookupHostForRepoResolve = oldLookup })

	sink := &fakeCmdSink{}
	h := wizardDeployHandler(Deps{DB: d, CommandSink: sink})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0-rc18"}`))
	req.Host = "wg.example.test"
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := sink.enqueued[0].Args["repo_resolve_ip"]; got != "83.171.224.125" {
		t.Fatalf("repo_resolve_ip=%v", got)
	}
}

func TestWizardDeployRejectsUnsafeReleaseTag(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("testkeen", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &fakeCmdSink{}
	h := wizardDeployHandler(Deps{DB: d, CommandSink: sink})
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/agents/testkeen/deploy", strings.NewReader(`{"target_version":"v0.13.0/../../bad"}`))
	req.Host = "wg.example.test"
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "testkeen")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("unsafe deploy should not enqueue command: %+v", sink.enqueued)
	}
}

type failingEnqueueSink struct {
	err error
}

func (s failingEnqueueSink) Dequeue(context.Context, int64, time.Duration) (*wire.Command, bool) {
	return nil, false
}

func (s failingEnqueueSink) RecordResult(int64, wire.CommandResult) error { return nil }

func (s failingEnqueueSink) ConsumeOriginRef(int64, string) (cmd.MessageRef, bool) {
	return cmd.MessageRef{}, false
}

func (s failingEnqueueSink) CommandByID(int64, string) (wire.Command, bool) {
	return wire.Command{}, false
}

func (s failingEnqueueSink) Enqueue(int64, wire.Command) error { return s.err }

func (s failingEnqueueSink) AwaitResult(context.Context, int64, string, time.Duration) (*wire.CommandResult, bool) {
	return nil, false
}

func TestWizardRepoResolveIPSkipsPrivateResults(t *testing.T) {
	oldLookup := lookupHostForRepoResolve
	lookupHostForRepoResolve = func(string) ([]string, error) {
		return []string{"127.0.0.1", "192.168.1.1", "::1"}, nil
	}
	t.Cleanup(func() { lookupHostForRepoResolve = oldLookup })

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Host = "wg.example.test"
	if got := wizardRepoResolveIP(req); got != "" {
		t.Fatalf("private-only resolve must be skipped, got %q", got)
	}
}

func TestReleaseAssetProxyServesAllowlistedAsset(t *testing.T) {
	for _, asset := range []string{"wg-monitor-agent-linux-arm64", "wg-monitor-backend-linux-arm64"} {
		t.Run(asset, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v0.13.0-rc18/"+asset {
					t.Fatalf("unexpected upstream path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte("asset-body"))
			}))
			defer upstream.Close()

			oldBase := releaseDownloadBase
			releaseDownloadBase = upstream.URL
			t.Cleanup(func() { releaseDownloadBase = oldBase })

			req := httptest.NewRequest(http.MethodGet, "/v1/releases/download/v0.13.0-rc18/"+asset, nil)
			req.SetPathValue("version", "v0.13.0-rc18")
			req.SetPathValue("asset", asset)
			rec := httptest.NewRecorder()
			releaseAssetProxyHandler(Deps{}).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != "asset-body" {
				t.Fatalf("body=%q", got)
			}
		})
	}
}

func TestReleaseAssetProxyRejectsUnknownAsset(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/releases/download/v0.13.0-rc18/../../secret", nil)
	req.SetPathValue("version", "v0.13.0-rc18")
	req.SetPathValue("asset", "../../secret")
	rec := httptest.NewRecorder()
	releaseAssetProxyHandler(Deps{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		body, _ := io.ReadAll(rec.Body)
		t.Fatalf("status=%d body=%s", rec.Code, string(body))
	}
}

func TestReleaseAssetProxyRejectsOversizedUpstreamContentLength(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0.13.0-rc18/wg-monitor-agent-linux-arm64" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(maxSelfUpdateProxyBytes+1, 10))
		_, _ = w.Write([]byte("x"))
	}))
	defer upstream.Close()

	oldBase := releaseDownloadBase
	releaseDownloadBase = upstream.URL
	t.Cleanup(func() { releaseDownloadBase = oldBase })

	req := httptest.NewRequest(http.MethodGet, "/v1/releases/download/v0.13.0-rc18/wg-monitor-agent-linux-arm64", nil)
	req.SetPathValue("version", "v0.13.0-rc18")
	req.SetPathValue("asset", "wg-monitor-agent-linux-arm64")
	rec := httptest.NewRecorder()
	releaseAssetProxyHandler(Deps{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "too large") {
		t.Fatalf("expected too large error, got %s", rec.Body.String())
	}
}

func TestReleaseAssetProxyRejectsOversizedChunkedUpstreamBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0.13.0-rc18/wg-monitor-agent-linux-arm64" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, io.LimitReader(zeroReader{}, maxSelfUpdateProxyBytes+1))
	}))
	defer upstream.Close()

	oldBase := releaseDownloadBase
	releaseDownloadBase = upstream.URL
	t.Cleanup(func() { releaseDownloadBase = oldBase })

	req := httptest.NewRequest(http.MethodGet, "/v1/releases/download/v0.13.0-rc18/wg-monitor-agent-linux-arm64", nil)
	req.SetPathValue("version", "v0.13.0-rc18")
	req.SetPathValue("asset", "wg-monitor-agent-linux-arm64")
	rec := &countingResponseWriter{header: make(http.Header)}
	releaseAssetProxyHandler(Deps{}).ServeHTTP(rec, req)

	if rec.code != http.StatusBadGateway {
		t.Fatalf("status=%d bytes=%d body=%q", rec.code, rec.bytes, rec.body.String())
	}
	if rec.bytes > 4096 {
		t.Fatalf("oversized proxy should fail before streaming asset bytes, wrote %d", rec.bytes)
	}
	if !strings.Contains(rec.body.String(), "too large") {
		t.Fatalf("expected too large error, got %q", rec.body.String())
	}
}

func TestReleaseAssetProxyRejectsTruncatedUpstreamBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0.13.0-rc18/wg-monitor-agent-linux-arm64" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Length", "10")
		_, _ = w.Write([]byte("short"))
	}))
	defer upstream.Close()

	oldBase := releaseDownloadBase
	releaseDownloadBase = upstream.URL
	t.Cleanup(func() { releaseDownloadBase = oldBase })

	req := httptest.NewRequest(http.MethodGet, "/v1/releases/download/v0.13.0-rc18/wg-monitor-agent-linux-arm64", nil)
	req.SetPathValue("version", "v0.13.0-rc18")
	req.SetPathValue("asset", "wg-monitor-agent-linux-arm64")
	rec := httptest.NewRecorder()
	releaseAssetProxyHandler(Deps{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "release fetch") {
		t.Fatalf("expected release fetch error, got %q", rec.Body.String())
	}
}

func TestReleaseAssetProxyAllowsChecksumsSignature(t *testing.T) {
	if !isAllowedReleaseAsset("checksums.txt.sig") {
		t.Fatal("checksums.txt.sig must be proxy-allowed for signed releases")
	}
}

type countingResponseWriter struct {
	header http.Header
	code   int
	bytes  int64
	body   strings.Builder
}

func (w *countingResponseWriter) Header() http.Header { return w.header }

func (w *countingResponseWriter) WriteHeader(code int) { w.code = code }

func (w *countingResponseWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	w.bytes += int64(len(p))
	if w.body.Len() < 4096 {
		remain := 4096 - w.body.Len()
		if len(p) > remain {
			p = p[:remain]
		}
		_, _ = w.body.Write(p)
	}
	return len(p), nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
