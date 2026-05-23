package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildDeferredAgentUpdateConfigKeepsOnlyWizardAuth(t *testing.T) {
	cfg, err := buildDeferredAgentUpdateConfig(deferredAgentUpdateConfigParams{
		Agent: AgentState{
			Nickname: "testkeen",
			Kind:     "static",
		},
		BackendURL:    "https://wg.example.test/",
		WizardToken:   "wizard-token",
		TargetVersion: "v0.13.0-rc27",
		ExpiresAt:     time.Unix(2000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("buildDeferredAgentUpdateConfig: %v", err)
	}
	if cfg.Nickname != "testkeen" || cfg.TargetVersion != "v0.13.0-rc27" {
		t.Fatalf("agent identity/version lost: %+v", cfg)
	}
	if cfg.BackendURL != "https://wg.example.test" {
		t.Fatalf("BackendURL=%q", cfg.BackendURL)
	}
	if cfg.WizardToken != "wizard-token" {
		t.Fatalf("wizard token not carried")
	}
	if cfg.ExpiresAtUnix != 2000 {
		t.Fatalf("ExpiresAtUnix=%d", cfg.ExpiresAtUnix)
	}
}

func TestRenderDeferredAgentUpdateRunnerScriptWaitsForHeartbeatAndVersionFlip(t *testing.T) {
	got := renderDeferredAgentUpdateRunnerScript()
	for _, want := range []string{
		`JOB_DIR + "/*.json"`,
		"/v1/wizard/agents",
		"/deploy",
		"last_deployed_version",
		"last_seen_at",
		"enqueued_at_unix",
		"json.dump",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runner script missing %q:\n%s", want, got)
		}
	}
}

func TestDeferredAgentUpdateRunnerPythonCompiles(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python not on PATH")
	}
	path := filepath.Join(t.TempDir(), "deferred-agent-update.py")
	if err := os.WriteFile(path, []byte(renderDeferredAgentUpdateRunnerScript()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-m", "py_compile", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("py_compile failed: %v\n%s", err, out)
	}
}
