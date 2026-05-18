package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	if err := cb1("192.168.31.1:22", nil, s1.PublicKey()); err != nil {
		t.Fatalf("alice first connect: %v", err)
	}
	if err := cb2("192.168.31.1:22", nil, s2.PublicKey()); err != nil {
		t.Fatalf("bob first connect (same IP, different alias): %v", err)
	}
	if err := cb1("192.168.31.1:22", nil, s1.PublicKey()); err != nil {
		t.Errorf("alice second connect should still match: %v", err)
	}
	if err := cb2("192.168.31.1:22", nil, s2.PublicKey()); err != nil {
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
	if err := cb("192.168.31.1:22", nil, s1.PublicKey()); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if err := cb("192.168.31.1:22", nil, s2.PublicKey()); err == nil {
		t.Fatal("expected MITM detection on key change for same alias")
	}
}

func TestKnownHosts_ReplaceHostKey(t *testing.T) {
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
	if err := cb("192.168.31.1:22", nil, s1.PublicKey()); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if err := kh.ReplaceHostKey("router_alice", s2.PublicKey()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := cb("192.168.31.1:22", nil, s2.PublicKey()); err != nil {
		t.Fatalf("new key should match after replace: %v", err)
	}
	if err := cb("192.168.31.1:22", nil, s1.PublicKey()); err == nil {
		t.Fatal("old key should fail after replace")
	}
}

func genTestSigner() (ssh.Signer, error) {
	// Generate ephemeral ed25519 key for tests.
	// (Implementation in ssh.go must export this helper, or use real ssh.NewSignerFromKey.)
	return generateEd25519Signer()
}

// TestDiagnoseSSHErr_Hints covers each known failure-mode → operator-hint
// mapping, plus the nil and untranslated branches. Synthetic errors only;
// no real SSH server.
func TestDiagnoseSSHErr_Hints(t *testing.T) {
	addr := "192.168.31.1:22"
	cases := []struct {
		name      string
		raw       string
		wantHint  string // substring expected in message ("" → no hint added)
		wantOrig  bool   // original message preserved (substring check via raw)
		passthru  bool   // expect verbatim wrap (no hint), used for HOST KEY CHANGED
		wantNoErr bool
	}{
		{
			name:     "auth-failure",
			raw:      "ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password]",
			wantHint: "пароль не подошёл",
			wantOrig: true,
		},
		{
			name:     "no-route-to-host",
			raw:      "dial tcp 192.168.31.1:22: connect: no route to host",
			wantHint: "хост недоступен",
			wantOrig: true,
		},
		{
			name:     "network-unreachable",
			raw:      "dial tcp 10.0.0.1:22: connect: network is unreachable",
			wantHint: "хост недоступен",
			wantOrig: true,
		},
		{
			name:     "io-timeout",
			raw:      "dial tcp 192.168.31.1:22: i/o timeout",
			wantHint: "таймаут SSH-handshake",
			wantOrig: true,
		},
		{
			name:     "deadline-exceeded",
			raw:      "context deadline exceeded",
			wantHint: "таймаут SSH-handshake",
			wantOrig: true,
		},
		{
			name:     "connection-refused",
			raw:      "dial tcp 192.168.31.1:22: connect: connection refused",
			wantHint: "порт закрыт",
			wantOrig: true,
		},
		{
			name:     "host-key-changed-passthru",
			raw:      "HOST KEY CHANGED for router_alice — possible MITM attack. Inspect /tmp/known_hosts ...",
			wantHint: "",
			passthru: true,
			wantOrig: true,
		},
		{
			name:     "untranslated",
			raw:      "some weird ssh error nobody mapped yet",
			wantHint: "",
			wantOrig: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := errors.New(tc.raw)
			got := diagnoseSSHErr(addr, orig)
			if got == nil {
				t.Fatalf("expected non-nil error")
			}
			gotMsg := got.Error()
			if !strings.Contains(gotMsg, addr) {
				t.Errorf("expected addr %q in message, got %q", addr, gotMsg)
			}
			if tc.wantOrig && !strings.Contains(gotMsg, tc.raw) {
				t.Errorf("expected original substring %q in message, got %q", tc.raw, gotMsg)
			}
			if tc.wantHint != "" && !strings.Contains(gotMsg, tc.wantHint) {
				t.Errorf("expected hint %q in message, got %q", tc.wantHint, gotMsg)
			}
			if tc.passthru {
				// HOST KEY CHANGED branch must not add an extra hint colon
				// before the original — only the addr prefix.
				want := "ssh " + addr + ": " + tc.raw
				if gotMsg != want {
					t.Errorf("passthru: want %q, got %q", want, gotMsg)
				}
			}
			// errors.Is/As must still see the wrapped original.
			if !errors.Is(got, orig) {
				t.Errorf("expected errors.Is to find original error")
			}
		})
	}
}

