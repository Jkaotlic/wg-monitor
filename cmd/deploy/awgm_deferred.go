package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/installtmpl"
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
	ExpectedSHA      string
	ExpiresAt        time.Time
	RecoveryHint     string
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
	if strings.TrimSpace(p.ExpectedSHA) == "" {
		return awgmRelayConfig{}, fmt.Errorf("expected sha required")
	}
	kind := strings.TrimSpace(p.Agent.Kind)
	if kind == "" {
		kind = "static"
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
		ExpectedSHA:      strings.TrimSpace(p.ExpectedSHA),
		InitScript:       installtmpl.InitScript(),
		ExpiresAtUnix:    p.ExpiresAt.Unix(),
		RecoveryHint:     p.RecoveryHint,
	}, nil
}

func deferredAWGMTokenEnv(nickname string) string {
	return "WG_AGENT_TOKEN_" + strings.ToUpper(strings.TrimSpace(nickname))
}

func deferredAWGMTokenFile(nickname string) string {
	return deferredAWGMJobDir + "/" + safeRelayName(nickname) + ".json.token"
}

func deferredAWGMRecoveryHint(state *State, ag *AgentState, tokenFile, tokenEnv string) string {
	host := "<vps-host>"
	port := 22
	user := "root"
	if state != nil {
		if strings.TrimSpace(state.Backend.Host) != "" {
			host = strings.TrimSpace(state.Backend.Host)
		}
		port = portOrDefault(state.Backend.Port, 22)
		user = userOrDefault(state.Backend.User, "root")
	}
	if tokenEnv == "" && ag != nil {
		tokenEnv = deferredAWGMTokenEnv(ag.Nickname)
	}
	secretPath := secretsCachePath()
	if strings.TrimSpace(secretPath) == "" {
		secretPath = "<local secrets.env>"
	}
	copyCmd := fmt.Sprintf("ssh -p %d %s@%s \"cat %s\" | Add-Content -Encoding ascii \"%s\"",
		port, user, host, tokenFile, secretPath)
	return strings.Join([]string{
		"recovery: импортируй " + tokenEnv + " из deferred AWG token artifact",
		"PowerShell: " + copyCmd,
		"Then: wg-monitor-deploy sync-vps",
		"Then: wg-monitor-deploy doctor",
	}, "\n")
}

