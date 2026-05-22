package main

import (
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
			Nickname: "router4car4",
			Kind:     "mobile",
			ThreadID: 1126,
			AWGMURL:  "https://awg.router4car4.example.test",
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
	if cfg.Nickname != "router4car4" || cfg.Kind != "mobile" || cfg.ThreadID != 1126 {
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

func TestAWGMRelayPythonSupportsDeferredBootstrap(t *testing.T) {
	for _, want := range []string{
		`cfg.get("mode") == "deferred_bootstrap"`,
		`/v1/wizard/enrollments`,
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
