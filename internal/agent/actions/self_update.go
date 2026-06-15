package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
	"github.com/Jkaotlic/wg-monitor/internal/releasesig"
)

// SelfUpdateRepoBase is the GitHub Releases base URL the agent pulls from.
// Overridable for tests; kept as a var rather than a const so a future fork
// can rebuild with a different default. Tag is appended at call time.
var SelfUpdateRepoBase = "https://github.com/Jkaotlic/wg-monitor/releases/download"

const (
	maxSelfUpdateChecksumsSize = 1 << 20
	maxSelfUpdateSignatureSize = 4 << 10
	maxSelfUpdateArtifactSize  = 64 << 20
	selfUpdateStateDir         = "/opt/var/wg-monitor"
)

// SelfUpdate downloads the agent binary for the given release tag, verifies
// its SHA-256 against checksums.txt from the same release, writes it to
// /opt/bin/wg-monitor.new, and spawns a detached shell script that swaps the
// binary and restarts the agent via S99wg-monitor. The current process
// returns "ok" BEFORE the swap occurs so the backend gets a CommandResult
// ack via /v1/cmd/result before the running agent is killed; the wizard
// then polls for the next heartbeat's agent_version to confirm the flip.
//
// On any failure prior to spawning the swap script, the running agent is
// left untouched on the old binary.
func SelfUpdate(ctx context.Context, version string, repoBaseOpt ...string) (string, error) {
	validVersion, err := releaseorigin.ValidateReleaseTag(version)
	if err != nil {
		return "", fmt.Errorf("self_update: %w", err)
	}
	version = validVersion

	arch, err := detectAgentArch()
	if err != nil {
		return "", err
	}

	assetName := "wg-monitor-agent-linux-" + arch
	repoBase := SelfUpdateRepoBase
	if len(repoBaseOpt) > 0 && strings.TrimSpace(repoBaseOpt[0]) != "" {
		repoBase = strings.TrimSpace(repoBaseOpt[0])
	}
	repoBase, err = releaseorigin.ValidateRepoBase(repoBase, []string{
		SelfUpdateRepoBase,
		releaseorigin.DefaultBackendMirrorBase,
	})
	if err != nil {
		return "", fmt.Errorf("self_update: %w", err)
	}
	binURL, sumsURL := selfUpdateURLs(version, assetName, repoBase)

	httpClient := &http.Client{Timeout: 60 * time.Second}
	var fallbackClient *http.Client
	var fallbackLabel string
	if len(repoBaseOpt) > 1 && strings.TrimSpace(repoBaseOpt[1]) != "" {
		fallbackClient, fallbackLabel = httpClientForPinnedRepoHost(repoBase, strings.TrimSpace(repoBaseOpt[1]), 60, nil)
	}

	// Download checksums.txt first (small file, safe to buffer in memory).
	sumsBody, err := httpGetWithFallback(ctx, httpClient, fallbackClient, sumsURL, fallbackLabel, maxSelfUpdateChecksumsSize)
	if err != nil {
		return "", fmt.Errorf("download checksums.txt: %w", err)
	}
	if releasesig.SignatureRequiredForVersion(version) {
		sigBody, err := httpGetWithFallback(ctx, httpClient, fallbackClient, sumsURL+".sig", fallbackLabel, maxSelfUpdateSignatureSize)
		if err != nil {
			return "", fmt.Errorf("download checksums.txt.sig: %w", err)
		}
		if err := releasesig.VerifyChecksumsSignature(sumsBody, sigBody); err != nil {
			return "", fmt.Errorf("verify checksums.txt signature: %w", err)
		}
	}
	wantSha, ok := parseChecksum(string(sumsBody), assetName)
	if !ok {
		return "", fmt.Errorf("checksums.txt: no entry for %s", assetName)
	}

	// Stream the binary directly to disk — never load 64 MB into RAM.
	// Critical on MIPSLE routers where total RAM is ≤64 MB.
	const binPath = "/opt/bin/wg-monitor"
	tmpPath := binPath + ".new"
	gotSha, err := httpGetToFileWithFallback(ctx, httpClient, fallbackClient, binURL, fallbackLabel, tmpPath, maxSelfUpdateArtifactSize)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", assetName, err)
	}
	if !strings.EqualFold(gotSha, wantSha) {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("sha256 mismatch: want %s got %s", wantSha[:16], gotSha[:16])
	}

	scriptPath := selfUpdateSwapScriptPath()
	script := selfUpdateSwapScript(binPath)
	if err := writeSelfUpdateSwapScript(scriptPath, script); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write %s: %w", scriptPath, err)
	}

	if err := launchSelfUpdateSwap(scriptPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	return fmt.Sprintf("%s verified, swap scheduled in ~3s", version), nil
}

