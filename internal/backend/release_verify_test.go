package backend

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifiedReleaseChecksums_RejectsMissingSig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("abc123  wg-monitor-agent-linux-arm64\n"))
	}))
	defer srv.Close()
	_, err := verifiedReleaseChecksums(context.Background(), srv.URL, "v9.9.9")
	if err == nil {
		t.Fatal("expected error when checksums.txt.sig is missing, got nil")
	}
}

func TestVerifiedReleaseChecksums_RejectsBadSig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			// Garbage bytes: neither the right ed25519 signature size nor a
			// signature that could verify against any key paired with the
			// checksums body below.
			_, _ = w.Write([]byte("not-a-real-signature"))
			return
		}
		_, _ = w.Write([]byte("abc123  wg-monitor-agent-linux-arm64\n"))
	}))
	defer srv.Close()
	_, err := verifiedReleaseChecksums(context.Background(), srv.URL, "v9.9.9")
	if err == nil {
		t.Fatal("expected error when checksums.txt.sig is garbage, got nil")
	}
}

// TestVerifiedReleaseChecksums_OK exercises the real fetch -> verify -> parse
// pipeline end to end with a genuine ed25519 signature.
//
// It cannot use the production release-signing public key embedded in
// internal/releasesig: the matching private seed is a GitHub Actions secret
// (WG_MONITOR_RELEASE_SIGNING_SEED_B64, see cmd/release-sign) and is not
// checked into this repo, so no test can produce a signature the real key
// would accept. internal/releasesig's own white-box tests work around this by
// swapping the unexported releaseSigningPublicKey var directly
// (signature_test.go); that var is not reachable from this package. Instead
// this test swaps the verifyChecksumsSignature package var (release_verify.go)
// for a verifier over a locally generated key pair, using the exact same
// generate-key-then-ed25519.Sign pattern internal/releasesig's tests use.
// Production code never reassigns verifyChecksumsSignature, so this is the
// only place the substitution happens.
func TestVerifiedReleaseChecksums_OK(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	origVerify := verifyChecksumsSignature
	verifyChecksumsSignature = func(checksums, signature []byte) error {
		if len(signature) != ed25519.SignatureSize {
			return fmt.Errorf("test release signature has invalid size %d", len(signature))
		}
		if !ed25519.Verify(pub, checksums, signature) {
			return fmt.Errorf("test release signature verification failed")
		}
		return nil
	}
	t.Cleanup(func() { verifyChecksumsSignature = origVerify })

	body := []byte("abc123  wg-monitor-agent-linux-arm64\ndef456  wg-monitor-backend-linux-amd64\n")
	sig := ed25519.Sign(priv, body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			_, _ = w.Write(sig)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := verifiedReleaseChecksums(context.Background(), srv.URL, "v9.9.9")
	if err != nil {
		t.Fatalf("verifiedReleaseChecksums: %v", err)
	}
	want := map[string]string{
		"wg-monitor-agent-linux-arm64":   "abc123",
		"wg-monitor-backend-linux-amd64": "def456",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("got[%q] = %q, want %q", k, got[k], v)
		}
	}
}
