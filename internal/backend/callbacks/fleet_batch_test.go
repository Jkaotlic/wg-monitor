package callbacks

import "testing"

func TestIsSafeSelfUpdateVersionRejectsMalformedVersions(t *testing.T) {
	for _, version := range []string{"v0.13.foo", "v0.13.0-rcx", "not-a-version"} {
		if isSafeSelfUpdateVersion(version) {
			t.Fatalf("malformed version %q should not be safe for fleet self-update", version)
		}
	}
}

func TestIsSafeSelfUpdateVersionKeepsKnownBoundaries(t *testing.T) {
	if !isSafeSelfUpdateVersion("v0.13.0-rc32") {
		t.Fatal("rc32 should be safe")
	}
	if isSafeSelfUpdateVersion("v0.13.0-rc31") {
		t.Fatal("rc31 should be unsafe")
	}
	if !isSafeSelfUpdateVersion("v0.13.0") {
		t.Fatal("stable v0.13.0 should be safe")
	}
}
