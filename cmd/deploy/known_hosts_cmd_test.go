package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKnownHostsFixture creates a synthetic known_hosts file with three
// entries keyed by alias:port and IP:port. Uses real ed25519 key bytes so
// downstream tooling that wants to parse the file with golang.org/x/crypto
// would not choke (we don't actually parse the keys here — we only care
// about the first column).
func writeKnownHostsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	// Generate a real ssh public key once; reuse its marshalled form for
	// every line (the file is just three host entries pointing at the same
	// fake key — semantics-wise that's fine for a list/forget test).
	signer, err := generateEd25519Signer()
	if err != nil {
		t.Fatalf("generate signer: %v", err)
	}
	pub := signer.PublicKey()
	keyType := pub.Type()
	keyBlob := pub.Marshal()
	keyB64 := encodeBase64(keyBlob)

	lines := []string{
		"# generated for test",
		"testkeen:22 " + keyType + " " + keyB64,
		"client_alice:22 " + keyType + " " + keyB64,
		"192.168.0.1:22 " + keyType + " " + keyB64,
		"",
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// encodeBase64 wraps stdlib base64 in the form ssh known_hosts expects
// (standard, non-URL-safe, no padding stripping).
func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func TestListKnownHostAliases_ReturnsAllThree(t *testing.T) {
	path := writeKnownHostsFixture(t)
	got, err := ListKnownHostAliases(path)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	wantSet := map[string]bool{
		"testkeen":     true,
		"client_alice": true,
		"192.168.0.1": true,
	}
	if len(got) != len(wantSet) {
		t.Fatalf("got %d aliases, want %d (%v)", len(got), len(wantSet), got)
	}
	for _, a := range got {
		if !wantSet[a] {
			t.Errorf("unexpected alias in list: %q", a)
		}
	}
}

func TestForgetKnownHost_RemovesOne(t *testing.T) {
	path := writeKnownHostsFixture(t)
	n, err := ForgetKnownHost(path, "client_alice")
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	got, err := ListKnownHostAliases(path)
	if err != nil {
		t.Fatalf("re-list: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("after forget, got %d aliases, want 2 (%v)", len(got), got)
	}
	for _, a := range got {
		if a == "client_alice" {
			t.Errorf("client_alice still present after forget: %v", got)
		}
	}
}

func TestForgetKnownHost_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope")
	n, err := ForgetKnownHost(path, "anything")
	if err != nil {
		t.Errorf("forget on missing file should be a no-op, got %v", err)
	}
	if n != 0 {
		t.Errorf("removed %d on missing file, want 0", n)
	}
}

func TestStripPort_Variants(t *testing.T) {
	cases := map[string]string{
		"testkeen:22":    "testkeen",
		"192.168.0.1":   "192.168.0.1",
		"192.168.0.1:22": "192.168.0.1",
		"[::1]:2222":     "::1",
		"bare_alias":     "bare_alias",
	}
	for in, want := range cases {
		if got := stripPort(in); got != want {
			t.Errorf("stripPort(%q) = %q, want %q", in, got, want)
		}
	}
}
