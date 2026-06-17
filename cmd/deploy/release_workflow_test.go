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
