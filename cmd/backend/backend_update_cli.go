package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
)

var backendUpdateAllowedRepoBases = []string{
	releaseorigin.DefaultGitHubReleaseBase,
	releaseorigin.DefaultBackendMirrorBase,
}

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
	if req.TargetVersion == "" || req.RepoBase == "" {
		err := fmt.Errorf("pending update requires target_version and repo_base")
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
	checks, err := httpGetBytes(ctx, req.RepoBase+"/"+req.TargetVersion+"/checksums.txt")
	if err != nil {
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	want, err := checksumForAsset(string(checks), asset)
	if err != nil {
		_ = writeBackendUpdateStatus(opts.PendingFile, req.TargetVersion, "err", err.Error())
		return err
	}
	bin, err := httpGetBytes(ctx, req.RepoBase+"/"+req.TargetVersion+"/"+asset)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
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
		c = exec.Command("cmd", "/C", cmd)
	} else {
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
