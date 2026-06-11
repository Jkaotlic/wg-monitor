package actions

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// UpdateBackendURL rewrites backend.url in the agent config file and triggers
// a service restart. The restart is scheduled in a background goroutine so the
// caller (runner) can post the command result before the process is replaced.
//
// Intended use: operator pushes "update_backend_url" command when the backend
// domain changes; each connected agent self-migrates without manual SSH/AWG
// Manager intervention.
func UpdateBackendURL(ctx context.Context, newURL, configPath string) (string, error) {
	if err := backendURLHealthCheck(ctx, newURL); err != nil {
		return "", fmt.Errorf("update_backend_url: health check failed: %w", err)
	}
	if err := rewriteBackendURL(configPath, newURL); err != nil {
		return "", err
	}
	scheduleURLUpdateRestart()
	return "backend.url updated to " + newURL + "; restarting", nil
}

// scheduleURLUpdateRestart spawns a detached shell that restarts the agent
// service after a short delay, giving the runner time to post the command
// result before the process is replaced.
var scheduleURLUpdateRestart = func() {
	cmd := exec.Command("sh", "-c", "sleep 2 && /opt/etc/init.d/S99wg-monitor restart >/dev/null 2>&1")
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Start()
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
}

var backendURLHealthCheck = func(ctx context.Context, rawURL string) error {
	normalized, err := validateBackendURL(rawURL)
	if err != nil {
		return err
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/healthz"
	u.RawQuery = ""
	u.Fragment = ""
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s: HTTP %d", u.String(), resp.StatusCode)
	}
	return nil
}

// rewriteBackendURL updates the `url:` line inside the `backend:` section of a
// YAML config file. It uses line-by-line replacement so comments and formatting
// in other sections are preserved. The write is atomic (temp file + rename).
func rewriteBackendURL(configPath, newURL string) error {
	normalizedURL, err := validateBackendURL(newURL)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("update_backend_url: read config: %w", err)
	}

	updated, err := substituteBackendURL(string(raw), normalizedURL)
	if err != nil {
		return err
	}

	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0600); err != nil {
		return fmt.Errorf("update_backend_url: write temp: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("update_backend_url: rename: %w", err)
	}
	return nil
}

func validateBackendURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", fmt.Errorf("update_backend_url: new URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("update_backend_url: invalid URL %q", raw)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("update_backend_url: URL must start with https://, got %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return "", fmt.Errorf("update_backend_url: backend URL must not use localhost")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return "", fmt.Errorf("update_backend_url: backend URL must not use private or loopback IP")
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String(), nil
}

// substituteBackendURL finds the `url:` line inside the `backend:` section and
// replaces its value. Returns an error if the line is not found.
func substituteBackendURL(content, newURL string) (string, error) {
	lines := strings.Split(content, "\n")
	inBackend := false
	replaced := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track top-level section changes (lines that start without leading spaces
		// and end with ':'). This detects when we leave the backend section.
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' {
			inBackend = strings.TrimSuffix(trimmed, ":") == "backend" && strings.HasSuffix(trimmed, ":")
		}

		if inBackend && !replaced {
			// Match "  url: <value>" — indented url line in backend section.
			stripped := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(stripped, "url:") {
				indent := line[:len(line)-len(stripped)]
				lines[i] = indent + "url: " + newURL
				replaced = true
			}
		}
	}

	if !replaced {
		return "", fmt.Errorf("update_backend_url: backend.url line not found in config")
	}
	return strings.Join(lines, "\n"), nil
}
