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

func TestRenderDeferredAWGMRunnerScriptIgnoresArchivedArtifacts(t *testing.T) {
	got := renderDeferredAWGMRunnerScript()
	if !strings.Contains(got, "for cfg in /var/lib/wg-monitor/deferred-awgm/*.json; do") {
		t.Fatalf("runner must only scan active *.json jobs:\n%s", got)
	}
	for _, unwanted := range []string{
		"*.done",
		"*.bak",
		"*.token",
		"*.json.*",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("runner should not retry archived artifact glob %q:\n%s", unwanted, got)
		}
	}
}

func TestAWGMTransientUnavailableMatchesRouterWakeFailures(t *testing.T) {
	for _, msg := range []string{
		"awgm vps relay failed rc=1: websocket closed",
		`awgm vps system info failed rc=1: <urlopen error [Errno -2] Name or service not known>`,
		`awgm POST /api/auth/login: HTTP 503: <!DOCTYPE html><html><body><noscript>503</noscript></body></html>`,
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
		if ag.Nickname != "puzirek" || ag.LastDeployedVersion != "v0.13.0-rc14" {
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
		Host:   "83.171.224.125",
		Domain: "wg.example.test",
	}}
	ag := &AgentState{
		Nickname:            "puzirek",
		Kind:                "static",
		AWGMURL:             "https://awg.puzirek.example.test",
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

func TestAWGMRelayPythonDeferredBootstrapCleanupPaths(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python not on PATH")
	}
	dir := t.TempDir()
	relayPath := filepath.Join(dir, "awgm-relay.py")
	if err := os.WriteFile(relayPath, []byte(awgmVPSRelayPython), 0o600); err != nil {
		t.Fatal(err)
	}
	harnessPath := filepath.Join(dir, "harness.py")
	harness := fmt.Sprintf(`
import importlib.util
import json
import os
import sys
import time

relay_path = %q
work_dir = %q
spec = importlib.util.spec_from_file_location("relay_under_test", relay_path)
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)

def write_job(name, data):
    path = os.path.join(work_dir, name)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f)
    return path

expired = write_job("expired.json", {
    "nickname": "expired",
    "target_version": "v0.13.0-rc57",
    "expires_at_unix": int(time.time()) - 10,
})
relay.run_deferred_bootstrap(json.load(open(expired, encoding="utf-8")), expired)
assert not os.path.exists(expired), "expired job remained active"

login_calls = []
def fail_login(*args, **kwargs):
    login_calls.append(True)
    raise AssertionError("satisfied job reached AWG Manager login")
relay.login_if_needed = fail_login
relay.opener = lambda: (object(), object())
relay.local_wizard_request = lambda cfg, method, path, body=None: {
    "agents": [{
        "nickname": "caredns-oldcar",
        "last_deployed_version": "v0.13.0-rc57",
        "pending_version": "",
        "pending_since": "",
    }]
}
satisfied = write_job("satisfied.json", {
    "nickname": "caredns-oldcar",
    "target_version": "v0.13.0-rc57",
    "expires_at_unix": int(time.time()) + 3600,
    "wizard_token": "secret-token",
})
relay.run_deferred_bootstrap(json.load(open(satisfied, encoding="utf-8")), satisfied)
assert not os.path.exists(satisfied), "satisfied job remained active"
assert login_calls == [], "satisfied job tried AWG Manager"

relay.login_if_needed = lambda op, cfg: None
relay.request = lambda op, cfg, method, path: {"data": {"goArch": "mips", "routerIP": "10.0.0.1"}}
relay.ensure_terminal = lambda op, cfg: None
class Sock:
    def close(self): pass
relay.ws_connect = lambda cfg, jar: Sock()
relay.ws_send = lambda *args, **kwargs: None
relay.send_resize = lambda *args, **kwargs: None
relay.login_terminal = lambda sock, cfg: ""
relay.run_bootstrap = lambda sock, cfg: None
relay.local_wizard_request = lambda cfg, method, path, body=None: (
    {"raw_token": "raw-token", "backend_url": "https://wg.example.test"}
    if method == "POST" else {}
)
success = write_job("success.json", {
    "nickname": "fresh",
    "kind": "static",
    "target_version": "v0.13.0-rc57",
    "release_base": "https://wg.example.test/v1/releases/download",
    "expires_at_unix": int(time.time()) + 3600,
    "wizard_token": "secret-token",
    "init_script": "#!/bin/sh\nexit 0",
})
relay.run_deferred_bootstrap(json.load(open(success, encoding="utf-8")), success)
assert not os.path.exists(success), "successful job remained active"
assert os.path.exists(success + ".done"), "successful job did not write done artifact"
assert not (success + ".done").endswith(".json"), "done artifact must not be active *.json"
`, relayPath, dir)
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, harnessPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deferred bootstrap cleanup harness failed: %v\n%s", err, out)
	}
}

