package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// Ядра зовутся и дашбордом, и мини-аппом. Здесь -- без единого
// http.ResponseWriter: если ядро снова начнёт писать ответ само, эти тесты
// перестанут компилироваться.

func TestStartProvisionInstall_StartsJobWithoutHTTP(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0, lines: []string{"__WG_STEP__ config_written"}}
	d, database := newReinstallCoreDeps(t, relay)
	stubVerifiedChecksums(t, map[string]string{"wg-monitor-agent-linux-arm64": "cafebabe"})

	jobID, version, serr := startProvisionInstall(context.Background(), d, provisionInstallCoreParams{
		Nickname: "fresh-one", AgentKind: db.KindStatic, AWGMURL: "https://panel.example.com",
		AWGMAuth: "web", RootPassword: "rootpw", AWGMLogin: "admin", AWGMPassword: "panelpw", Version: "v0.36.0",
	})
	if serr != nil {
		t.Fatalf("serr = %+v", serr)
	}
	if jobID == "" || version != "v0.36.0" {
		t.Fatalf("jobID=%q version=%q", jobID, version)
	}
	job := waitForProvisionTerminal(t, d.Provision.Store, jobID, time.Second)
	if job.State != provision.StateSuccess || job.Kind != provision.KindProvision {
		t.Fatalf("job = %+v", job)
	}
	// Токен коммитится на config_written -- роутер появился в базе.
	u, err := database.Users().GetByNickname("fresh-one")
	if err != nil || stringValue(u.AWGMURL) != "https://panel.example.com" || stringValue(u.AWGMAuth) != "web" {
		t.Fatalf("роутер после установки: %+v err=%v", u, err)
	}
	var captured awgmInstallJob
	if err := json.Unmarshal(relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.TerminalPassword != "rootpw" || captured.Mode != "bootstrap_install" || captured.BackendURL != "https://wgmon.example.com" {
		t.Fatalf("job: %+v", captured)
	}
}

func TestStartProvisionInstall_RefusalsWithoutHTTP(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	d, database := newReinstallCoreDeps(t, relay)
	stubVerifiedChecksums(t, map[string]string{"wg-monitor-agent-linux-arm64": "cafebabe"})
	base := provisionInstallCoreParams{Nickname: "fresh-two", AWGMURL: "https://panel.example.com", RootPassword: "x", Version: "v0.36.0"}

	noStore := d
	noStore.Provision.Store = nil
	if _, _, serr := startProvisionInstall(context.Background(), noStore, base); serr == nil || serr.Code != "provision_not_configured" || serr.Status != http.StatusServiceUnavailable {
		t.Fatalf("без движка: %+v", serr)
	}

	noURL := d
	noURL.PublicBaseURL = ""
	if _, _, serr := startProvisionInstall(context.Background(), noURL, base); serr == nil || serr.Code != "no_public_base_url" {
		t.Fatalf("без публичного адреса: %+v", serr)
	}

	u := seedReinstallRouter(t, database, "old-one", "https://panel.example.com", "v0.36.0")
	down := base
	down.Nickname, down.Existing, down.Version = "old-one", u, "v0.35.0"
	if _, _, serr := startProvisionInstall(context.Background(), d, down); serr == nil || serr.Code != "downgrade_rejected" || serr.Status != http.StatusBadRequest {
		t.Fatalf("откат: %+v", serr)
	}

	if !d.Provision.Store.TryLock("fresh-two") {
		t.Fatal("замок занят до теста")
	}
	if _, _, serr := startProvisionInstall(context.Background(), d, base); serr == nil || serr.Code != "provision_already_running" || serr.Status != http.StatusConflict {
		t.Fatalf("занятый замок: %+v", serr)
	}
	d.Provision.Store.Unlock("fresh-two")

	bad := base
	bad.Nickname = "Bad Name"
	if _, _, serr := startProvisionInstall(context.Background(), d, bad); serr == nil || serr.Code != "invalid_nickname" {
		t.Fatalf("имя: %+v", serr)
	}
	if relay.callCount() != 0 {
		t.Fatalf("отказы дошли до роутера: %d вызовов", relay.callCount())
	}
}

func TestRegisterAgent_MintsAndKeepsTopicWhenAsked(t *testing.T) {
	d, database := newReinstallCoreDeps(t, &fakeProvisionRelay{})
	enr, serr := registerAgent(d, registerAgentInput{Nickname: "invited", AgentKind: db.KindMobile, AWGMURL: "https://panel.example.com", AWGMAuth: "api-key"})
	if serr != nil {
		t.Fatalf("serr = %+v", serr)
	}
	if enr.Nickname != "invited" || len(enr.RawToken) < 32 {
		t.Fatalf("enrollment = %+v", enr)
	}
	u, err := database.Users().GetByNickname("invited")
	if err != nil || u.Kind != db.KindMobile || stringValue(u.AWGMAuth) != "api-key" {
		t.Fatalf("роутер: %+v err=%v", u, err)
	}
	if u.TelegramChatID != nil && *u.TelegramChatID != 0 {
		t.Fatalf("UpdateTopic=false тронул чат: %v", *u.TelegramChatID)
	}
	if _, serr := registerAgent(d, registerAgentInput{Nickname: "x"}); serr == nil || serr.Code != "invalid_nickname" {
		t.Fatalf("короткое имя: %+v", serr)
	}
	if _, serr := registerAgent(d, registerAgentInput{Nickname: "good-name", AgentKind: "boat"}); serr == nil || serr.Code != "invalid_kind" {
		t.Fatalf("тип: %+v", serr)
	}
}

