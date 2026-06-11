package actions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// UpdateBackendURL rewrites backend.url in the agent config file and triggers
// a service restart. The restart is scheduled in a background goroutine so the
// caller (runner) can post the command result before the process is replaced.
//
// Intended use: operator pushes "update_backend_url" command when the backend
// domain changes; each connected agent self-migrates without manual SSH/AWG
// Manager intervention.
func UpdateBackendURL(ctx context.Context, newURL, configPath string) (string, error) {
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

// rewriteBackendURL updates the `url:` line inside the `backend:` section of a
// YAML config file. It uses line-by-line replacement so comments and formatting
// in other sections are preserved. The write is atomic (temp file + rename).
func rewriteBackendURL(configPath, newURL string) error {
	newURL = strings.TrimSpace(newURL)
	if newURL == "" {
		return fmt.Errorf("update_backend_url: new URL is empty")
	}
	if !strings.HasPrefix(newURL, "https://") {
		return fmt.Errorf("update_backend_url: URL must start with https://, got %q", newURL)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("update_backend_url: read config: %w", err)
	}

	updated, err := substituteBackendURL(string(raw), newURL)
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
