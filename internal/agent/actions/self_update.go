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

	"github.com/anex/wg-monitor/internal/releaseorigin"
	"github.com/anex/wg-monitor/internal/releasesig"
)

// SelfUpdateRepoBase is the GitHub Releases base URL the agent pulls from.
// Overridable for tests; kept as a var rather than a const so a future fork
// can rebuild with a different default. Tag is appended at call time.
var SelfUpdateRepoBase = "https://github.com/Jkaotlic/wg-monitor/releases/download"

// SelfUpdateExec runs the handful of external commands SelfUpdate's own
// preflight checks need (currently just `df -k /opt`). A package var (like
// SelfUpdateRepoBase above) rather than a Runner field, since SelfUpdate is a
// free function, not a Runner method; tests substitute a fake to avoid
// depending on a real df binary.
var SelfUpdateExec ExecFunc = DefaultExec

// selfUpdateDetectArch resolves the release asset suffix for this host. A
// package var wrapping detectAgentArch so tests running on a dev/CI
// machine's native GOARCH (never arm64 or mipsle in this project's release
// matrix) can fake a supported arch and exercise the rest of SelfUpdate's
// sequence instead of always failing at this first step.
var selfUpdateDetectArch = detectAgentArch

// selfUpdateValidateRepoBase indirects validateSelfUpdateRepoBase so tests
// can substitute a passthrough validator. releaseorigin's checks correctly
// hard-reject every loopback/private-IP origin (httptest.Server always binds
// to 127.0.0.1), which would otherwise make it impossible to drive
// SelfUpdate's real HTTP sequence against a local test server at all.
var selfUpdateValidateRepoBase = validateSelfUpdateRepoBase

// selfUpdateVerifyChecksumsSignature wraps releasesig.VerifyChecksumsSignature
// (see internal/backend/release_verify.go's identical verifyChecksumsSignature
// var for the full rationale): the production signing public key baked into
// internal/releasesig has no matching private key in this repo (the signing
// seed is a GitHub Actions secret), so an accept-path test cannot produce a
// signature the real key would verify without substituting this var for a
// verifier over a locally generated key pair. Production code never
// reassigns it.
var selfUpdateVerifyChecksumsSignature = releasesig.VerifyChecksumsSignature

// selfUpdateStateDir holds the swap script + its log. A var (not const) so
// tests point it at a t.TempDir()-backed sandbox instead of the real
// /opt/var/wg-monitor.
var selfUpdateStateDir = "/opt/var/wg-monitor"

// selfUpdateBinPath is the live agent binary SelfUpdate ultimately replaces —
// SelfUpdate itself only ever writes selfUpdateBinPath+".new"; the actual
// swap to this path happens later, inside the detached swap script. A var so
// tests write to a t.TempDir()-backed path instead of the real
// /opt/bin/wg-monitor, and can assert it stays byte-for-byte untouched when
// SelfUpdate fails before ever handing off to that script.
var selfUpdateBinPath = "/opt/bin/wg-monitor"

const (
	maxSelfUpdateChecksumsSize = 1 << 20
	maxSelfUpdateSignatureSize = 4 << 10
	maxSelfUpdateArtifactSize  = 64 << 20
	// selfUpdateEstimatedBinaryKB is the disk-space preflight's estimate of
	// the new binary's size — deliberately NOT maxSelfUpdateArtifactSize
	// (that constant is the HTTP download's hard safety cap, guarding
	// against a corrupted/oversized CDN response; it has never reflected an
	// actual release size). Real agent binaries are UPX-packed and land
	// around 2.1 MB (measured from the v0.14.3 release assets); 8 MB is
	// ~4x that, generous slack for the binary growing over future releases
	// without demanding space no real update will ever need. Routers with
	// small Entware /opt partitions (100-250 MB is typical on Keenetic)
	// were being refused updates over a 64 MB estimate ~32x too large — see
	// docs/superpowers/specs/2026-07-13-self-update-space-check-realistic-estimate-design.md.
	selfUpdateEstimatedBinaryKB = 8192
	// selfUpdateMinFreeRatioPct is the same "≥10% of the partition must
	// stay free post-write" floor OpkgRunner.SmartUpgrade's df -k /opt check
	// (opkg.go) uses, applied here to the estimated agent binary size
	// instead of an opkg package set.
	selfUpdateMinFreeRatioPct = 10
)