func selfUpdateSwapScriptPath() string {
	return selfUpdateStateDir + "/self-update-swap.sh"
}

func writeSelfUpdateSwapScript(path, script string) error {
	if err := os.MkdirAll(selfUpdateStateDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o700)
	return nil
}

func launchSelfUpdateSwap(scriptPath string) error {
	logPath := selfUpdateStateDir + "/self-update-swap.log"
	_ = os.MkdirAll(selfUpdateStateDir, 0o700)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		logFile = nil
	} else {
		defer logFile.Close()
	}

	cmd := selfUpdateSwapCommand(scriptPath)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn swap script: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release swap script: %w", err)
	}
	return nil
}

func selfUpdateSwapCommand(scriptPath string) *exec.Cmd {
	return exec.Command("sh", scriptPath)
}

func selfUpdateURLs(version, assetName, repoBase string) (binURL, sumsURL string) {
	base := strings.TrimRight(repoBase, "/")
	return base + "/" + version + "/" + assetName, base + "/" + version + "/checksums.txt"
}

// detectAgentArch maps GOARCH to the asset suffix produced by the release
// workflow (arm64, mipsle). Returns an error on any other arch so that a
// self_update command sent to a non-Keenetic target fails fast instead of
// downloading the wrong binary.
func detectAgentArch() (string, error) {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64", nil
	case "mipsle":
		return "mipsle", nil
	default:
		return "", fmt.Errorf("self_update: unsupported GOARCH %q (expected arm64 or mipsle)", runtime.GOARCH)
	}
}

func httpGet(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	return httpGetLimited(ctx, c, url, maxSelfUpdateArtifactSize)
}

func httpGetLimited(ctx context.Context, c *http.Client, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response too large: exceeds %d bytes", maxBytes)
	}
	return body, nil
}

// httpGetToFile streams the response body to dst+".tmp", computing sha256 in
// the same pass, then atomically renames to dst. Returns the sha256 hex.
// Avoids loading large binaries into RAM — critical for 64 MB agents on
// MIPSLE routers with ≤64 MB total memory.
func httpGetToFile(ctx context.Context, c *http.Client, rawURL, dst string, maxBytes int64) (sha256hex string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, rawURL)
	}

	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", closeErr
	}
	if n > maxBytes {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("response too large: exceeds %d bytes", maxBytes)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func httpGetWithFallback(ctx context.Context, primary, fallback *http.Client, url, fallbackLabel string, maxBytes ...int64) ([]byte, error) {
	limit := int64(maxSelfUpdateArtifactSize)
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = maxBytes[0]
	}
	body, err := httpGetLimited(ctx, primary, url, limit)
	if err == nil {
		return body, nil
	}
	if fallback == nil || strings.TrimSpace(fallbackLabel) == "" || !isSelfUpdateTransportError(err) {
		return nil, err
	}
	body, fallbackErr := httpGetLimited(ctx, fallback, url, limit)
	if fallbackErr != nil {
		return nil, fmt.Errorf("%w; retry via %s failed: %v", err, fallbackLabel, fallbackErr)
	}
	return body, nil
}

