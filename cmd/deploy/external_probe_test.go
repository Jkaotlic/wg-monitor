package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderExternalProbeFilesContractAndNoSecretState(t *testing.T) {
	f, err := renderExternalProbeFiles("https://wgmon.example.com/readyz", `tok"en`, "123")
	if err != nil {
		t.Fatal(err)
	}
	s := string(f.Script)
	for _, want := range []string{"--max-time 5", `[ "$body" = "ready" ]`, `failures=$((failures + 1))`, `[ "$failures" -ge 2 ]`, "mv -f \"$TMP\" \"$STATE\"", "RECOVERED", "DOWN"} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q", want)
		}
	}
	if strings.Contains(s, "tok") || strings.Contains(s, "123") {
		t.Fatal("notification secrets leaked into script/state logic")
	}
	if !strings.Contains(string(f.Env), `TG_TOKEN="tok\"en"`) {
		t.Fatalf("env quoting wrong: %q", f.Env)
	}
	if strings.Contains(string(f.Service), "tok") || strings.Contains(string(f.Timer), "tok") {
		t.Fatal("secret leaked into unit")
	}
}

func TestRenderExternalProbeFilesRejectsUnsafeOrWrongURL(t *testing.T) {
	for _, raw := range []string{"http://example.com/readyz", "https://example.com/healthz", "https://example.com/readyz?x=1", "https://example.com/readyz';touch /tmp/x"} {
		if _, err := renderExternalProbeFiles(raw, "token", "chat"); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if _, err := renderExternalProbeFiles("https://example.com/readyz", "bad\nvalue", "chat"); err == nil {
		t.Fatal("accepted multiline secret")
	}
}

func TestInstallExternalProbeUploadsModesAndChecksPostconditions(t *testing.T) {
	r := newFakeRemote()
	r.reply = func(cmd string) (string, string, int, error) {
		if strings.Contains(cmd, "mktemp -d") {
			return "/tmp/wg-monitor-probe.A1b2C3d4\n", "", 0, nil
		}
		return "", "", 0, nil
	}
	f, _ := renderExternalProbeFiles("https://example.com/readyz", "token", "chat")
	if err := installExternalProbe(r, f); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]string{"/tmp/wg-monitor-probe.A1b2C3d4/probe": "0700", "/tmp/wg-monitor-probe.A1b2C3d4/probe.env": "0600", "/tmp/wg-monitor-probe.A1b2C3d4/probe.service": "0644", "/tmp/wg-monitor-probe.A1b2C3d4/probe.timer": "0644"} {
		if r.indexOfCall("upload: "+path+" mode="+mode) < 0 {
			t.Errorf("missing upload %s mode %s", path, mode)
		}
	}
	joined := strings.Join(r.calls, "\n")
	for _, want := range []string{"enable --now wg-monitor-probe.timer", "is-enabled --quiet", "test -x /usr/local/bin/wg-monitor-probe", "test -r /etc/wg-monitor-probe.env"} {
		if !strings.Contains(joined, want) {
			t.Errorf("install missing %q", want)
		}
	}
	for _, hardening := range []string{"DynamicUser=yes", "StateDirectory=wg-monitor-probe", "ProtectSystem=strict", "NoNewPrivileges=yes"} {
		if !strings.Contains(string(f.Service), hardening) {
			t.Errorf("service missing hardening %q", hardening)
		}
	}
}

func TestInstallExternalProbeRejectsUnsafeMktempPath(t *testing.T) {
	r := newFakeRemote()
	r.reply = func(string) (string, string, int, error) { return "/tmp/wg-monitor-probe;touch /tmp/x\n", "", 0, nil }
	f, _ := renderExternalProbeFiles("https://example.com/readyz", "token", "chat")
	if err := installExternalProbe(r, f); err == nil {
		t.Fatal("unsafe remote temporary path was accepted")
	}
	if len(r.uploads) != 0 {
		t.Fatal("files uploaded before temporary path validation")
	}
}

func TestExternalProbeStatusAndRemoveHavePostconditions(t *testing.T) {
	r := newFakeRemote()
	if _, err := externalProbeStatus(r); err != nil {
		t.Fatal(err)
	}
	if err := removeExternalProbe(r); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.calls, "\n")
	if !strings.Contains(joined, "cat /var/lib/wg-monitor-probe/state") {
		t.Fatal("status does not inspect persisted state")
	}
	if !strings.Contains(joined, "test ! -e /usr/local/bin/wg-monitor-probe") {
		t.Fatal("remove lacks postcondition")
	}
}

func TestExternalProbePreviewContainsNoSecrets(t *testing.T) {
	s := &State{ExternalProbe: ExternalProbeState{Host: "probe.example.com", Port: 2222, User: "ops", ReadyURL: "https://wgmon.example.com/readyz"}}
	got, err := externalProbePreview(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"probe.example.com:2222", "https://wgmon.example.com/readyz", "timeout=5s", "failures=2", "host_key=external-probe"} {
		if !strings.Contains(got, want) {
			t.Errorf("preview missing %q: %s", want, got)
		}
	}
}

func TestExternalProbeScriptDeduplicatesAndRecovers(t *testing.T) {
	f, err := renderExternalProbeFiles("https://example.com/readyz", "token", "chat")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "probe")
	curl := filepath.Join(dir, "curl")
	response := filepath.Join(dir, "response")
	notifications := filepath.Join(dir, "notifications")
	if err := os.WriteFile(script, f.Script, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeCurl := `#!/bin/sh
case "$*" in
  *api.telegram.org*) printf '%s\n' "$*" >> "$NOTIFICATIONS"; exit 0;;
esac
cat "$RESPONSE"
[ "$(cat "$RESPONSE")" = ready ]
`
	if err := os.WriteFile(curl, []byte(fakeCurl), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(response, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(wantOK bool) {
		cmd := exec.Command("/bin/sh", script)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "WG_PROBE_STATE="+filepath.Join(dir, "state"), "TG_TOKEN=token", "TG_CHAT=chat", "RESPONSE="+response, "NOTIFICATIONS="+notifications)
		err := cmd.Run()
		if wantOK && err != nil {
			t.Fatalf("probe should pass: %v", err)
		}
		if !wantOK && err == nil {
			t.Fatal("probe should fail")
		}
	}
	run(false) // first failure: no page
	run(false) // second failure: one DOWN
	run(false) // deduplicated
	b, _ := os.ReadFile(notifications)
	if got := strings.Count(string(b), "sendMessage"); got != 1 {
		t.Fatalf("DOWN notifications=%d, want 1: %s", got, b)
	}
	if err := os.WriteFile(response, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(true)
	b, _ = os.ReadFile(notifications)
	if got := strings.Count(string(b), "sendMessage"); got != 2 {
		t.Fatalf("notifications after recovery=%d, want 2: %s", got, b)
	}
	state, err := os.ReadFile(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if string(state) != "ok 0\n" {
		t.Fatalf("state=%q, want ok 0", state)
	}
}
