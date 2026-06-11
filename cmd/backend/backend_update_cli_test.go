package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
)

func TestRunBackendUpdateRunnerSwapsBinaryAndClearsPending(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "wg-monitor-backend")
	pending := filepath.Join(dir, "backend-update.json")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBody := []byte("new-binary")
	asset := "wg-monitor-backend-linux-" + runtime.GOARCH
	sum := sha256.Sum256(newBody)
	repoBase := releaseorigin.DefaultBackendMirrorBase
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: backendUpdateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body []byte
		switch r.URL.String() {
		case repoBase + "/v0.13.0-rc109/" + asset:
			body = newBody
		case repoBase + "/v0.13.0-rc109/checksums.txt":
			body = []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset))
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = oldClient })
	body := fmt.Sprintf(`{"target_version":"v0.13.0-rc109","repo_base":%q}`, repoBase)
	if err := os.WriteFile(pending, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runBackendUpdateRunnerCommand([]string{
		"--pending-file", pending,
		"--binary", current,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("binary=%q", string(got))
	}
	if _, err := os.Stat(current + ".bak"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending should be removed, err=%v", err)
	}
	logBody, err := os.ReadFile(pending + ".last")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBody), "v0.13.0-rc109") || !strings.Contains(string(logBody), "ok") {
		t.Fatalf("last status=%s", string(logBody))
	}
}

func TestRunBackendUpdateRunnerRejectsUntrustedRepoBase(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "wg-monitor-backend")
	pending := filepath.Join(dir, "backend-update.json")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"target_version":"v0.13.0-rc109","repo_base":"http://127.0.0.1:1/v1/releases/download"}`
	if err := os.WriteFile(pending, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runBackendUpdateRunnerCommand([]string{
		"--pending-file", pending,
		"--binary", current,
	})
	if err == nil {
		t.Fatal("expected untrusted repo_base to fail")
	}
	if !strings.Contains(err.Error(), "repo_base") {
		t.Fatalf("expected repo_base validation error, got %v", err)
	}
	got, readErr := os.ReadFile(current)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old" {
		t.Fatalf("binary changed to %q", string(got))
	}
	logBody, readErr := os.ReadFile(pending + ".last")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(logBody), "err") || !strings.Contains(string(logBody), "repo_base") {
		t.Fatalf("last status=%s", string(logBody))
	}
}

type backendUpdateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f backendUpdateRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
