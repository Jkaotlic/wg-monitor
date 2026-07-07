package actions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

const expectedMaxSelfUpdateArtifactSize = 64 << 20
const expectedMaxSelfUpdateChecksumsSize = 1 << 20
const expectedMaxSelfUpdateSignatureSize = 4 << 10

func TestHTTPGetRejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(w, zeroReader{}, expectedMaxSelfUpdateArtifactSize+1)
	}))
	defer srv.Close()

	_, err := httpGet(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected oversized response to fail")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected size-limit error, got: %v", err)
	}
}

func TestHTTPGetLimitedUsesSmallerMetadataLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(w, zeroReader{}, expectedMaxSelfUpdateChecksumsSize+1)
	}))
	defer srv.Close()

	_, err := httpGetLimited(context.Background(), srv.Client(), srv.URL, expectedMaxSelfUpdateChecksumsSize)
	if err == nil {
		t.Fatal("expected oversized metadata response to fail")
	}
	if !strings.Contains(err.Error(), "too large") || !strings.Contains(err.Error(), "1048576") {
		t.Fatalf("expected checksum size-limit error, got: %v", err)
	}
}

func TestSelfUpdateRejectsUnsafeReleaseTag(t *testing.T) {
	_, err := SelfUpdate(context.Background(), "v0.13.0/../../bad", "", false)
	if err == nil {
		t.Fatal("expected unsafe release tag to fail")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected version validation error, got %v", err)
	}
}

// --- isSelfUpdateDowngrade (unit) -------------------------------------------

func TestIsSelfUpdateDowngrade(t *testing.T) {
	cases := []struct {
		name, target, current string
		want                  bool
	}{
		{"rc9 target vs rc10 current is a downgrade", "v0.13.0-rc9", "v0.13.0-rc10", true},
		{"rc10 target vs rc9 current is not a downgrade (numeric, not lexical, RC compare)", "v0.13.0-rc10", "v0.13.0-rc9", false},
		{"equal versions are not a downgrade", "v0.13.0-rc9", "v0.13.0-rc9", false},
		{"lower patch is a downgrade", "v0.13.0", "v0.13.1", true},
		{"higher patch is not a downgrade", "v0.13.1", "v0.13.0", false},
		{"empty current is never a downgrade", "v0.13.0-rc9", "", false},
		{"empty target is never a downgrade", "", "v0.13.0-rc9", false},
		{"unparseable current is never treated as a downgrade", "v0.13.0-rc9", "not-a-version", false},
		{"unparseable target is never treated as a downgrade", "not-a-version", "v0.13.0-rc9", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSelfUpdateDowngrade(c.target, c.current); got != c.want {
				t.Fatalf("isSelfUpdateDowngrade(%q,%q) = %v, want %v", c.target, c.current, got, c.want)
			}
		})
	}
}

// --- checkSelfUpdateFreeSpace (fake exec) -----------------------------------

func TestCheckSelfUpdateFreeSpaceSufficient(t *testing.T) {
	old := SelfUpdateExec
	t.Cleanup(func() { SelfUpdateExec = old })
	SelfUpdateExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "df" || strings.Join(args, " ") != "-k /opt" {
			t.Fatalf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		// total=1,000,000 KB, free=900,000 KB: comfortably more than the 64 MB
		// (65,536 KB) artifact ceiling plus the required 10% headroom.
		return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 1000000 100000 900000 10% /opt\n"), nil
	}
	if err := checkSelfUpdateFreeSpace(context.Background()); err != nil {
		t.Fatalf("expected sufficient space to pass, got %v", err)
	}
}

func TestCheckSelfUpdateFreeSpaceInsufficient(t *testing.T) {
	old := SelfUpdateExec
	t.Cleanup(func() { SelfUpdateExec = old })
	SelfUpdateExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// total=100,000 KB (10,000 KB headroom needed); free=70,000 KB; the
		// artifact ceiling alone (65,536 KB) leaves only 4,464 KB < headroom.
		return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 100000 30000 70000 30% /opt\n"), nil
	}
	err := checkSelfUpdateFreeSpace(context.Background())
	if err == nil {
		t.Fatal("expected insufficient space to fail")
	}
	if !strings.Contains(err.Error(), "space") {
		t.Fatalf("expected a space-related error, got %v", err)
	}
}

func TestCheckSelfUpdateFreeSpacePropagatesExecError(t *testing.T) {
	old := SelfUpdateExec
	t.Cleanup(func() { SelfUpdateExec = old })
	SelfUpdateExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("df: command not found")
	}
	if err := checkSelfUpdateFreeSpace(context.Background()); err == nil {
		t.Fatal("expected df exec error to propagate")
	}
}

// --- SelfUpdate: downgrade guard wiring -------------------------------------

