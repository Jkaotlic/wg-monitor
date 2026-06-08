package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

func tryWizardBackendUpdate(state *State, secrets *SecretStore, version string) (bool, error) {
	if state == nil || secrets == nil || strings.TrimSpace(state.Backend.Domain) == "" {
		return false, nil
	}
	tok := secrets.GetNonInteractive("WIZARD_TOKEN")
	if strings.TrimSpace(tok) == "" {
		return false, nil
	}
	client := NewVPSClientForBackend(state, tok, 30*time.Second)
	if client == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := client.DeployBackend(ctx, version); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "HTTP 404") || strings.Contains(msg, "HTTP 405") || strings.Contains(msg, "HTTP 503") {
			return false, err
		}
		return true, err
	}
	if err := waitBackendVersion(state.Backend.Domain, version, 4*time.Minute); err != nil {
		return true, err
	}
	return true, nil
}

func waitBackendVersion(domain, want string, timeout time.Duration) error {
	base := strings.TrimRight(domain, "/")
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	url := base + "/healthz"
	deadline := time.Now().Add(timeout)
	var last string
	client := &http.Client{Timeout: 8 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			var got struct {
				Version string `json:"version"`
			}
			if resp.Body != nil {
				_ = json.NewDecoder(resp.Body).Decode(&got)
				_ = resp.Body.Close()
			}
			if resp.StatusCode == http.StatusOK && got.Version == want {
				return nil
			}
			last = fmt.Sprintf("HTTP %d version=%q", resp.StatusCode, got.Version)
		} else {
			last = err.Error()
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("backend did not report %s at %s before timeout; last=%s", want, url, last)
}

func installBackendUpdateRunner(s *SSH, state *State, plan backendSwapPlan) error {
	if s == nil || state == nil || strings.TrimSpace(plan.RemotePath) == "" {
		return nil
	}
	if strings.HasPrefix(plan.Name, "docker:") {
		return installDockerBackendUpdateRunner(s, userOrDefault(state.Backend.User, "root"), plan)
	}
	return installSystemBackendUpdateRunner(s, plan)
}

func installDockerBackendUpdateRunner(s *SSH, user string, plan backendSwapPlan) error {
	base := path.Dir(path.Dir(plan.RemotePath))
	unitDir := userHome(user) + "/.config/systemd/user"
	service := backendUpdateService("wg-monitor-backend-update.service",
		plan.RemotePath,
		base+"/config/backend.yaml",
		base+"/data/backend-update.json",
		plan.RemotePath,
		"docker restart wg-monitor-backend >/dev/null")
	timer := backendUpdateTimer("wg-monitor-backend-update.service")
	if err := stepUploadFile(s, unitDir+"/wg-monitor-backend-update.service", []byte(service), "644"); err != nil {
		return err
	}
	if err := stepUploadFile(s, unitDir+"/wg-monitor-backend-update.timer", []byte(timer), "644"); err != nil {
		return err
	}
	_, err := s.MustRun("systemctl --user daemon-reload && systemctl --user enable --now wg-monitor-backend-update.timer")
	return err
}

func installSystemBackendUpdateRunner(s *SSH, plan backendSwapPlan) error {
	service := backendUpdateService("wg-monitor-backend-update.service",
		plan.RemotePath,
		"/etc/wg-monitor/backend.yaml",
		"/var/lib/wg-monitor/backend-update.json",
		plan.RemotePath,
		"systemctl restart wg-monitor-backend")
	timer := backendUpdateTimer("wg-monitor-backend-update.service")
	if err := stepUploadFile(s, "/etc/systemd/system/wg-monitor-backend-update.service", []byte(service), "644"); err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/systemd/system/wg-monitor-backend-update.timer", []byte(timer), "644"); err != nil {
		return err
	}
	_, err := s.MustRun("systemctl daemon-reload && systemctl enable --now wg-monitor-backend-update.timer")
	return err
}

func backendUpdateService(name, binary, config, pending, targetBinary, restartCmd string) string {
	cmd := fmt.Sprintf("%s backend-update-runner --config %s --pending-file %s --binary %s --restart-cmd %q",
		shellSingleQuote(binary), shellSingleQuote(config), shellSingleQuote(pending), shellSingleQuote(targetBinary), restartCmd)
	return fmt.Sprintf(`[Unit]
Description=wg-monitor remote backend update runner

[Service]
Type=oneshot
ExecStart=/bin/sh -lc %s
`, shellSingleQuote(cmd))
}

func backendUpdateTimer(service string) string {
	return fmt.Sprintf(`[Unit]
Description=wg-monitor remote backend update poller

[Timer]
OnBootSec=30s
OnUnitActiveSec=20s
AccuracySec=5s
Unit=%s

[Install]
WantedBy=timers.target
`, service)
}

func userHome(user string) string {
	if user == "" || user == "root" {
		return "/root"
	}
	return "/home/" + user
}
