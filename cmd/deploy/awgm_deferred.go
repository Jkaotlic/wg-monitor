package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	deferredAWGMJobDir     = "/var/lib/wg-monitor/deferred-awgm"
	deferredAWGMLibDir     = "/usr/local/lib/wg-monitor"
	deferredAWGMRelayPath  = deferredAWGMLibDir + "/awgm-relay.py"
	deferredAWGMRunnerPath = "/usr/local/bin/wg-monitor-deferred-awgm-runner"
)

type deferredAWGMConfigParams struct {
	Agent            AgentState
	APIKey           string
	Login            string
	Password         string
	TerminalUser     string
	TerminalPassword string
	BackendURL       string
	WizardToken      string
	Release          *Release
	ExpiresAt        time.Time
}

func buildDeferredAWGMConfig(p deferredAWGMConfigParams) (awgmRelayConfig, error) {
	if p.Release == nil || strings.TrimSpace(p.Release.TagName) == "" {
		return awgmRelayConfig{}, fmt.Errorf("release tag required")
	}
	if strings.TrimSpace(p.Agent.Nickname) == "" || strings.TrimSpace(p.Agent.AWGMURL) == "" {
		return awgmRelayConfig{}, fmt.Errorf("nickname and AWG Manager URL are required")
	}
	backendURL := strings.TrimRight(strings.TrimSpace(p.BackendURL), "/")
	if backendURL == "" || strings.TrimSpace(p.WizardToken) == "" {
		return awgmRelayConfig{}, fmt.Errorf("backend URL and wizard token are required")
	}
	kind := strings.TrimSpace(p.Agent.Kind)
	if kind == "" {
		kind = "static"
	}
	initScript, err := ReadStaticTemplate("S99wg-monitor")
	if err != nil {
		return awgmRelayConfig{}, err
	}
	return awgmRelayConfig{
		BaseURL:          normalizeAWGMURL(p.Agent.AWGMURL),
		APIKey:           p.APIKey,
		Login:            p.Login,
		Password:         p.Password,
		TerminalUser:     userOrDefault(p.TerminalUser, "root"),
		TerminalPassword: p.TerminalPassword,
		Mode:             "deferred_bootstrap",
		Nickname:         p.Agent.Nickname,
		Kind:             kind,
		ThreadID:         int64(p.Agent.ThreadID),
		BackendURL:       backendURL,
		WizardToken:      p.WizardToken,
		TargetVersion:    p.Release.TagName,
		ReleaseBase:      backendURL + "/v1/releases/download",
		InitScript:       strings.TrimRight(string(initScript), "\n"),
		ExpiresAtUnix:    p.ExpiresAt.Unix(),
	}, nil
}

func renderDeferredAWGMRunnerScript() string {
	return `#!/bin/sh
set -u

JOB_DIR=/var/lib/wg-monitor/deferred-awgm
RELAY=/usr/local/lib/wg-monitor/awgm-relay.py

[ -d "$JOB_DIR" ] || exit 0
[ -x "$RELAY" ] || exit 0

for cfg in /var/lib/wg-monitor/deferred-awgm/*.json; do
    [ -e "$cfg" ] || exit 0
    timeout 20m python3 "$RELAY" "$cfg" || echo "wg-monitor deferred AWGM job failed rc=$? cfg=$cfg"
done
exit 0
`
}

func renderDeferredAWGMService() string {
	return `[Unit]
Description=wg-monitor deferred AWG Manager deploy runner
After=network-online.target wg-monitor-backend.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/wg-monitor-deferred-awgm-runner
`
}

func renderDeferredAWGMTimer() string {
	return `[Unit]
Description=Run wg-monitor deferred AWG Manager deploy jobs

[Timer]
OnBootSec=1min
OnUnitActiveSec=2min
AccuracySec=30s
Persistent=true

[Install]
WantedBy=timers.target
`
}