func TestSelfUpdateRejectsDowngradeWithoutAllow(t *testing.T) {
	// repoBase deliberately unreachable: the downgrade guard must reject
	// before any network activity, so this must never actually be dialed.
	_, err := SelfUpdate(context.Background(), "v0.13.0-rc9", "v0.13.0-rc10", false, "https://127.0.0.1.invalid.example/unreachable")
	if err == nil {
		t.Fatal("expected downgrade to be rejected")
	}
	if !strings.Contains(err.Error(), "older") {
		t.Fatalf("expected a downgrade-specific error, got %v", err)
	}
}

func TestSelfUpdateAllowsDowngradeOverrideAndProceedsPastGuard(t *testing.T) {
	oldArch := selfUpdateDetectArch
	selfUpdateDetectArch = func() (string, error) { return "arm64", nil }
	t.Cleanup(func() { selfUpdateDetectArch = oldArch })
	oldValidate := selfUpdateValidateRepoBase
	selfUpdateValidateRepoBase = func(repoBase, _ string) (string, error) { return repoBase, nil }
	t.Cleanup(func() { selfUpdateValidateRepoBase = oldValidate })

	// No handlers registered: SelfUpdate should reach (and fail at) the
	// checksums.txt fetch — proof that the downgrade guard let it past the
	// version compare instead of rejecting it outright.
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	_, err := SelfUpdate(context.Background(), "v0.13.0-rc9", "v0.13.0-rc10", true, srv.URL)
	if err == nil {
		t.Fatal("expected the (unhandled) checksums.txt fetch to fail")
	}
	if strings.Contains(err.Error(), "older") {
		t.Fatalf("allow_downgrade should have bypassed the guard, got downgrade error: %v", err)
	}
	if !strings.Contains(err.Error(), "checksums.txt") {
		t.Fatalf("expected a checksums.txt download error proving guard bypass, got %v", err)
	}
}

// --- SelfUpdate: free-space guard wiring ------------------------------------

func selfUpdateFakeChecksumsServer(t *testing.T, assetName, sha string, assetHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			_, _ = w.Write([]byte(sha + "  " + assetName + "\n"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/"+assetName) {
			assetHandler(w, r)
			return
		}
		http.NotFound(w, r)
	})
	return httptest.NewServer(mux)
}

func TestSelfUpdateRejectsInsufficientOptSpaceBeforeDownloadingBinary(t *testing.T) {
	oldArch := selfUpdateDetectArch
	selfUpdateDetectArch = func() (string, error) { return "arm64", nil }
	t.Cleanup(func() { selfUpdateDetectArch = oldArch })
	oldValidate := selfUpdateValidateRepoBase
	selfUpdateValidateRepoBase = func(repoBase, _ string) (string, error) { return repoBase, nil }
	t.Cleanup(func() { selfUpdateValidateRepoBase = oldValidate })
	oldExec := SelfUpdateExec
	t.Cleanup(func() { SelfUpdateExec = oldExec })
	SelfUpdateExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 100000 30000 70000 30% /opt\n"), nil
	}

	assetHit := false
	srv := selfUpdateFakeChecksumsServer(t, "wg-monitor-agent-linux-arm64", strings.Repeat("ab", 32),
		func(w http.ResponseWriter, r *http.Request) {
			assetHit = true
			_, _ = w.Write([]byte("binary-bytes"))
		})
	defer srv.Close()

	_, err := SelfUpdate(context.Background(), "v0.13.0-rc50", "", false, srv.URL)
	if err == nil {
		t.Fatal("expected insufficient /opt space to fail")
	}
	if !strings.Contains(err.Error(), "space") {
		t.Fatalf("expected a space-related error, got %v", err)
	}
	if assetHit {
		t.Fatal("insufficient space must refuse before requesting the binary asset")
	}
}

func TestSelfUpdateProceedsPastFreeSpaceCheckWhenSufficient(t *testing.T) {
	oldArch := selfUpdateDetectArch
	selfUpdateDetectArch = func() (string, error) { return "arm64", nil }
	t.Cleanup(func() { selfUpdateDetectArch = oldArch })
	oldValidate := selfUpdateValidateRepoBase
	selfUpdateValidateRepoBase = func(repoBase, _ string) (string, error) { return repoBase, nil }
	t.Cleanup(func() { selfUpdateValidateRepoBase = oldValidate })
	oldExec := SelfUpdateExec
	t.Cleanup(func() { SelfUpdateExec = oldExec })
	SelfUpdateExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 1000000 100000 900000 10% /opt\n"), nil
	}

	assetHit := false
	srv := selfUpdateFakeChecksumsServer(t, "wg-monitor-agent-linux-arm64", strings.Repeat("ab", 32),
		func(w http.ResponseWriter, r *http.Request) {
			assetHit = true
			// Deliberately fail the request itself (rather than serve real
			// bytes) so this test stays scoped to "did the space check let us
			// reach the download" without also exercising the download/swap
			// machinery — that is covered end-to-end separately.
			http.Error(w, "boom", http.StatusInternalServerError)
		})
	defer srv.Close()

	_, err := SelfUpdate(context.Background(), "v0.13.0-rc50", "", false, srv.URL)
	if err == nil {
		t.Fatal("expected the deliberately-failing asset download to fail")
	}
	if strings.Contains(err.Error(), "space") {
		t.Fatalf("sufficient space must not be refused, got %v", err)
	}
	if !assetHit {
		t.Fatal("expected SelfUpdate to reach the binary asset request once space is sufficient")
	}
}

