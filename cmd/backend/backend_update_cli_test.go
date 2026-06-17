package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	withBackendUpdateHTTPClient(t, func(r *http.Request) (*http.Response, error) {
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
	})
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

func TestRunBackendUpdateRunnerRollsBackWhenRestartCommandFails(t *testing.T) {
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
	withBackendUpdateHTTPClient(t, func(r *http.Request) (*http.Response, error) {
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
	})
	body := fmt.Sprintf(`{"target_version":"v0.13.0-rc109","repo_base":%q}`, repoBase)
	if err := os.WriteFile(pending, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runBackendUpdateRunnerCommand([]string{
		"--pending-file", pending,
		"--binary", current,
		"--restart-cmd", "exit 1",
	})
	if err == nil {
		t.Fatal("expected restart failure")
	}
	got, readErr := os.ReadFile(current)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old" {
		t.Fatalf("binary should roll back to old after restart failure, got %q", string(got))
	}
	logBody, readErr := os.ReadFile(pending + ".last")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(logBody), "err") || !strings.Contains(string(logBody), "restart") {
		t.Fatalf("last status=%s", string(logBody))
	}
}

func TestRunBackendUpdateRunnerUsesRepoResolveIPFallback(t *testing.T) {
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

	withBackendUpdateHTTPClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: lookup wgmonitor.example.com: no such host")
	})

	oldPinned := newBackendUpdatePinnedHTTPClient
	newBackendUpdatePinnedHTTPClient = func(gotRepoBase, gotResolveIP string, timeoutSec int, dial func(network, addr string) (net.Conn, error)) (*http.Client, string) {
		if gotRepoBase != repoBase {
			t.Fatalf("repoBase=%q", gotRepoBase)
		}
		if gotResolveIP != "198.51.100.10" {
			t.Fatalf("resolveIP=%q", gotResolveIP)
		}
		if timeoutSec != 90 {
			t.Fatalf("timeoutSec=%d", timeoutSec)
		}
		return &http.Client{Transport: backendUpdateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			var body []byte
			switch r.URL.String() {
			case repoBase + "/v0.13.0-rc109/checksums.txt":
				body = []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset))
			case repoBase + "/v0.13.0-rc109/" + asset:
				body = newBody
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
		})}, "198.51.100.10"
	}
	t.Cleanup(func() { newBackendUpdatePinnedHTTPClient = oldPinned })

	body := fmt.Sprintf(`{"target_version":"v0.13.0-rc109","repo_base":%q,"repo_resolve_ip":"198.51.100.10"}`, repoBase)
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
}

func TestRunBackendUpdateRunnerAllowsTrustedBackendMirror(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "wg-monitor-backend")
	pending := filepath.Join(dir, "backend-update.json")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBody := []byte("new-binary")
	asset := "wg-monitor-backend-linux-" + runtime.GOARCH
	sum := sha256.Sum256(newBody)
	repoBase := "https://wg.example.test/v1/releases/download"
	withBackendUpdateHTTPClient(t, func(r *http.Request) (*http.Response, error) {
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
	})
	body := fmt.Sprintf(`{"target_version":"v0.13.0-rc109","repo_base":%q,"trusted_backend_url":"https://wg.example.test"}`, repoBase)
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
}

func TestBackendUpdatePinnedHTTPClientDialsResolvedIPForRepoHost(t *testing.T) {
	var dialAddr string
	c, label := backendUpdatePinnedHTTPClient(releaseorigin.DefaultBackendMirrorBase, "198.51.100.10", 5, func(network, addr string) (net.Conn, error) {
		dialAddr = addr
		return nil, errors.New("stop after dial capture")
	})
	if c == nil || label != "198.51.100.10" {
		t.Fatalf("client=%v label=%q", c, label)
	}
	_, _ = httpGetBytesLimitedWithClient(context.Background(), c, releaseorigin.DefaultBackendMirrorBase+"/v/tag/checksums.txt", 1024)
	if dialAddr != "198.51.100.10:443" {
		t.Fatalf("dialAddr=%q", dialAddr)
	}
}