func TestDiagnoseSSHErr_Nil(t *testing.T) {
	if err := diagnoseSSHErr("x:22", nil); err != nil {
		t.Errorf("expected nil for nil input, got %v", err)
	}
}

// TestProgressDots_SilentBelowThreshold verifies no output is emitted for
// tiny payloads (< 256 KiB), matching the small-config-upload case.
func TestProgressDots_SilentBelowThreshold(t *testing.T) {
	var buf bytes.Buffer
	p := newProgressDots(100<<10, &buf) // 100 KiB
	p.add(100 << 10)
	p.finish()
	if buf.Len() != 0 {
		t.Errorf("expected silent for sub-threshold payload, got %q", buf.String())
	}
}

// TestProgressDots_DotPerMiB feeds a 5 MiB payload in small chunks and
// expects exactly five dots followed by a newline.
func TestProgressDots_DotPerMiB(t *testing.T) {
	var buf bytes.Buffer
	total := 5 << 20
	p := newProgressDots(total, &buf)
	// Feed in 64 KiB chunks (mirrors UploadStdin's chunk pattern, scaled).
	chunk := 64 << 10
	for written := 0; written < total; written += chunk {
		n := chunk
		if written+n > total {
			n = total - written
		}
		p.add(n)
	}
	p.finish()
	got := buf.String()
	want := strings.Repeat(".", 5) + "\n"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestProgressDots_FinishIdempotent guards against the defer-style double
// finish() pattern used in UploadSFTP/UploadStdin error paths.
func TestProgressDots_FinishIdempotent(t *testing.T) {
	var buf bytes.Buffer
	p := newProgressDots(2<<20, &buf)
	p.add(2 << 20)
	p.finish()
	p.finish() // second call should be a no-op.
	got := buf.String()
	want := "..\n"
	if got != want {
		t.Errorf("expected %q after double finish, got %q", want, got)
	}
}

// TestProgressDots_WriteCountsBytes ensures the io.Writer adapter feeds the
// same byte counter as add() and never echoes payload data into the sink.
func TestProgressDots_WriteCountsBytes(t *testing.T) {
	var buf bytes.Buffer
	p := newProgressDots(3<<20, &buf)
	payload := make([]byte, 1<<20)
	for i := 0; i < 3; i++ {
		n, err := p.Write(payload)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != len(payload) {
			t.Fatalf("Write: short, got %d want %d", n, len(payload))
		}
	}
	p.finish()
	got := buf.String()
	want := "...\n"
	if got != want {
		t.Errorf("expected %q, got %q (writer must not echo payload)", want, got)
	}
}

// TestProgressDots_NoDotsNoNewline verifies that when total >= threshold
// but no full MiB boundary is crossed, finish() does NOT emit a stray
// newline (avoids a bogus blank line in the operator's terminal).
func TestProgressDots_NoDotsNoNewline(t *testing.T) {
	var buf bytes.Buffer
	// 300 KiB: above 256 KiB threshold, but below the 1 MiB dot step.
	p := newProgressDots(300<<10, &buf)
	p.add(300 << 10)
	p.finish()
	if buf.Len() != 0 {
		t.Errorf("expected no output when no dot boundary crossed, got %q", buf.String())
	}
}
