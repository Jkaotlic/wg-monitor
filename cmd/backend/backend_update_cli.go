package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
	"github.com/Jkaotlic/wg-monitor/internal/releasesig"
)

var newBackendUpdatePinnedHTTPClient = backendUpdatePinnedHTTPClient

var backendUpdateAllowedRepoBases = []string{
	releaseorigin.DefaultGitHubReleaseBase,
	releaseorigin.DefaultBackendMirrorBase,
}

const (
	maxBackendUpdateChecksumsSize = 1 << 20
	maxBackendUpdateSignatureSize = 4 << 10
	maxBackendUpdateArtifactSize  = 64 << 20
)

type backendUpdateRunnerOptions struct {
	ConfigPath  string
	PendingFile string
	BinaryPath  string
	RestartCmd  string
}

type backendUpdatePending struct {
	TargetVersion string `json:"target_version"`
	RepoBase      string `json:"repo_base"`
	RepoResolveIP string `json:"repo_resolve_ip,omitempty"`
	RequestedAt   string `json:"requested_at"`
}

func runBackendUpdateRunnerCommand(args []string) error {
	fs := flag.NewFlagSet("backend-update-runner", flag.ContinueOnError)
	opts := backendUpdateRunnerOptions{}
	fs.StringVar(&opts.ConfigPath, "config", "/etc/wg-monitor/backend.yaml", "backend config path")
	fs.StringVar(&opts.PendingFile, "pending-file", "", "pending backend update JSON path")
	fs.StringVar(&opts.BinaryPath, "binary", "", "backend binary path to replace")
	fs.StringVar(&opts.RestartCmd, "restart-cmd", "", "shell command to restart backend after swap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if opts.PendingFile == "" {
		cfg, err := backend.LoadConfig(opts.ConfigPath)
		if err != nil {
			return err
		}
		opts.PendingFile = backend.DefaultBackendUpdatePath(cfg)
	}
	if opts.BinaryPath == "" {
		return fmt.Errorf("--binary is required")
	}
	return runBackendUpdateRunner(opts)
}

