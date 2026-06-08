package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0.13.0-rc109/" + asset:
			_, _ = w.Write(newBody)
		case "/v0.13.0-rc109/checksums.txt":
			_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	body := fmt.Sprintf(`{"target_version":"v0.13.0-rc109","repo_base":%q}`, srv.URL)
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
