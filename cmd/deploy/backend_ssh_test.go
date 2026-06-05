package main

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestParseBackendSSHAuthAnswerTreatsPastedKeyPathAsKey(t *testing.T) {
	auth, keyPath, prompt := parseBackendSSHAuthAnswer(`C:\Users\Anex\.ssh\id_ed25519_vps_main`)
	if auth != backendSSHAuthKey {
		t.Fatalf("auth=%q, want %q", auth, backendSSHAuthKey)
	}
	if keyPath != `C:\Users\Anex\.ssh\id_ed25519_vps_main` {
		t.Fatalf("keyPath=%q", keyPath)
	}
	if prompt {
		t.Fatal("pasted key path should not ask for another key path")
	}
}

func TestParseBackendSSHAuthAnswerKeepsExplicitModes(t *testing.T) {
	auth, keyPath, prompt := parseBackendSSHAuthAnswer("key")
	if auth != backendSSHAuthKey || keyPath != "" || !prompt {
		t.Fatalf("key answer parsed wrong: auth=%q keyPath=%q prompt=%v", auth, keyPath, prompt)
	}

	auth, keyPath, prompt = parseBackendSSHAuthAnswer("password")
	if auth != backendSSHAuthPassword || keyPath != "" || prompt {
		t.Fatalf("password answer parsed wrong: auth=%q keyPath=%q prompt=%v", auth, keyPath, prompt)
	}
}

func TestDialSSHRejectsInvalidSourceBind(t *testing.T) {
	cfg := &ssh.ClientConfig{Timeout: 10 * time.Millisecond}
	_, err := dialSSH("tcp", "127.0.0.1:22", cfg, "not an ip")
	if err == nil || !strings.Contains(err.Error(), "invalid source_bind") {
		t.Fatalf("err=%v, want invalid source_bind", err)
	}
}

func TestDialableTCPFromRejectsInvalidSourceBind(t *testing.T) {
	if dialableTCPFrom("127.0.0.1", 22, 10*time.Millisecond, "not an ip") {
		t.Fatal("invalid source_bind should not be dialable")
	}
}

func TestBackendDockerBase(t *testing.T) {
	if got := backendDockerBase("anex"); got != "/home/anex/wg-monitor" {
		t.Fatalf("backendDockerBase(anex)=%q", got)
	}
	if got := backendDockerBase("root"); got != "/root/wg-monitor" {
		t.Fatalf("backendDockerBase(root)=%q", got)
	}
}