func runBackendUpdateRunner(opts backendUpdateRunnerOptions) error {
	body, err := os.ReadFile(opts.PendingFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var req backendUpdatePending
	if err := json.Unmarshal(body, &req); err != nil {
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	req.TargetVersion = strings.TrimSpace(req.TargetVersion)
	req.RepoBase = strings.TrimRight(strings.TrimSpace(req.RepoBase), "/")
	if req.TargetVersion == "" {
		err := fmt.Errorf("pending update requires target_version")
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	if req.RepoBase == "" {
		err := fmt.Errorf("pending update requires repo_base")
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	req.TargetVersion, err = releaseorigin.ValidateReleaseTag(req.TargetVersion)
	if err != nil {
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	req.RepoBase, err = releaseorigin.ValidateRepoBase(req.RepoBase, backendUpdateAllowedRepoBases)
	if err != nil {
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	asset := "wg-monitor-backend-linux-" + runtime.GOARCH
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	primaryClient := http.DefaultClient
	var fallbackClient *http.Client
	var fallbackLabel string
	if strings.TrimSpace(req.RepoResolveIP) != "" {
		fallbackClient, fallbackLabel = newBackendUpdatePinnedHTTPClient(req.RepoBase, strings.TrimSpace(req.RepoResolveIP), 90, nil)
	}
	checks, err := httpGetBytesLimitedWithFallback(ctx, primaryClient, fallbackClient, req.RepoBase+"/"+req.TargetVersion+"/checksums.txt", fallbackLabel, maxBackendUpdateChecksumsSize)
	if err != nil {
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	if releasesig.SignatureRequiredForVersion(req.TargetVersion) {
		sig, err := httpGetBytesLimitedWithFallback(ctx, primaryClient, fallbackClient, req.RepoBase+"/"+req.TargetVersion+"/checksums.txt.sig", fallbackLabel, maxBackendUpdateSignatureSize)
		if err != nil {
			err = fmt.Errorf("download checksums.txt signature: %w", err)
			_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
			return err
		}
		if err := releasesig.VerifyChecksumsSignature(checks, sig); err != nil {
			err = fmt.Errorf("verify checksums.txt signature: %w", err)
			_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
			return err
		}
	}
	want, err := checksumForAsset(string(checks), asset)
	if err != nil {
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	bin, err := httpGetBytesLimitedWithFallback(ctx, primaryClient, fallbackClient, req.RepoBase+"/"+req.TargetVersion+"/"+asset, fallbackLabel, maxBackendUpdateArtifactSize)
	if err != nil {
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	gotSum := sha256.Sum256(bin)
	got := hex.EncodeToString(gotSum[:])
	if got != want {
		err := fmt.Errorf("sha256 mismatch for %s: got %s want %s", asset, got, want)
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	if err := swapBackendBinary(opts.BinaryPath, bin); err != nil {
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	if strings.TrimSpace(opts.RestartCmd) != "" {
		if err := runRestartCommand(opts.RestartCmd); err != nil {
			_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
			return err
		}
	}
	if err := os.Remove(opts.PendingFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "ok", "")
}

func httpGetBytes(ctx context.Context, url string) ([]byte, error) {
	return httpGetBytesLimited(ctx, url, maxBackendUpdateArtifactSize)
}

func httpGetBytesLimited(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	return httpGetBytesLimitedWithClient(ctx, http.DefaultClient, url, maxBytes)
}

func httpGetBytesLimitedWithClient(ctx context.Context, c *http.Client, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("GET %s: response too large: exceeds %d bytes", url, maxBytes)
	}
	return body, nil
}

func httpGetBytesLimitedWithFallback(ctx context.Context, primary, fallback *http.Client, url, fallbackLabel string, maxBytes int64) ([]byte, error) {
	body, err := httpGetBytesLimitedWithClient(ctx, primary, url, maxBytes)
	if err == nil {
		return body, nil
	}
	if fallback == nil || strings.TrimSpace(fallbackLabel) == "" || !backendUpdateTransportError(err) {
		return nil, err
	}
	body, fallbackErr := httpGetBytesLimitedWithClient(ctx, fallback, url, maxBytes)
	if fallbackErr != nil {
		return nil, fmt.Errorf("%w; retry via %s failed: %v", err, fallbackLabel, fallbackErr)
	}
	return body, nil
}

func backendUpdateTransportError(err error) bool {
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

func backendUpdatePinnedHTTPClient(repoBase, resolveIP string, timeoutSec int, dial func(network, addr string) (net.Conn, error)) (*http.Client, string) {
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
		timeoutSec = 90
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

func checksumForAsset(checksums, asset string) (string, error) {
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt missing %s", asset)
}

func swapBackendBinary(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	bak := path + ".bak"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(bak)
		if err := os.Rename(path, bak); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		if _, statErr := os.Stat(bak); statErr == nil {
			_ = os.Rename(bak, path)
		}
		return err
	}
	return nil
}

func runRestartCommand(cmd string) error {
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		// #nosec G204 -- RestartCmd is a trusted local CLI/systemd flag, not network payload.
		c = exec.Command("cmd", "/C", cmd)
	} else {
		// #nosec G204 -- RestartCmd is a trusted local CLI/systemd flag, not network payload.
		c = exec.Command("sh", "-c", cmd)
	}
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart command failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeBackendUpdateStatus(pendingPath, version, status, message string) error {
	body, err := json.MarshalIndent(struct {
		TargetVersion string `json:"target_version"`
		Status        string `json:"status"`
		Message       string `json:"message,omitempty"`
		UpdatedAt     string `json:"updated_at"`
	}{TargetVersion: version, Status: status, Message: message, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pendingPath+".last", append(body, '\n'), 0o600)
}
