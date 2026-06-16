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
		ExpectedSHA:      strings.Repeat("d", 64),
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
	if cfg.ExpectedSHA != strings.Repeat("d", 64) {
		t.Fatalf("ExpectedSHA=%q", cfg.ExpectedSHA)
	}
}

func TestBuildDeferredAWGMConfigCarriesRecoveryHint(t *testing.T) {
	cfg, err := buildDeferredAWGMConfig(deferredAWGMConfigParams{
		Agent: AgentState{
			Nickname: "client-g",
			Kind:     "mobile",
			AWGMURL:  "https://awg.client-g.example.test",
		},
		BackendURL:   "https://wg.example.test",
		WizardToken:  "wizard-token",
		Release:      &Release{TagName: "v0.13.0-rc60"},
		ExpectedSHA:  strings.Repeat("f", 64),
		ExpiresAt:    time.Unix(2000, 0).UTC(),
		RecoveryHint: "PowerShell: ssh ... | Add-Content ...\nThen: wg-monitor-deploy doctor",
	})
	if err != nil {
		t.Fatalf("buildDeferredAWGMConfig: %v", err)
	}
	if !strings.Contains(cfg.RecoveryHint, "Add-Content") || !strings.Contains(cfg.RecoveryHint, "doctor") {
		t.Fatalf("recovery hint not carried into deferred job config: %+v", cfg)
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

func TestDeferredAWGMRecoveryHintGivesCopyPastePath(t *testing.T) {
	secPath := filepath.Join(t.TempDir(), "secrets.env")
	t.Setenv("WG_SECRETS_FILE", secPath)

	got := deferredAWGMRecoveryHint(
		&State{Backend: BackendState{Host: "198.51.100.10", Port: 2202, User: "deployer"}},
		&AgentState{Nickname: "client-g"},
		"/var/lib/wg-monitor/deferred-awgm/client-g.json.token",
		"WG_AGENT_TOKEN_BRONYA",
	)
	for _, want := range []string{
		"WG_AGENT_TOKEN_BRONYA",
		"ssh -p 2202 deployer@198.51.100.10 \"cat /var/lib/wg-monitor/deferred-awgm/client-g.json.token\" | Add-Content -Encoding ascii \"" + secPath + "\"",
		"wg-monitor-deploy sync-vps",
		"wg-monitor-deploy doctor",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("recovery hint missing %q:\n%s", want, got)
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

func TestDeferredAWGMFailureClassificationSeparatesDNSAndTLS(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "dns", msg: "urlopen error [Errno -2] Name or service not known", want: "dns_error"},
		{name: "tls", msg: "x509: certificate is valid for *.ddns.example, not awg.client-e.example", want: "tls_error"},
		{name: "auth", msg: "awgm POST /api/auth/login: HTTP 401: unauthorized", want: "auth_error"},
		{name: "offline", msg: "awgm GET /api/system/info: HTTP 503: service unavailable", want: "offline"},
		{name: "unknown", msg: "unexpected relay failure", want: "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDeferredAWGMFailure(tt.msg); got != tt.want {
				t.Fatalf("classifyDeferredAWGMFailure(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}

func TestRenderDeferredAWGMStatusSummarizesJobs(t *testing.T) {
	rows := []deferredAWGMStatusRow{
		{Nickname: "client-e", State: "tls_error", TargetVersion: "v0.13.0-rc72", Reason: "certificate is valid for *.ddns.example"},
		{Nickname: "client-g", State: "dns_error", TargetVersion: "v0.13.0-rc72", Reason: "Name or service not known"},
		{Nickname: "client-b", State: "patched", TargetVersion: "v0.13.0-rc72", Reason: "ok client-b"},
	}
	got := renderDeferredAWGMStatus(rows)
	for _, want := range []string{"client-e", "tls_error", "client-g", "dns_error", "client-b", "patched", "v0.13.0-rc72"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
	}
}

func TestScheduleDeferredAWGMDeploySupportsExistingAgentReenroll(t *testing.T) {
	oldInstall := installDeferredAWGMDeployViaVPSFunc
	defer func() { installDeferredAWGMDeployViaVPSFunc = oldInstall }()

	called := false
	installDeferredAWGMDeployViaVPSFunc = func(state *State, secrets *SecretStore, ag *AgentState, apiKey, login, pass, terminalUser, terminalPass string, rel *Release, wizardToken, expectedSHA string) error {
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
		if expectedSHA != strings.Repeat("e", 64) {
			t.Fatalf("expectedSHA=%q", expectedSHA)
		}
		return nil
	}

	base := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Jkaotlic/wg-monitor/releases":
			_, _ = w.Write([]byte(`[{"tag_name":"v0.13.0-rc37","assets":[{"name":"checksums.txt","browser_download_url":"` + base + `/checksums.txt"}]}]`))
		case "/checksums.txt", "/v1/releases/download/v0.13.0-rc37/checksums.txt":
			_, _ = w.Write([]byte(strings.Repeat("e", 64) + "  wg-monitor-agent-linux-arm64\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base = srv.URL

	oldAPI := GitHubAPIBase
	GitHubAPIBase = srv.URL
	defer func() { GitHubAPIBase = oldAPI }()

	state := &State{Backend: BackendState{
		Host:   "198.51.100.10",
		Domain: base,
	}}
	ag := &AgentState{
		Nickname:            "client-b",
		Kind:                "static",
		AWGMURL:             "https://awg.client-b.example.test",
		Arch:                "arm64",
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
        "nickname": "client-f-old",
        "last_deployed_version": "v0.13.0-rc57",
        "pending_version": "",
        "pending_since": "",
    }]
}
satisfied = write_job("satisfied.json", {
    "nickname": "client-f-old",
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
success_bootstrapped = {"done": False}
relay.run_bootstrap = lambda sock, cfg: success_bootstrapped.update(done=True)
relay.deferred_agent_token_valid = lambda cfg, raw_token: True
def success_wizard(cfg, method, path, body=None):
    if method == "POST" and path == "/v1/wizard/enrollments":
        return {"raw_token": "raw-token", "backend_url": "https://wg.example.test"}
    if method == "GET" and path == "/v1/wizard/agents" and success_bootstrapped["done"]:
        return {"agents": [{
            "nickname": "fresh",
            "last_deployed_version": "v0.13.0-rc57",
            "pending_version": "",
            "last_seen_at": time.strftime("%%Y-%%m-%%dT%%H:%%M:%%SZ", time.gmtime()),
        }]}
    return {}
relay.local_wizard_request = success_wizard
success = write_job("success.json", {
    "nickname": "fresh",
    "kind": "static",
    "target_version": "v0.13.0-rc57",
    "release_base": "https://wg.example.test/v1/releases/download",
    "expected_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "expires_at_unix": int(time.time()) + 3600,
    "wizard_token": "secret-token",
    "init_script": "#!/bin/sh\nexit 0",
    "confirm_timeout_sec": 0,
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
        "nickname": "client-g",
        "kind": "mobile",
        "base_url": "https://awg.client-g.example.test",
        "target_version": "v0.13.0-rc60",
        "backend_url": "https://wg.example.test",
        "release_base": "https://wg.example.test/v1/releases/download",
        "expected_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        "expires_at_unix": int(time.time()) + 3600,
        "wizard_token": "secret-token",
        "terminal_user": "root",
        "terminal_password": "keenetic",
        "init_script": "#!/bin/sh\nexit 0",
        "recovery_hint": "copy with ssh\nthen run sync-vps\nthen run doctor",
        "confirm_timeout_sec": 0,
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
        if "start" in events:
            return {"agents": [{
                "nickname": "client-g",
                "kind": "mobile",
                "last_deployed_version": "v0.13.0-rc60",
                "pending_version": "",
                "last_seen_at": time.strftime("%%Y-%%m-%%dT%%H:%%M:%%SZ", time.gmtime()),
            }]}
        return {"agents": [{
            "nickname": "client-g",
            "kind": "mobile",
            "last_deployed_version": "",
            "pending_version": "v0.13.0-rc60",
        }]}
    if method == "POST" and path == "/v1/wizard/enrollments":
        return {"raw_token": "early-token", "backend_url": "https://wg.example.test"}
    if method == "PUT" and path == "/v1/wizard/agents/client-g":
        put_body.update(body or {})
    return {}
relay.local_wizard_request = fake_wizard

def fake_commit(cfg, nick, raw_token):
    events.append("commit:%%s" %% nick)
relay.commit_existing_agent_token_hash = fake_commit
relay.deferred_agent_token_valid = lambda cfg, raw_token: True
relay.new_deferred_agent_raw_token = lambda: "existing-token"

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
    if "commit:client-g" not in events:
        raise AssertionError("service started before token hash commit")
    events.append("start")
relay.run_bootstrap = fake_bootstrap

relay.run_deferred_bootstrap(json.load(open(job_path, encoding="utf-8")), job_path)
assert events == [
    "wizard:GET:/v1/wizard/agents",
    "bootstrap",
    "commit:client-g",
    "start",
    "wizard:GET:/v1/wizard/agents",
    "wizard:PUT:/v1/wizard/agents/client-g",
], events
assert not os.path.exists(job_path), "successful existing-agent job remained active"
assert os.path.exists(job_path + ".done"), "successful existing-agent job did not write done artifact"
done = open(job_path + ".done", encoding="utf-8").read()
assert "token_env=WG_AGENT_TOKEN_BRONYA" in done, done
assert ("token_file=" + job_path + ".token") in done, done
assert "local deploy secrets" in done, done
assert "doctor/auth-probe" in done, done
assert "copy with ssh" in done, done
assert "then run sync-vps" in done, done
assert "then run doctor" in done, done
assert "existing-token" not in done, done
token = open(job_path + ".token", encoding="utf-8").read()
assert token == "WG_AGENT_TOKEN_BRONYA=existing-token\n", token
assert put_body["deploy_mode"] == "awgm", put_body
assert put_body["awgm_url"] == "https://awg.client-g.example.test", put_body
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

func TestAWGMRelayPythonDeferredBootstrapDoesNotCompleteWithoutFreshBackendHeartbeat(t *testing.T) {
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
import time

relay_path = %q
work_dir = %q
spec = importlib.util.spec_from_file_location("relay_under_test", relay_path)
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)

job_path = os.path.join(work_dir, "client-g.json")
with open(job_path, "w", encoding="utf-8") as f:
    json.dump({
        "nickname": "client-g",
        "kind": "mobile",
        "base_url": "https://awg.client-g.example.test",
        "target_version": "v0.13.0-rc60",
        "backend_url": "https://wg.example.test",
        "release_base": "https://wg.example.test/v1/releases/download",
        "expected_sha": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
        "expires_at_unix": int(time.time()) + 3600,
        "wizard_token": "secret-token",
        "terminal_user": "root",
        "terminal_password": "keenetic",
        "init_script": "#!/bin/sh\nexit 0",
        "confirm_timeout_sec": 0,
    }, f)

events = []
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
relay.run_bootstrap = lambda sock, cfg: events.append("bootstrap")
relay.commit_existing_agent_token_hash = lambda cfg, nick, raw_token: events.append("commit")

def fake_wizard(cfg, method, path, body=None):
    events.append("wizard:%%s:%%s" %% (method, path))
    if method == "GET" and path == "/v1/wizard/agents":
        return {"agents": [{
            "nickname": "client-g",
            "kind": "mobile",
            "last_deployed_version": "v0.13.0-rc60",
            "pending_version": "v0.13.0-rc60",
        }]}
    if method == "PUT" and path == "/v1/wizard/agents/client-g":
        raise AssertionError("deferred bootstrap must not clear pending before fresh heartbeat")
    return {}
relay.local_wizard_request = fake_wizard
relay.deferred_agent_token_valid = lambda cfg, raw_token: True

relay.run_deferred_bootstrap(json.load(open(job_path, encoding="utf-8")), job_path)
assert os.path.exists(job_path), "job must remain active until backend heartbeat confirms"
assert not os.path.exists(job_path + ".done"), "job without fresh backend heartbeat must not write done"
`, relayPath, dir)
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, harnessPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deferred stale-heartbeat harness failed: %v\n%s", err, out)
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
