package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestKnownHosts_NewHost_Adds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	kh, err := NewKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}

	// Fake key.
	signer, err := genTestSigner()
	if err != nil {
		t.Skip("genTestSigner unavailable, skipping")
	}
	pub := signer.PublicKey()

	if err := kh.HostKeyCallback("1.2.3.4:22", nil, pub); err != nil {
		t.Errorf("first connect TOFU should succeed, got %v", err)
	}

	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		t.Error("expected known_hosts to be written on first connect")
	}

	// Second connect with same key — should NOT error.
	if err := kh.HostKeyCallback("1.2.3.4:22", nil, pub); err != nil {
		t.Errorf("second connect with same key should succeed, got %v", err)
	}
}

func TestKnownHosts_KeyMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	kh, err := NewKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}

	s1, err := genTestSigner()
	if err != nil {
		t.Skip()
	}
	s2, err := genTestSigner()
	if err != nil {
		t.Skip()
	}

	kh.HostKeyCallback("1.2.3.4:22", nil, s1.PublicKey())

	err = kh.HostKeyCallback("1.2.3.4:22", nil, s2.PublicKey())
	if err == nil {
		t.Fatal("expected MITM detection error on key change")
	}
}

// TestKnownHosts_AliasScopesSameIP verifies the SSTP-rotation scenario:
// two physically different routers reachable through the SAME LAN IP at
// different times must not trigger the MITM guard when looked up under
// distinct aliases (their nicknames).
func TestKnownHosts_AliasScopesSameIP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	kh, err := NewKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := genTestSigner()
	if err != nil {
		t.Skip()
	}
	s2, err := genTestSigner()
	if err != nil {
		t.Skip()
	}

	cb1 := kh.HostKeyCallbackFor("router_alice")
	cb2 := kh.HostKeyCallbackFor("router_bob")

	if err := cb1("192.168.0.1:22", nil, s1.PublicKey()); err != nil {
		t.Fatalf("alice first connect: %v", err)
	}
	if err := cb2("192.168.0.1:22", nil, s2.PublicKey()); err != nil {
		t.Fatalf("bob first connect (same IP, different alias): %v", err)
	}
	if err := cb1("192.168.0.1:22", nil, s1.PublicKey()); err != nil {
		t.Errorf("alice second connect should still match: %v", err)
	}
	if err := cb2("192.168.0.1:22", nil, s2.PublicKey()); err != nil {
		t.Errorf("bob second connect should still match: %v", err)
	}
}

// TestKnownHosts_AliasMismatchStillCaught verifies the MITM guard remains
// effective WITHIN a single alias: if router_alice's key changes between
// connects, we must reject — only the cross-alias collision is fixed.
func TestKnownHosts_AliasMismatchStillCaught(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	kh, err := NewKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := genTestSigner()
	if err != nil {
		t.Skip()
	}
	s2, err := genTestSigner()
	if err != nil {
		t.Skip()
	}
	cb := kh.HostKeyCallbackFor("router_alice")
	if err := cb("192.168.0.1:22", nil, s1.PublicKey()); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if err := cb("192.168.0.1:22", nil, s2.PublicKey()); err == nil {
		t.Fatal("expected MITM detection on key change for same alias")
	}
}

func genTestSigner() (ssh.Signer, error) {
	// Generate ephemeral ed25519 key for tests.
	// (Implementation in ssh.go must export this helper, or use real ssh.NewSignerFromKey.)
	return generateEd25519Signer()
}
