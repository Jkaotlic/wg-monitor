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
		{"v0.13.0", true},
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

func TestCompareReleaseTags(t *testing.T) {
	cases := []struct {
		a, b    string
		wantCmp int
		wantOK  bool
		reason  string
	}{
		{"v0.13.0-rc10", "v0.13.0-rc9", 1, true, "rc10 must rank above rc9 (numeric, not lexical, RC compare)"},
		{"v0.13.0-rc9", "v0.13.0-rc10", -1, true, "rc9 must rank below rc10"},
		{"v0.13.0-rc9", "v0.13.0-rc9", 0, true, "identical tags rank equal"},
		{"v0.13.0", "v0.13.0-rc133", 1, true, "a full release outranks any RC of the same major.minor.patch"},
		{"v0.13.1", "v0.13.0", 1, true, "higher patch outranks lower patch"},
		{"v0.12.9", "v0.13.0", -1, true, "lower minor ranks below higher minor regardless of patch"},
		{"not-a-tag", "v0.13.0", 0, false, "unparseable left side is not ok"},
		{"v0.13.0", "not-a-tag", 0, false, "unparseable right side is not ok"},
	}
	for _, c := range cases {
		t.Run(c.a+"_vs_"+c.b, func(t *testing.T) {
			cmp, ok := CompareReleaseTags(c.a, c.b)
			if ok != c.wantOK {
				t.Fatalf("%s: CompareReleaseTags(%q,%q) ok=%v, want %v", c.reason, c.a, c.b, ok, c.wantOK)
			}
			if ok && sign(cmp) != sign(c.wantCmp) {
				t.Fatalf("%s: CompareReleaseTags(%q,%q)=%d, want sign %d", c.reason, c.a, c.b, cmp, c.wantCmp)
			}
		})
	}
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
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