func httpGetToFileWithFallback(ctx context.Context, primary, fallback *http.Client, rawURL, fallbackLabel, dst string, maxBytes int64) (string, error) {
	sha, err := httpGetToFile(ctx, primary, rawURL, dst, maxBytes)
	if err == nil {
		return sha, nil
	}
	if fallback == nil || strings.TrimSpace(fallbackLabel) == "" || !isSelfUpdateTransportError(err) {
		return "", err
	}
	sha, fallbackErr := httpGetToFile(ctx, fallback, rawURL, dst, maxBytes)
	if fallbackErr != nil {
		return "", fmt.Errorf("%w; retry via %s failed: %v", err, fallbackLabel, fallbackErr)
	}
	return sha, nil
}

func isSelfUpdateTransportError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{
		"lookup ",
		"no such host",
		"server misbehaving",
		"temporary failure",
		"i/o timeout",
		"timeout",
		"deadline exceeded",
		"network is unreachable",
		"no route to host",
		"connection refused",
		"connection reset",
		"connection aborted",
		"tls handshake timeout",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func httpClientForPinnedRepoHost(repoBase, resolveIP string, timeoutSec int, dial func(network, addr string) (net.Conn, error)) (*http.Client, string) {
	ip := strings.TrimSpace(resolveIP)
	if net.ParseIP(ip) == nil {
		return nil, ""
	}
	u, err := url.Parse(strings.TrimSpace(repoBase))
	if err != nil || u.Hostname() == "" {
		return nil, ""
	}
	repoHost := strings.ToLower(u.Hostname())
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	dialer := &net.Dialer{Timeout: time.Duration(timeoutSec) * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || !strings.EqualFold(host, repoHost) {
			return dialer.DialContext(ctx, network, addr)
		}
		pinnedAddr := net.JoinHostPort(ip, port)
		if dial == nil {
			return dialer.DialContext(ctx, network, pinnedAddr)
		}
		return dial(network, pinnedAddr)
	}
	return &http.Client{Timeout: time.Duration(timeoutSec) * time.Second, Transport: transport}, ip
}

// parseChecksum picks the SHA-256 hex for asset `name` from a checksums.txt
// where every line is "<hex>  <filename>" (two spaces, GNU sha256sum
// format). Returns ("", false) if no line matches.
func parseChecksum(body, name string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[len(fields)-1] == name {
			return fields[0], true
		}
	}
	return "", false
}

func selfUpdateSwapScript(binPath string) string {
	return `#!/bin/sh
sleep 3
/opt/etc/init.d/S99wg-monitor stop 2>/dev/null
killall -9 wg-monitor 2>/dev/null
sleep 1
cp -p ` + binPath + ` ` + binPath + `.bak 2>/dev/null
mv ` + binPath + `.new ` + binPath + `
chmod 755 ` + binPath + `
/opt/etc/init.d/S99wg-monitor start
is_running() {
	if command -v pidof >/dev/null 2>&1 && pidof wg-monitor >/dev/null 2>&1; then
		return 0
	fi
	if command -v pgrep >/dev/null 2>&1 && pgrep -x wg-monitor >/dev/null 2>&1; then
		return 0
	fi
	ps 2>/dev/null | grep '[w]g-monitor' >/dev/null 2>&1
}
# Poll for 60 s (12 × 5 s) to catch crashes that occur after initial startup.
_ok=0
_i=0
while [ $_i -lt 12 ]; do
	sleep 5
	if ! is_running; then
		_ok=0
		break
	fi
	_ok=1
	_i=$((_i + 1))
done
if [ $_ok -eq 0 ]; then
	/opt/etc/init.d/S99wg-monitor stop 2>/dev/null
	mv ` + binPath + `.bak ` + binPath + `
	chmod 755 ` + binPath + `
	/opt/etc/init.d/S99wg-monitor start
fi
`
}
