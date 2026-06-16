package releasesig

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestVerifyChecksumsSignatureAcceptsValidSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldPub := releaseSigningPublicKey
	releaseSigningPublicKey = pub
	t.Cleanup(func() { releaseSigningPublicKey = oldPub })

	checksums := []byte("abc123  wg-monitor-agent-linux-arm64\n")
	sig := ed25519.Sign(priv, checksums)
	if err := VerifyChecksumsSignature(checksums, sig); err != nil {
		t.Fatalf("VerifyChecksumsSignature: %v", err)
	}
}

func TestVerifyChecksumsSignatureRejectsTampering(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldPub := releaseSigningPublicKey
	releaseSigningPublicKey = pub
	t.Cleanup(func() { releaseSigningPublicKey = oldPub })

	checksums := []byte("abc123  wg-monitor-agent-linux-arm64\n")
	sig := ed25519.Sign(priv, checksums)
	if err := VerifyChecksumsSignature([]byte("tampered\n"), sig); err == nil {
		t.Fatal("expected tampered checksums to fail signature verification")
	}
}

func TestSignatureRequiredForVersion(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"v0.13.0-rc127", false},
		{"v0.13.0-rc128", true},
		{"v0.13.0-rc129", true},
		{"v0.13.0", false},
		{"v0.13.1", true},
		{"v1.0.0", true},
		{"", true},
		{"v0.13.0-rc-1", true},
		{"v0.13.0-rc+1", true},
		{"v0.-1.0", true},
		{"v+0.13.0", true},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			if got := SignatureRequiredForVersion(c.version); got != c.want {
				t.Fatalf("SignatureRequiredForVersion(%q)=%v, want %v", c.version, got, c.want)
			}
		})
	}
}

func TestReleaseRankRejectsSignedComponents(t *testing.T) {
	cases := []string{
		"v0.13.0-rc-1",
		"v0.13.0-rc+1",
		"v0.-1.0",
		"v+0.13.0",
	}
	for _, version := range cases {
		t.Run(version, func(t *testing.T) {
			if _, ok := releaseRank(version); ok {
				t.Fatalf("releaseRank(%q) ok=true, want false", version)
			}
		})
	}
}