// SelfUpdate downloads the agent binary for the given release tag, verifies
// its SHA-256 against checksums.txt from the same release, writes it to
// /opt/bin/wg-monitor.new, and spawns a detached shell script that swaps the
// binary and restarts the agent via S99wg-monitor. The current process
// returns "ok" BEFORE the swap occurs so the backend gets a CommandResult
// ack via /v1/cmd/result before the running agent is killed; the wizard
// then polls for the next heartbeat's agent_version to confirm the flip.
//
// currentVersion is the agent's own running version (e.g. main.Version),
// used only for the downgrade guard below; an empty value skips that guard
// (nothing to compare against). allowDowngrade overrides it.
//
// Before any of the above: version must not rank below currentVersion
// (isSelfUpdateDowngrade) unless allowDowngrade is set, and — after the
// small checksums.txt/signature fetch but before streaming the ≤64 MB
// binary — /opt must have enough free space (checkSelfUpdateFreeSpace).
// Both guards return an error without touching the filesystem or network
// beyond what they each individually need.
//
// On any failure prior to spawning the swap script, the running agent is
// left untouched on the old binary.
func SelfUpdate(ctx context.Context, version, currentVersion string, allowDowngrade bool, repoBaseOpt ...string) (string, error) {
	validVersion, err := releaseorigin.ValidateReleaseTag(version)
	if err != nil {
		return "", fmt.Errorf("self_update: %w", err)
	}
	version = validVersion

	if !allowDowngrade && isSelfUpdateDowngrade(version, currentVersion) {
		return "", fmt.Errorf("self_update: target version %s is older than the running %s — pass allow_downgrade to override", version, currentVersion)
	}

	arch, err := selfUpdateDetectArch()
	if err != nil {
		return "", err
	}

	assetName := "wg-monitor-agent-linux-" + arch
	repoBase := SelfUpdateRepoBase
	if len(repoBaseOpt) > 0 && strings.TrimSpace(repoBaseOpt[0]) != "" {
		repoBase = strings.TrimSpace(repoBaseOpt[0])
	}
	trustedBackendURL := ""
	if len(repoBaseOpt) > 2 {
		trustedBackendURL = strings.TrimSpace(repoBaseOpt[2])
	}
	repoBase, err = selfUpdateValidateRepoBase(repoBase, trustedBackendURL)
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
		if err := selfUpdateVerifyChecksumsSignature(sumsBody, sigBody); err != nil {
			return "", fmt.Errorf("verify checksums.txt signature: %w", err)
		}
	}
	wantSha, ok := parseChecksum(string(sumsBody), assetName)
	if !ok {
		return "", fmt.Errorf("checksums.txt: no entry for %s", assetName)
	}

	if err := checkSelfUpdateFreeSpace(ctx); err != nil {
		return "", err
	}

	// Stream the binary directly to disk — never load 64 MB into RAM.
	// Critical on MIPSLE routers where total RAM is ≤64 MB.
	binPath := selfUpdateBinPath
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

func validateSelfUpdateRepoBase(repoBase, trustedBackendURL string) (string, error) {
	return releaseorigin.ValidateRepoBaseForBackendURL(repoBase, []string{
		SelfUpdateRepoBase,
		releaseorigin.DefaultBackendMirrorBase,
	}, trustedBackendURL)
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

// selfUpdateSwapCommand builds the exec.Cmd that runs the swap script. A
// package var (default: `sh scriptPath`) so tests substitute a harmless,
// always-present process instead of depending on a POSIX shell being
// resolvable wherever `go test` runs — this test binary re-invoked with a
// no-op test filter (`-test.run=^$`) is the standard portable stand-in (see
// os/exec's own test suite for the same trick).
var selfUpdateSwapCommand = func(scriptPath string) *exec.Cmd {
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

// isSelfUpdateDowngrade reports whether target ranks strictly below current
// in this project's release-tag order (major.minor.patch, then RC number).
// Delegates to releasesig.CompareReleaseTags — already an agent-side
// dependency for SignatureRequiredForVersion/VerifyChecksumsSignature —
// rather than adding a fourth local copy of the same rank/compare pair
// already duplicated in internal/backend/dashboard_handler.go
// (compareDashboardReleaseTags, used by the wizard deploy paths' own
// isVersionDowngrade) and internal/backend/callbacks/fleet_batch.go
// (compareReleaseTagsLocal). See CompareReleaseTags's doc comment for why
// golang.org/x/mod/semver is deliberately not used here either (it would
// rank "-rc9" after "-rc10").
//
// An empty target or current is never a downgrade: current is empty when
// the running binary's version wasn't wired in (defensive default — should
// not happen in practice, main.Version always has a value), and an
// unparseable tag on either side means "cannot determine" rather than
// "reject" — self_update must not refuse to run over a version string whose
// shape it doesn't recognize.
func isSelfUpdateDowngrade(target, current string) bool {
	target = strings.TrimSpace(target)
	current = strings.TrimSpace(current)
	if target == "" || current == "" {
		return false
	}
	cmp, ok := releasesig.CompareReleaseTags(target, current)
	return ok && cmp < 0
}

// checkSelfUpdateFreeSpace runs `df -k /opt` (via SelfUpdateExec, so tests
// can fake it) and refuses to proceed if writing another
// selfUpdateEstimatedBinaryKB-sized binary to /opt/bin/wg-monitor.new would
// leave less than selfUpdateMinFreeRatioPct of the partition free —
// mirroring OpkgRunner.SmartUpgrade's own df -k /opt safety check (opkg.go).
// Unlike SmartUpgrade's per-package Installed-Size estimate (opkg has no
// fixed upper bound on an upgrade's total size), the "needed" size here is a
// simple constant: real release binaries vary little in size release to
// release, so a fixed realistic estimate (see selfUpdateEstimatedBinaryKB's
// doc comment) serves just as well as a per-asset total, without needing to
// probe the actual release first.
func checkSelfUpdateFreeSpace(ctx context.Context) error {
	out, err := SelfUpdateExec(ctx, "df", "-k", "/opt")
	if err != nil {
		return fmt.Errorf("self_update: df /opt: %w", err)
	}
	freeKB, totalKB, err := parseDfOptOutput(out)
	if err != nil {
		return fmt.Errorf("self_update: df /opt: %w", err)
	}
	neededKB := int64(selfUpdateEstimatedBinaryKB)
	headroomKB := totalKB * selfUpdateMinFreeRatioPct / 100
	if freeKB-neededKB < headroomKB {
		return fmt.Errorf("self_update: insufficient /opt space: %d KB free, need %d KB for the new binary plus %d KB headroom (%d KB total)",
			freeKB, neededKB, headroomKB, totalKB)
	}
	return nil
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