func TestBackendUpdatePrimaryHTTPClientIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	c := backendUpdatePrimaryHTTPClient(7)
	if c.Timeout != 7*time.Second {
		t.Fatalf("timeout=%v", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T", c.Transport)
	}
	if tr.Proxy != nil {
		req, err := http.NewRequest(http.MethodGet, releaseorigin.DefaultBackendMirrorBase+"/v/tag/checksums.txt", nil)
		if err != nil {
			t.Fatal(err)
		}
		proxyURL, proxyErr := tr.Proxy(req)
		t.Fatalf("primary backend update client must not use environment proxy, proxy=%v err=%v", proxyURL, proxyErr)
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

func TestRunBackendUpdateRunnerRejectsForeignBackendMirror(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "wg-monitor-backend")
	pending := filepath.Join(dir, "backend-update.json")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"target_version":"v0.13.0-rc109","repo_base":"https://evil.example/v1/releases/download","trusted_backend_url":"https://wg.example.test"}`
	if err := os.WriteFile(pending, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runBackendUpdateRunnerCommand([]string{
		"--pending-file", pending,
		"--binary", current,
	})
	if err == nil {
		t.Fatal("expected foreign backend mirror to fail")
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
}

func TestRunBackendUpdateRunnerRejectsUnsafeReleaseTag(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "wg-monitor-backend")
	pending := filepath.Join(dir, "backend-update.json")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"target_version":"v0.13.0/../../bad","repo_base":%q}`, releaseorigin.DefaultBackendMirrorBase)
	if err := os.WriteFile(pending, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runBackendUpdateRunnerCommand([]string{
		"--pending-file", pending,
		"--binary", current,
	})
	if err == nil {
		t.Fatal("expected unsafe target_version to fail")
	}
	if !strings.Contains(err.Error(), "target_version") {
		t.Fatalf("expected target_version validation error, got %v", err)
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
	if !strings.Contains(string(logBody), "err") || !strings.Contains(string(logBody), "target_version") {
		t.Fatalf("last status=%s", string(logBody))
	}
}

func TestRunBackendUpdateRunnerRequiresSignatureForNewRelease(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "wg-monitor-backend")
	pending := filepath.Join(dir, "backend-update.json")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	asset := "wg-monitor-backend-linux-" + runtime.GOARCH
	repoBase := releaseorigin.DefaultBackendMirrorBase
	withBackendUpdateHTTPClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case repoBase + "/v0.13.0-rc128/checksums.txt":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(strings.Repeat("0", 64) + "  " + asset + "\n")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}
	})
	body := fmt.Sprintf(`{"target_version":"v0.13.0-rc128","repo_base":%q}`, repoBase)
	if err := os.WriteFile(pending, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runBackendUpdateRunnerCommand([]string{
		"--pending-file", pending,
		"--binary", current,
	})
	if err == nil {
		t.Fatal("expected missing checksums signature to fail")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected signature error, got %v", err)
	}
}

func TestRunBackendUpdateRunnerRejectsOversizedAsset(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "wg-monitor-backend")
	pending := filepath.Join(dir, "backend-update.json")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	asset := "wg-monitor-backend-linux-" + runtime.GOARCH
	repoBase := releaseorigin.DefaultBackendMirrorBase
	withBackendUpdateHTTPClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case repoBase + "/v0.13.0-rc109/checksums.txt":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(strings.Repeat("0", 64) + "  " + asset + "\n")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		case repoBase + "/v0.13.0-rc109/" + asset:
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(io.LimitReader(zeroReader{}, (64<<20)+1)),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}
	})
	body := fmt.Sprintf(`{"target_version":"v0.13.0-rc109","repo_base":%q}`, repoBase)
	if err := os.WriteFile(pending, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runBackendUpdateRunnerCommand([]string{
		"--pending-file", pending,
		"--binary", current,
	})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected oversized asset error, got %v", err)
	}
	got, readErr := os.ReadFile(current)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old" {
		t.Fatalf("binary changed to %q", string(got))
	}
}

func withBackendUpdateHTTPClient(t *testing.T, rt backendUpdateRoundTripFunc) {
	t.Helper()
	old := newBackendUpdatePrimaryHTTPClient
	newBackendUpdatePrimaryHTTPClient = func(timeoutSec int) *http.Client {
		if timeoutSec != 90 {
			t.Fatalf("timeoutSec=%d", timeoutSec)
		}
		return &http.Client{Transport: rt}
	}
	t.Cleanup(func() { newBackendUpdatePrimaryHTTPClient = old })
}

type backendUpdateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f backendUpdateRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