func TestSelfUpdateSwapScriptRollsBackWhenNewBinaryDoesNotStayRunning(t *testing.T) {
	script := selfUpdateSwapScript("/opt/bin/wg-monitor")

	required := []string{
		"/opt/etc/init.d/S99wg-monitor start",
		"is_running()",
		"pidof wg-monitor >/dev/null 2>&1",
		"pgrep -x wg-monitor >/dev/null 2>&1",
		"ps 2>/dev/null | grep '[w]g-monitor' >/dev/null 2>&1",
		"if ! is_running; then",
		"mv /opt/bin/wg-monitor.bak /opt/bin/wg-monitor",
		"/opt/etc/init.d/S99wg-monitor start",
	}
	last := -1
	for _, want := range required {
		idx := strings.Index(script[last+1:], want)
		if idx < 0 {
			t.Fatalf("script missing %q after offset %d:\n%s", want, last, script)
		}
		last += idx + len(want)
	}
}

// TestSelfUpdateSwapScriptPollsForHealthAfterStart verifies that the swap
// script checks liveness multiple times over a ~60 s window, not just once
// after 2 s. A crash-after-start (e.g. config parse error) would pass a
// single 2 s check but fail within the 60 s polling window.
func TestSelfUpdateSwapScriptPollsForHealthAfterStart(t *testing.T) {
	script := selfUpdateSwapScript("/opt/bin/wg-monitor")

	// Must contain a loop that polls for process health after start.
	if !strings.Contains(script, "while ") && !strings.Contains(script, "for ") {
		t.Fatal("script must contain a polling loop to detect crash-after-start")
	}
	// The window must be at least 30 s (we use 60 s in practice).
	if !strings.Contains(script, "60") && !strings.Contains(script, "30") {
		t.Fatal("script must poll for at least 30 s to detect delayed crashes")
	}
	// Rollback must still be present inside the polling path.
	if !strings.Contains(script, "mv /opt/bin/wg-monitor.bak /opt/bin/wg-monitor") {
		t.Fatal("script must roll back to .bak when health check fails")
	}
	// Must not sleep more than 5 s between checks (responsive rollback).
	if strings.Contains(script, "sleep 10") || strings.Contains(script, "sleep 15") || strings.Contains(script, "sleep 30") {
		t.Fatal("inter-check sleep is too long; rollback would be delayed")
	}
}

func TestSelfUpdateSwapCommandDoesNotRequireNohup(t *testing.T) {
	cmd := selfUpdateSwapCommand("/tmp/wg-monitor-swap.sh")
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "nohup") {
		t.Fatalf("swap command must not depend on nohup: %q", got)
	}
	if got != "sh /tmp/wg-monitor-swap.sh" {
		t.Fatalf("swap command=%q", got)
	}
}

func TestSelfUpdateSwapScriptPathUsesPrivateStateDir(t *testing.T) {
	got := selfUpdateSwapScriptPath()
	if strings.HasPrefix(got, "/tmp/") {
		t.Fatalf("swap script path must not be in world-writable /tmp: %q", got)
	}
	if got != "/opt/var/wg-monitor/self-update-swap.sh" {
		t.Fatalf("swap script path=%q", got)
	}
}

func TestSelfUpdateURLsCanUseBackendMirror(t *testing.T) {
	binURL, sumsURL := selfUpdateURLs("v0.13.0-rc18", "wg-monitor-agent-linux-arm64", "https://wg.example.test/v1/releases/download/")
	if binURL != "https://wg.example.test/v1/releases/download/v0.13.0-rc18/wg-monitor-agent-linux-arm64" {
		t.Fatalf("binURL=%q", binURL)
	}
	if sumsURL != "https://wg.example.test/v1/releases/download/v0.13.0-rc18/checksums.txt" {
		t.Fatalf("sumsURL=%q", sumsURL)
	}
}

