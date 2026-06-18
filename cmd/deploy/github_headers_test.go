package main

import (
	"net/http"
	"testing"
)

func TestApplyGitHubAPIHeaders_AddsBearerWhenTokenSet(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "secret-tok")
	req, _ := http.NewRequest("GET", "https://api.github.com/x", nil)
	applyGitHubAPIHeaders(req)
	if got := req.Header.Get("Authorization"); got != "Bearer secret-tok" {
		t.Fatalf("Authorization=%q, want %q", got, "Bearer secret-tok")
	}
	if req.Header.Get("Accept") != "application/vnd.github+json" {
		t.Fatalf("Accept header missing: %q", req.Header.Get("Accept"))
	}
}

func TestApplyGitHubAPIHeaders_FallsBackToGHToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "gh-tok")
	req, _ := http.NewRequest("GET", "https://api.github.com/x", nil)
	applyGitHubAPIHeaders(req)
	if got := req.Header.Get("Authorization"); got != "Bearer gh-tok" {
		t.Fatalf("Authorization=%q, want %q", got, "Bearer gh-tok")
	}
}

func TestApplyGitHubAPIHeaders_NoTokenNoAuthHeader(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	req, _ := http.NewRequest("GET", "https://api.github.com/x", nil)
	applyGitHubAPIHeaders(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization=%q, want empty (no token configured)", got)
	}
}