func installDeferredAWGMDeployViaVPS(state *State, secrets *SecretStore, ag *AgentState, apiKey, login, pass, terminalUser, terminalPass string, rel *Release, wizardToken string) error {
	if state == nil || ag == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return fmt.Errorf("backend SSH host is not configured; install backend first")
	}
	backendURL := strings.TrimRight("https://"+strings.TrimSpace(state.Backend.Domain), "/")
	cfg, err := buildDeferredAWGMConfig(deferredAWGMConfigParams{
		Agent:            *ag,
		APIKey:           apiKey,
		Login:            login,
		Password:         pass,
		TerminalUser:     terminalUser,
		TerminalPassword: terminalPass,
		BackendURL:       backendURL,
		WizardToken:      wizardToken,
		Release:          rel,
		ExpiresAt:        time.Now().UTC().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		return err
	}
	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	bs, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return err
	}
	defer bs.Close()

	if _, err := bs.MustRun("install -d -m 700 " + shellSingleQuote(deferredAWGMJobDir) + " " + shellSingleQuote(deferredAWGMLibDir)); err != nil {
		return err
	}
	if err := bs.UploadStdin(deferredAWGMRelayPath, []byte(awgmVPSRelayPython)); err != nil {
		return fmt.Errorf("upload deferred awgm relay: %w", err)
	}
	if err := bs.UploadStdin(deferredAWGMRunnerPath, []byte(renderDeferredAWGMRunnerScript())); err != nil {
		return fmt.Errorf("upload deferred awgm runner: %w", err)
	}
	jobPath := deferredAWGMJobDir + "/" + safeRelayName(ag.Nickname) + ".json"
	if err := bs.UploadStdin(jobPath, cfgBytes); err != nil {
		return fmt.Errorf("upload deferred awgm job: %w", err)
	}
	if err := bs.UploadStdin("/etc/systemd/system/wg-monitor-deferred-awgm.service", []byte(renderDeferredAWGMService())); err != nil {
		return fmt.Errorf("upload deferred awgm service: %w", err)
	}
	if err := bs.UploadStdin("/etc/systemd/system/wg-monitor-deferred-awgm.timer", []byte(renderDeferredAWGMTimer())); err != nil {
		return fmt.Errorf("upload deferred awgm timer: %w", err)
	}
	chmod := "chmod 700 " + shellSingleQuote(deferredAWGMRelayPath) + " " + shellSingleQuote(deferredAWGMRunnerPath) +
		" && chmod 600 " + shellSingleQuote(jobPath) +
		" && chmod 644 /etc/systemd/system/wg-monitor-deferred-awgm.service /etc/systemd/system/wg-monitor-deferred-awgm.timer" +
		" && systemctl daemon-reload && systemctl enable --now wg-monitor-deferred-awgm.timer && systemctl start wg-monitor-deferred-awgm.service"
	if _, err := bs.MustRun(chmod); err != nil {
		return err
	}
	return nil
}

var installDeferredAWGMDeployViaVPSFunc = installDeferredAWGMDeployViaVPS

func scheduleDeferredAWGMDeployIfWanted(state *State, secrets *SecretStore, dl *Downloader, ag *AgentState, apiKey, login, pass, terminalUser, terminalPass, wizardToken, authMode string, cause error) (bool, error) {
	if !shouldRunAWGMBootstrapViaVPS(state, ag.AWGMURL) || !isAWGMTransientUnavailable(cause) {
		return false, nil
	}
	PrintWarn("AWG Manager сейчас недоступен с VPS: " + shortAWGMErrorForOperator(cause))
	PrintInfo("Можно поставить отложенный деплой: VPS будет сам проверять AWG Manager и установит агента, когда роутер проснётся.")
	if os.Getenv("WG_YES_TO_ALL") != "1" && !Confirm("Поставить отложенный деплой на VPS?", true) {
		return false, nil
	}
	if dl == nil {
		dl = NewDownloader()
	}
	rel, err := dl.GetLatestRelease()
	if err != nil {
		return true, fmt.Errorf("latest release for deferred deploy: %w", err)
	}
	if err := installDeferredAWGMDeployViaVPSFunc(state, secrets, ag, apiKey, login, pass, terminalUser, terminalPass, rel, wizardToken); err != nil {
		return true, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	ag.DeployMode = "awgm"
	ag.AWGMAuth = authMode
	ag.PendingVersion = rel.TagName
	ag.PendingSince = now
	PrintOK("отложенный деплой поставлен на VPS: " + ag.Nickname + " → " + rel.TagName)
	PrintInfo("статус на VPS: systemctl status wg-monitor-deferred-awgm.timer; job лежит в " + deferredAWGMJobDir)
	return true, nil
}

func isAWGMTransientUnavailable(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "http 401") || strings.Contains(s, "unauthorized") {
		return false
	}
	for _, needle := range []string{
		"http 502",
		"http 503",
		"http 504",
		"bad gateway",
		"service unavailable",
		"gateway timeout",
		"timeout",
		"deadline exceeded",
		"connection refused",
		"connection reset",
		"connection aborted",
		"websocket closed",
		"name or service not known",
		"no such host",
		"network is unreachable",
		"no route to host",
		"tls handshake timeout",
		"temporary failure",
		"temporary failure in name resolution",
		"urlopen error",
		"failed rc=75",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