func TestStartRepairRepoint_WithoutHTTP(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	d, database := newReinstallCoreDeps(t, relay)
	u := seedReinstallRouter(t, database, "bronya", "https://awg.example.com", "v0.36.0")

	if _, _, serr := startRepairRepoint(d, "bronya", u, repointInput{RootPassword: "x", NewBackendURL: "http://plain.example.com"}); serr == nil || serr.Code != "invalid_backend_url" {
		t.Fatalf("http-адрес: %+v", serr)
	}
	bare := seedReinstallRouter(t, database, "no-panel", "", "v0.36.0")
	if _, _, serr := startRepairRepoint(d, "no-panel", bare, repointInput{RootPassword: "x"}); serr == nil || serr.Code != "no_awgm_url" {
		t.Fatalf("без панели: %+v", serr)
	}

	jobID, newURL, serr := startRepairRepoint(d, "bronya", u, repointInput{RootPassword: "rootpw", NewBackendURL: "https://other.example.com"})
	if serr != nil || jobID == "" || newURL != "https://other.example.com" {
		t.Fatalf("jobID=%q url=%q serr=%+v", jobID, newURL, serr)
	}
	job := waitForProvisionTerminal(t, d.Provision.Store, jobID, time.Second)
	if job.Kind != provision.KindRepairRepoint || len(job.Steps) != 4 {
		t.Fatalf("job = %+v", job)
	}
	var captured awgmReviveJob
	if err := json.Unmarshal(relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.TerminalPassword != "rootpw" || !strings.Contains(captured.BootstrapScript, "https://other.example.com") {
		t.Fatalf("job: %+v", captured)
	}
}

func TestRepointInputHidesSecretsWhenPrinted(t *testing.T) {
	in := repointInput{RootPassword: miniappReviveRoot, AWGMPassword: miniappRevivePanel, AWGMAPIKey: miniappReviveKey}
	for _, s := range []string{in.String(), in.GoString(), in.LogValue().String()} {
		assertNoReviveSecrets(t, "repointInput", s)
	}
}

func TestQueueBackendUpdate_WithoutHTTP(t *testing.T) {
	old := serverVersion
	SetVersion("v0.36.0")
	t.Cleanup(func() { SetVersion(old) })
	path := filepath.Join(t.TempDir(), "backend-update.json")
	d := Deps{BackendUpdatePath: path}
	stubRepoResolve(t, map[string]string{"backend.example.com": "203.0.113.5"})
	repo := func() (string, bool) { return "https://backend.example.com", true }
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	if _, serr := queueBackendUpdate(Deps{}, backendUpdateInput{TargetVersion: "v0.37.0", RepoBase: repo}); serr == nil || serr.Code != "backend_update_not_configured" || serr.Status != http.StatusServiceUnavailable {
		t.Fatalf("без пути: %+v", serr)
	}
	if _, serr := queueBackendUpdate(d, backendUpdateInput{TargetVersion: "latest", RepoBase: repo}); serr == nil || serr.Code != errCodeBadJSON {
		t.Fatalf("тег: %+v", serr)
	}
	if _, serr := queueBackendUpdate(d, backendUpdateInput{TargetVersion: "v0.35.0", RepoBase: repo}); serr == nil || serr.Code != "downgrade_rejected" {
		t.Fatalf("откат: %+v", serr)
	}
	noRepo := func() (string, bool) { return "", false }
	if _, serr := queueBackendUpdate(d, backendUpdateInput{TargetVersion: "v0.37.0", RepoBase: noRepo}); serr == nil || serr.Code != errCodeBadJSON {
		t.Fatalf("адрес: %+v", serr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("отказы записали файл: %v", err)
	}

	got, serr := queueBackendUpdate(d, backendUpdateInput{TargetVersion: "v0.37.0", RepoBase: repo, Now: now})
	if serr != nil {
		t.Fatalf("serr = %+v", serr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk backendUpdateRequest
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	want := backendUpdateRequest{
		TargetVersion: "v0.37.0", RepoBase: "https://backend.example.com/v1/releases/download",
		RepoResolveIP: "203.0.113.5", TrustedBackendURL: "https://backend.example.com", RequestedAt: "2026-09-17T10:00:00Z",
	}
	if onDisk != want || got != want {
		t.Fatalf("на диске %+v, вернулось %+v, ждали %+v", onDisk, got, want)
	}
}

func TestValidateAgentEdit(t *testing.T) {
	ok := dashboardEditAgentReq{Kind: " mobile ", Arch: "aarch64", AWGMURL: "https://panel.example.com"}
	if serr := validateAgentEdit(&ok); serr != nil || ok.Kind != "mobile" || ok.Arch != "arm64" {
		t.Fatalf("serr=%+v req=%+v", serr, ok)
	}
	for code, req := range map[string]dashboardEditAgentReq{
		"invalid_kind":     {Kind: "boat"},
		"invalid_arch":     {Arch: "x86"},
		"invalid_awgm_url": {AWGMURL: "ftp://panel.example.com"},
	} {
		if serr := validateAgentEdit(&req); serr == nil || serr.Code != code || serr.Status != http.StatusBadRequest {
			t.Errorf("%s: %+v", code, serr)
		}
	}
}