func printDeferredAWGMRecoveryHint(hint string) {
	for _, line := range strings.Split(hint, "\n") {
		if strings.TrimSpace(line) != "" {
			PrintInfo(line)
		}
	}
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
    tmp="$cfg.last.tmp"
    if timeout 20m python3 "$RELAY" "$cfg" >"$tmp" 2>&1; then
        rm -f "$tmp" "$cfg.last"
    else
        rc=$?
        mv -f "$tmp" "$cfg.last"
        echo "wg-monitor deferred AWGM job failed rc=$rc cfg=$cfg"
    fi
done
exit 0
`
}

type deferredAWGMStatusRow struct {
	Nickname      string `json:"nickname"`
	State         string `json:"state"`
	TargetVersion string `json:"target_version"`
	BaseURL       string `json:"base_url"`
	Reason        string `json:"reason"`
	Path          string `json:"path"`
}

func classifyDeferredAWGMFailure(reason string) string {
	s := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case s == "":
		return "pending"
	case strings.Contains(s, "certificate is valid for") ||
		strings.Contains(s, "x509:") ||
		strings.Contains(s, "hostname") && strings.Contains(s, "certificate") ||
		strings.Contains(s, "tls"):
		return "tls_error"
	case strings.Contains(s, "name or service not known") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "nxdomain") ||
		strings.Contains(s, "temporary failure in name resolution"):
		return "dns_error"
	case strings.Contains(s, "http 401") ||
		strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "http 403") ||
		strings.Contains(s, "forbidden"):
		return "auth_error"
	case strings.Contains(s, "http 502") ||
		strings.Contains(s, "http 503") ||
		strings.Contains(s, "http 504") ||
		strings.Contains(s, "service unavailable") ||
		strings.Contains(s, "gateway timeout") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connection reset"):
		return "offline"
	default:
		return "pending"
	}
}

func renderDeferredAWGMStatus(rows []deferredAWGMStatusRow) string {
	if len(rows) == 0 {
		return "no deferred AWGM jobs found"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-20s %-14s %-16s %s\n", "nickname", "state", "target", "reason")
	for _, row := range rows {
		reason := strings.TrimSpace(row.Reason)
		if reason == "" {
			reason = "-"
		}
		fmt.Fprintf(&b, "%-20s %-14s %-16s %s\n",
			emptyDash(row.Nickname),
			emptyDash(row.State),
			emptyDash(row.TargetVersion),
			reason,
		)
	}
	return strings.TrimRight(b.String(), "\n")
}

func actionDeferredAWGMStatus(state *State, secrets *SecretStore) error {
	if state == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return fmt.Errorf("backend SSH host is not configured")
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
	out, err := bs.MustRun(deferredAWGMStatusRemoteScript())
	if err != nil {
		return err
	}
	var rows []deferredAWGMStatusRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return fmt.Errorf("parse deferred AWGM status: %w: %s", err, out)
	}
	for i := range rows {
		if rows[i].State == "" || rows[i].State == "pending" {
			rows[i].State = classifyDeferredAWGMFailure(rows[i].Reason)
		}
	}
	fmt.Println(renderDeferredAWGMStatus(rows))
	return nil
}

func deferredAWGMStatusRemoteScript() string {
	return `python3 - <<'PY'
import glob
import json
import os

job_dir = "/var/lib/wg-monitor/deferred-awgm"
rows = []

def first_line(path):
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if line:
                    return line[:500]
    except OSError:
        return ""
    return ""

for path in sorted(glob.glob(os.path.join(job_dir, "*.json"))):
    try:
        with open(path, "r", encoding="utf-8") as f:
            cfg = json.load(f)
    except Exception as e:
        cfg = {}
        reason = "job json parse failed: %s" % e
    else:
        reason = first_line(path + ".last")
    rows.append({
        "nickname": cfg.get("nickname") or os.path.basename(path).removesuffix(".json"),
        "state": "pending",
        "target_version": cfg.get("target_version") or "",
        "base_url": cfg.get("base_url") or "",
        "reason": reason,
        "path": path,
    })

for path in sorted(glob.glob(os.path.join(job_dir, "*.json.done"))):
    rows.append({
        "nickname": os.path.basename(path).removesuffix(".json.done"),
        "state": "patched",
        "target_version": "",
        "base_url": "",
        "reason": first_line(path),
        "path": path,
    })

print(json.dumps(rows, ensure_ascii=False))
PY`
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

func installDeferredAWGMDeployViaVPS(state *State, secrets *SecretStore, ag *AgentState, apiKey, login, pass, terminalUser, terminalPass string, rel *Release, wizardToken, expectedSHA string) error {
	if state == nil || ag == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return fmt.Errorf("backend SSH host is not configured; install backend first")
	}
	backendURL := strings.TrimRight("https://"+strings.TrimSpace(state.Backend.Domain), "/")
	tokenEnv := deferredAWGMTokenEnv(ag.Nickname)
	tokenFile := deferredAWGMTokenFile(ag.Nickname)
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
		ExpectedSHA:      expectedSHA,
		ExpiresAt:        time.Now().UTC().Add(7 * 24 * time.Hour),
		RecoveryHint:     deferredAWGMRecoveryHint(state, ag, tokenFile, tokenEnv),
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
	tmpJobPath := jobPath + ".tmp"
	if err := bs.UploadStdin(tmpJobPath, cfgBytes); err != nil {
		return fmt.Errorf("upload deferred awgm job: %w", err)
	}
	if err := bs.UploadStdin("/etc/systemd/system/wg-monitor-deferred-awgm.service", []byte(renderDeferredAWGMService())); err != nil {
		return fmt.Errorf("upload deferred awgm service: %w", err)
	}
	if err := bs.UploadStdin("/etc/systemd/system/wg-monitor-deferred-awgm.timer", []byte(renderDeferredAWGMTimer())); err != nil {
		return fmt.Errorf("upload deferred awgm timer: %w", err)
	}
	chmod := "chmod 700 " + shellSingleQuote(deferredAWGMRelayPath) + " " + shellSingleQuote(deferredAWGMRunnerPath) +
		" && chmod 600 " + shellSingleQuote(tmpJobPath) +
		" && mv -f " + shellSingleQuote(tmpJobPath) + " " + shellSingleQuote(jobPath) +
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
	PrintInfo("Для одного router nickname хранится один job; повторная постановка обновит/заменит предыдущую.")
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
	arch, err := normalizeKeeneticArch(ag.Arch)
	if err != nil {
		return true, err
	}
	assetName := "wg-monitor-agent-linux-" + arch
	sums := rel.AssetByName("checksums.txt")
	if sums == nil {
		return true, fmt.Errorf("release %s has no checksums.txt", rel.TagName)
	}
	checksumURL := releaseAssetURLForRouter(state, rel.TagName, "checksums.txt", sums.DownloadURL)
	expectedSHA, err := dl.fetchExpectedSha(checksumURL, assetName, rel.TagName)
	if err != nil {
		return true, fmt.Errorf("verify release checksums for %s: %w", assetName, err)
	}
	if err := installDeferredAWGMDeployViaVPSFunc(state, secrets, ag, apiKey, login, pass, terminalUser, terminalPass, rel, wizardToken, expectedSHA); err != nil {
		return true, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	ag.DeployMode = "awgm"
	ag.AWGMAuth = authMode
	ag.PendingVersion = rel.TagName
	ag.PendingSince = now
	tokenEnv := deferredAWGMTokenEnv(ag.Nickname)
	tokenFile := deferredAWGMTokenFile(ag.Nickname)
	PrintOK("отложенный деплой поставлен на VPS: " + ag.Nickname + " → " + rel.TagName)
	PrintInfo("статус на VPS: systemctl status wg-monitor-deferred-awgm.timer; job лежит в " + deferredAWGMJobDir)
	PrintInfo("после успеха runner оставит " + tokenFile + " (" + tokenEnv + ")")
	printDeferredAWGMRecoveryHint(deferredAWGMRecoveryHint(state, ag, tokenFile, tokenEnv))
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
