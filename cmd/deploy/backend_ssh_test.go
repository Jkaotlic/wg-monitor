package main

import "testing"

func TestParseBackendSSHAuthAnswerTreatsPastedKeyPathAsKey(t *testing.T) {
	auth, keyPath, prompt := parseBackendSSHAuthAnswer(`C:\Users\user\.ssh\id_ed25519_vps_main`)
	if auth != backendSSHAuthKey {
		t.Fatalf("auth=%q, want %q", auth, backendSSHAuthKey)
	}
	if keyPath != `C:\Users\user\.ssh\id_ed25519_vps_main` {
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
