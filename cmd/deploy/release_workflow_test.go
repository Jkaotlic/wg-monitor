package main

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseWorkflowRunsPreflightBeforePublishing(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"preflight:",
		"go vet ./...",
		"go test ./... -count=1",
		"govulncheck ./...",
		"gosec -severity high",
		"grype dir:. --fail-on high",
		"needs: [preflight, build]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q", want)
		}
	}
}

func TestReleaseWorkflowManualDispatchRequiresExplicitTag(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "workflow_dispatch:") {
		return
	}
	for _, want := range []string{
		"inputs:",
		"tag:",
		"tag_name: ${{ inputs.tag || github.ref_name }}",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manual release workflow missing %q", want)
		}
	}
}

// AGENT-02: строка-привязка тега дописывается в checksums.txt ПОСЛЕ
// последнего пересчёта и ДО подписи, тег берётся из env (не подставляется
// выражением прямо в shell).
func TestReleaseWorkflowBindsTagIntoSignedChecksums(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	recheck := strings.Index(text, "Re-checksum with SBOM included")
	bind := strings.Index(text, "VERSION-")
	sign := strings.Index(text, "cmd/release-sign")
	if recheck < 0 || bind < 0 || sign < 0 {
		t.Fatalf("workflow steps missing: recheck=%d bind=%d sign=%d", recheck, bind, sign)
	}
	if !(recheck < bind && bind < sign) {
		t.Fatalf("VERSION line must be appended after the last re-checksum and before signing: recheck=%d bind=%d sign=%d", recheck, bind, sign)
	}
	for _, want := range []string{
		`RELEASE_TAG: ${{ inputs.tag || github.ref_name }}`,
		`printf '%s' "$RELEASE_TAG" | sha256sum`,
		`>> checksums.txt`,
	} {
		if !strings.Contains(text[recheck:sign], want) {
			t.Fatalf("binding step missing %q", want)
		}
	}
}
