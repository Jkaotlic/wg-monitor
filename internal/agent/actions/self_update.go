package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// SelfUpdateRepoBase is the GitHub Releases base URL the agent pulls from.
// Overridable for tests; kept as a var rather than a const so a future fork
// can rebuild with a different default. Tag is appended at call time.
var SelfUpdateRepoBase = "https://github.com/Jkaotlic/wg-monitor/releases/download"

const maxSelfUpdateArtifactSize = 64 << 20

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
	if version == "" {
		return "", fmt.Errorf("self_update: version is required")
	}

	arch, err := detectAgentArch()
	if err != nil {
		return "", err
	}

	assetName := "wg-monitor-agent-linux-" + arch
	repoBase := SelfUpdateRepoBase
	if len(repoBaseOpt) > 0 && strings.TrimSpace(repoBaseOpt[0]) != "" {
		repoBase = strings.TrimSpace(repoBaseOpt[0])
	}
	binURL, sumsURL := selfUpdateURLs(version, assetName, repoBase)

	httpClient := &http.Client{Timeout: 60 * time.Second}

	binBytes, err := httpGet(ctx, httpClient, binURL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", assetName, err)
	}
	sumsBody, err := httpGet(ctx, httpClient, sumsURL)
	if err != nil {
		return "", fmt.Errorf("download checksums.txt: %w", err)
	}

	wantSha, ok := parseChecksum(string(sumsBody), assetName)
	if !ok {
		return "", fmt.Errorf("checksums.txt: no entry for %s", assetName)
	}
	gotSha := sha256Hex(binBytes)
	if !strings.EqualFold(gotSha, wantSha) {
		return "", fmt.Errorf("sha256 mismatch: want %s got %s", wantSha[:16], gotSha[:16])
	}

	const binPath = "/opt/bin/wg-monitor"
	tmpPath := binPath + ".new"
	if err := writeFileAtomic(tmpPath, binBytes, 0o755); err != nil {
		return "", fmt.Errorf("write %s: %w", tmpPath, err)
	}

	scriptPath := "/tmp/wg-monitor-swap.sh"
	script := selfUpdateSwapScript(binPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write %s: %w", scriptPath, err)
	}

	// Detached: nohup + & so the script outlives the SIGTERM we'll receive
	// when it kills the running agent. setsid keeps it free of our pgrp.
	cmd := exec.Command("sh", "-c", "nohup sh "+scriptPath+" >/dev/null 2>&1 &")
	if err := cmd.Start(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("spawn swap script: %w", err)
	}
	_ = cmd.Wait()

	return fmt.Sprintf("%s verified, swap scheduled in ~3s", version), nil
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSelfUpdateArtifactSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxSelfUpdateArtifactSize {
		return nil, fmt.Errorf("response too large: exceeds %d bytes", maxSelfUpdateArtifactSize)
	}
	return body, nil
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

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeFileAtomic writes to path+".tmp" then renames into place so a crash
// mid-write doesn't leave a half-written .new binary lurking on disk that
// the next swap-script run would happily mv into /opt/bin/wg-monitor.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
sleep 2
is_running() {
	if command -v pidof >/dev/null 2>&1 && pidof wg-monitor >/dev/null 2>&1; then
		return 0
	fi
	if command -v pgrep >/dev/null 2>&1 && pgrep -x wg-monitor >/dev/null 2>&1; then
		return 0
	fi
	ps 2>/dev/null | grep '[w]g-monitor' >/dev/null 2>&1
}
if ! is_running; then
	/opt/etc/init.d/S99wg-monitor stop 2>/dev/null
	mv ` + binPath + `.bak ` + binPath + `
	chmod 755 ` + binPath + `
	/opt/etc/init.d/S99wg-monitor start
fi
`
}