func TestAWGMRelayPythonDefersExistingAgentTokenCommitUntilBootstrapSucceeds(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python not on PATH")
	}
	dir := t.TempDir()
	relayPath := filepath.Join(dir, "awgm-relay.py")
	if err := os.WriteFile(relayPath, []byte(awgmVPSRelayPython), 0o600); err != nil {
		t.Fatal(err)
	}
	harnessPath := filepath.Join(dir, "harness.py")
	harness := fmt.Sprintf(`
import importlib.util
import json
import os
import sys
import time

relay_path = %q
work_dir = %q
spec = importlib.util.spec_from_file_location("relay_under_test", relay_path)
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)

job_path = os.path.join(work_dir, "existing.json")
with open(job_path, "w", encoding="utf-8") as f:
    json.dump({
        "nickname": "bronya",
        "kind": "mobile",
        "base_url": "https://awg.bronya.example.test",
        "target_version": "v0.13.0-rc60",
        "backend_url": "https://wg.example.test",
        "release_base": "https://wg.example.test/v1/releases/download",
        "expires_at_unix": int(time.time()) + 3600,
        "wizard_token": "secret-token",
        "terminal_user": "root",
        "terminal_password": "keenetic",
        "init_script": "#!/bin/sh\nexit 0",
    }, f)

events = []
put_body = {}
relay.opener = lambda: (object(), object())
relay.login_if_needed = lambda op, cfg: None
relay.request = lambda op, cfg, method, path: {"data": {"goArch": "mips", "routerIP": "10.0.0.1"}}
relay.ensure_terminal = lambda op, cfg: None
class Sock:
    def close(self): pass
relay.ws_connect = lambda cfg, jar: Sock()
relay.ws_send = lambda *args, **kwargs: None
relay.send_resize = lambda *args, **kwargs: None
relay.login_terminal = lambda sock, cfg: ""

def fake_wizard(cfg, method, path, body=None):
    events.append("wizard:%%s:%%s" %% (method, path))
    if method == "GET" and path == "/v1/wizard/agents":
        return {"agents": [{
            "nickname": "bronya",
            "kind": "mobile",
            "last_deployed_version": "",
            "pending_version": "v0.13.0-rc60",
        }]}
    if method == "POST" and path == "/v1/wizard/enrollments":
        return {"raw_token": "early-token", "backend_url": "https://wg.example.test"}
    if method == "PUT" and path == "/v1/wizard/agents/bronya":
        put_body.update(body or {})
    return {}
relay.local_wizard_request = fake_wizard

def fake_commit(cfg, nick, raw_token):
    events.append("commit:%%s" %% nick)
relay.commit_existing_agent_token_hash = fake_commit

def fake_bootstrap(sock, cfg):
    script = cfg.get("bootstrap_script") or ""
    if "DOWNLOAD_URL=" in script:
        if any(e.startswith("wizard:POST:/v1/wizard/enrollments") for e in events):
            raise AssertionError("existing deferred bootstrap rotated token before install succeeded")
        if '"$INIT" restart || "$INIT" start' in script:
            raise AssertionError("existing deferred bootstrap started service before token hash commit")
        events.append("bootstrap")
        return
    if '"$INIT" restart || "$INIT" start' not in script:
        raise AssertionError("post-commit start script did not restart service")
    if "commit:bronya" not in events:
        raise AssertionError("service started before token hash commit")
    events.append("start")
relay.run_bootstrap = fake_bootstrap

relay.run_deferred_bootstrap(json.load(open(job_path, encoding="utf-8")), job_path)
assert events == [
    "wizard:GET:/v1/wizard/agents",
    "bootstrap",
    "commit:bronya",
    "start",
    "wizard:PUT:/v1/wizard/agents/bronya",
], events
assert not os.path.exists(job_path), "successful existing-agent job remained active"
assert os.path.exists(job_path + ".done"), "successful existing-agent job did not write done artifact"
assert put_body["deploy_mode"] == "awgm", put_body
assert put_body["awgm_url"] == "https://awg.bronya.example.test", put_body
assert put_body["awgm_auth"] == "router-admin", put_body
assert put_body["last_deploy"], put_body
`, relayPath, dir)
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, harnessPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deferred existing-agent harness failed: %v\n%s", err, out)
	}
}

func TestAWGMRelayPythonSupportsDeferredBootstrap(t *testing.T) {
	for _, want := range []string{
		`cfg.get("mode") == "deferred_bootstrap"`,
		`/v1/wizard/enrollments`,
		`normalize_arch`,
		`("aarch64", "arm64")`,
		`127.0.0.1:8080`,
		`os.remove(cfg_path)`,
		`subprocess.run`,
		`SELECT changes();`,
	} {
		if !strings.Contains(awgmVPSRelayPython, want) {
			t.Fatalf("relay python missing %q", want)
		}
	}
	if strings.Contains(awgmVPSRelayPython, "import sqlite3") {
		t.Fatal("relay should use sqlite3 CLI, not Python sqlite3 module")
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
