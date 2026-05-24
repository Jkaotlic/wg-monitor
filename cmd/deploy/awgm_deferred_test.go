package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildDeferredAWGMConfigDefersEnrollmentUntilWake(t *testing.T) {
	rel := &Release{TagName: "v0.13.0-rc23"}
	cfg, err := buildDeferredAWGMConfig(deferredAWGMConfigParams{
		Agent: AgentState{
			Nickname: "client-e",
			Kind:     "mobile",
			ThreadID: 1126,
			AWGMURL:  "https://awg.client-e.example.test",
		},
		APIKey:           "api-key",
		TerminalUser:     "root",
		TerminalPassword: "keenetic",
		BackendURL:       "https://wg.example.test",
		WizardToken:      "wizard-token",
		Release:          rel,
		ExpiresAt:        time.Unix(2000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("buildDeferredAWGMConfig: %v", err)
	}
	if cfg.Mode != "deferred_bootstrap" {
		t.Fatalf("Mode=%q, want deferred_bootstrap", cfg.Mode)
	}
	if cfg.RawToken != "" {
		t.Fatal("deferred job must not store agent raw token before the router wakes")
	}
	if cfg.Nickname != "client-e" || cfg.Kind != "mobile" || cfg.ThreadID != 1126 {
		t.Fatalf("router identity lost: %+v", cfg)
	}
	if cfg.TargetVersion != "v0.13.0-rc23" {
		t.Fatalf("TargetVersion=%q", cfg.TargetVersion)
	}
	if cfg.ReleaseBase != "https://wg.example.test/v1/releases/download" {
		t.Fatalf("ReleaseBase=%q", cfg.ReleaseBase)
	}
}

func TestRenderDeferredAWGMRunnerScriptScansQueue(t *testing.T) {
	got := renderDeferredAWGMRunnerScript()
	for _, want := range []string{
		"/var/lib/wg-monitor/deferred-awgm/*.json",
		"/usr/local/lib/wg-monitor/awgm-relay.py",
		"timeout 20m python3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runner script missing %q:\n%s", want, got)
		}
	}
}

func TestAWGMTransientUnavailableMatchesRouterWakeFailures(t *testing.T) {
	for _, msg := range []string{
		"awgm vps relay failed rc=1: websocket closed",
		`awgm vps system info failed rc=1: <urlopen error [Errno -2] Name or service not known>`,
	} {
		if !isAWGMTransientUnavailable(fmt.Errorf("%s", msg)) {
			t.Fatalf("expected transient AWGM failure for %q", msg)
		}
	}
}

func TestScheduleDeferredAWGMDeploySupportsExistingAgentReenroll(t *testing.T) {
	oldInstall := installDeferredAWGMDeployViaVPSFunc
	defer func() { installDeferredAWGMDeployViaVPSFunc = oldInstall }()

	called := false
	installDeferredAWGMDeployViaVPSFunc = func(state *State, secrets *SecretStore, ag *AgentState, apiKey, login, pass, terminalUser, terminalPass string, rel *Release, wizardToken string) error {
		called = true
		if ag.Nickname != "client-b" || ag.LastDeployedVersion != "v0.13.0-rc14" {
			t.Fatalf("existing agent identity lost: %+v", ag)
		}
		if rel == nil || rel.TagName != "v0.13.0-rc37" {
			t.Fatalf("release=%+v, want rc37", rel)
		}
		if wizardToken != "wizard-token" {
			t.Fatalf("wizardToken=%q", wizardToken)
		}
		return nil
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.13.0-rc37"}]`))
	}))
	defer srv.Close()

	oldAPI := GitHubAPIBase
	GitHubAPIBase = srv.URL
	defer func() { GitHubAPIBase = oldAPI }()

	state := &State{Backend: BackendState{
		Host:   "198.51.100.10",
		Domain: "wg.example.test",
	}}
	ag := &AgentState{
		Nickname:            "client-b",
		Kind:                "static",
		AWGMURL:             "https://awg.client-b.example.test",
		LastDeployedVersion: "v0.13.0-rc14",
	}
	t.Setenv("WG_YES_TO_ALL", "1")

	scheduled, err := scheduleDeferredAWGMDeployIfWanted(
		state,
		&SecretStore{},
		&Downloader{HTTP: srv.Client(), CacheDir: t.TempDir()},
		ag,
		"",
		"admin",
		"pass",
		"root",
		"root-pass",
		"wizard-token",
		"router-admin",
		fmt.Errorf("awgm vps relay failed rc=1: websocket closed"),
	)
	if err != nil {
		t.Fatalf("scheduleDeferredAWGMDeployIfWanted: %v", err)
	}
	if !scheduled || !called {
		t.Fatalf("deferred AWGM deploy not scheduled: scheduled=%v called=%v", scheduled, called)
	}
	if ag.PendingVersion != "v0.13.0-rc37" || ag.DeployMode != "awgm" || ag.AWGMAuth != "router-admin" {
		t.Fatalf("agent pending state not recorded: %+v", ag)
	}
}

func TestAWGMRelayPythonSupportsDeferredBootstrap(t *testing.T) {
	for _, want := range []string{
		`cfg.get("mode") == "deferred_bootstrap"`,
		`/v1/wizard/enrollments`,
		`normalize_arch`,
		`("aarch64", "arm64")`,
		`127.0.0.1:8080`,
		`os.remove(sys.argv[1])`,
	} {
		if !strings.Contains(awgmVPSRelayPython, want) {
			t.Fatalf("relay python missing %q", want)
		}
	}
}

func TestAWGMRelayPythonCompiles(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python not on PATH")
	}
	path := filepath.Join(t.TempDir(), "awgm-relay.py")
	if err := os.WriteFile(path, []byte(awgmVPSRelayPython), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-m", "py_compile", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("py_compile failed: %v\n%s", err, out)
	}
}
