package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateBackendURLReplacesURL(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(cfg, []byte(`backend:
  url: https://old.example.com
  token: "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abc1"

agent:
  nickname: testagent
  interval_sec: 60
`), 0600)
	if err != nil {
		t.Fatal(err)
	}

	if err := rewriteBackendURL(cfg, "https://new.example.com"); err != nil {
		t.Fatalf("rewriteBackendURL: %v", err)
	}

	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	content := string(got)
	if !strings.Contains(content, "url: https://new.example.com") {
		t.Errorf("new URL not present: %s", content)
	}
	if strings.Contains(content, "old.example.com") {
		t.Errorf("old URL still present: %s", content)
	}
}

func TestUpdateBackendURLPreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	original := `backend:
  url: https://old.example.com
  token: "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abc1"

agent:
  nickname: myrouter
  interval_sec: 60

# keep this comment
checks:
  dns:
    auto_discover: true
`
	if err := os.WriteFile(cfg, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	if err := rewriteBackendURL(cfg, "https://new.example.com"); err != nil {
		t.Fatalf("rewriteBackendURL: %v", err)
	}

	got, _ := os.ReadFile(cfg)
	content := string(got)

	for _, want := range []string{
		"myrouter",
		"abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abc1",
		"auto_discover: true",
		"keep this comment",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("field %q lost after rewrite:\n%s", want, content)
		}
	}
}

func TestUpdateBackendURLRejectsNonHTTPS(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(cfg, []byte("backend:\n  url: https://old.example.com\n"), 0600)

	err := rewriteBackendURL(cfg, "http://insecure.example.com")
	if err == nil {
		t.Fatal("expected error for non-https URL, got nil")
	}
}

func TestUpdateBackendURLRejectsPrivateHost(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(cfg, []byte("backend:\n  url: https://old.example.com\n"), 0600)

	err := rewriteBackendURL(cfg, "https://192.168.0.87")
	if err == nil {
		t.Fatal("expected error for private backend URL")
	}
}

func TestUpdateBackendURLHealthCheckFailureDoesNotRewriteOrRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	original := "backend:\n  url: https://old.example.com\n"
	if err := os.WriteFile(cfg, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	oldCheck := backendURLHealthCheck
	backendURLHealthCheck = func(context.Context, string) error {
		return errors.New("health down")
	}
	t.Cleanup(func() { backendURLHealthCheck = oldCheck })
	oldRestart := scheduleURLUpdateRestart
	restarted := false
	scheduleURLUpdateRestart = func() { restarted = true }
	t.Cleanup(func() { scheduleURLUpdateRestart = oldRestart })

	_, err := UpdateBackendURL(context.Background(), "https://new.example.com", cfg)
	if err == nil {
		t.Fatal("expected health-check failure")
	}
	if restarted {
		t.Fatal("restart must not be scheduled when health check fails")
	}
	got, readErr := os.ReadFile(cfg)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("config changed despite failed health check:\n%s", string(got))
	}
}

func TestUpdateBackendURLRejectsMissingURL(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(cfg, []byte("backend:\n  url: https://old.example.com\n"), 0600)

	err := rewriteBackendURL(cfg, "")
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

func TestUpdateBackendURLFileNotFound(t *testing.T) {
	err := rewriteBackendURL("/nonexistent/config.yaml", "https://new.example.com")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestUpdateBackendURLNoSectionMatch(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	// config without backend.url line
	_ = os.WriteFile(cfg, []byte("agent:\n  nickname: test\n"), 0600)

	err := rewriteBackendURL(cfg, "https://new.example.com")
	if err == nil {
		t.Fatal("expected error when backend.url not found")
	}
}

func TestUpdateBackendURLWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(cfg, []byte("backend:\n  url: https://old.example.com\n  token: \"abc123abc123abc123abc123abc123abc123\"\n"), 0600)

	if err := rewriteBackendURL(cfg, "https://new.example.com"); err != nil {
		t.Fatal(err)
	}
	// Verify no temp file left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