func TestValidateSelfUpdateRepoBaseAllowsConfiguredBackendMirror(t *testing.T) {
	got, err := validateSelfUpdateRepoBase("https://wg.example.test/v1/releases/download", "https://wg.example.test")
	if err != nil {
		t.Fatalf("validateSelfUpdateRepoBase: %v", err)
	}
	if got != "https://wg.example.test/v1/releases/download" {
		t.Fatalf("repo_base=%q", got)
	}
}

func TestValidateSelfUpdateRepoBaseRejectsForeignBackendMirror(t *testing.T) {
	if _, err := validateSelfUpdateRepoBase("https://evil.example/v1/releases/download", "https://wg.example.test"); err == nil {
		t.Fatal("foreign backend mirror unexpectedly allowed")
	}
}

func TestValidateSelfUpdateRepoBaseAllowsCanonicalBackendMirrorWithoutBackendURL(t *testing.T) {
	got, err := validateSelfUpdateRepoBase("https://wgmonitor.anexaev.crazedns.ru/v1/releases/download", "")
	if err != nil {
		t.Fatalf("validateSelfUpdateRepoBase: %v", err)
	}
	if got != "https://wgmonitor.anexaev.crazedns.ru/v1/releases/download" {
		t.Fatalf("repo_base=%q", got)
	}
}

func TestHTTPGetWithFallbackRetriesNetworkFailureOnly(t *testing.T) {
	primary := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(`dial tcp: lookup wgmonitor.jkaotlic.duckdns.org on 127.0.0.1:53: i/o timeout`)
	})}
	fallbackHit := false
	fallback := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		fallbackHit = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}

	body, err := httpGetWithFallback(context.Background(), primary, fallback, "https://wg.example.test/file", "83.171.224.125")
	if err != nil {
		t.Fatalf("httpGetWithFallback: %v", err)
	}
	if string(body) != "ok" || !fallbackHit {
		t.Fatalf("fallback not used correctly: body=%q hit=%v", body, fallbackHit)
	}
}

func TestHTTPGetWithFallbackDoesNotRetryHTTPFailure(t *testing.T) {
	primary := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("missing")),
			Header:     make(http.Header),
		}, nil
	})}
	fallback := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("fallback must not run for HTTP errors")
		return nil, nil
	})}

	_, err := httpGetWithFallback(context.Background(), primary, fallback, "https://wg.example.test/file", "83.171.224.125")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected original HTTP 404, got %v", err)
	}
}

func TestHTTPClientForPinnedRepoHostDialsResolvedIPForSameHost(t *testing.T) {
	var dialAddr string
	c, label := httpClientForPinnedRepoHost("https://wgmonitor.jkaotlic.duckdns.org/v1/releases/download", "83.171.224.125", 5, func(network, addr string) (net.Conn, error) {
		dialAddr = addr
		return nil, errors.New("stop after dial capture")
	})
	if c == nil || label != "83.171.224.125" {
		t.Fatalf("client=%v label=%q", c, label)
	}
	_, _ = httpGet(context.Background(), c, "https://wgmonitor.jkaotlic.duckdns.org/v1/releases/download/v/tag")
	if dialAddr != "83.171.224.125:443" {
		t.Fatalf("dialAddr=%q", dialAddr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPGetToFileWritesBodyAndReturnsSHA256(t *testing.T) {
	content := bytes.Repeat([]byte("deadbeef"), 512) // 4 KB
	h := sha256.Sum256(content)
	wantSHA := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "agent.new")
	gotSHA, err := httpGetToFile(context.Background(), srv.Client(), srv.URL, dst, int64(len(content))+1)
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA != wantSHA {
		t.Fatalf("sha256: got %s want %s", gotSHA, wantSHA)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, content) {
		t.Fatal("file content does not match payload")
	}
}

func TestHTTPGetToFileRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(w, zeroReader{}, expectedMaxSelfUpdateArtifactSize+1)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "agent.new")
	_, err := httpGetToFile(context.Background(), srv.Client(), srv.URL, dst, expectedMaxSelfUpdateArtifactSize)
	if err == nil {
		t.Fatal("expected oversized response to fail")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected size-limit error, got: %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatal("temp file should be cleaned up on error")
	}
}

func TestHTTPGetToFileWithFallbackRetriesNetworkFailure(t *testing.T) {
	content := []byte("binary-content")
	h := sha256.Sum256(content)
	wantSHA := hex.EncodeToString(h[:])

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	defer fallback.Close()

	primaryClient := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	})}

	dst := filepath.Join(t.TempDir(), "agent.new")
	gotSHA, err := httpGetToFileWithFallback(context.Background(), primaryClient, fallback.Client(), fallback.URL, "fallback-ip", dst, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA != wantSHA {
		t.Fatalf("sha256: got %s want %s", gotSHA, wantSHA)
	}
}
