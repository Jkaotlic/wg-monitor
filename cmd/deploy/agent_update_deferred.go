package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	deferredAgentUpdateJobDir     = "/var/lib/wg-monitor/deferred-agent-update"
	deferredAgentUpdateRunnerPath = "/usr/local/bin/wg-monitor-deferred-agent-update-runner"
)

type deferredAgentUpdateConfigParams struct {
	Agent         AgentState
	BackendURL    string
	WizardToken   string
	TargetVersion string
	ExpiresAt     time.Time
}

type deferredAgentUpdateConfig struct {
	Nickname       string `json:"nickname"`
	Kind           string `json:"kind,omitempty"`
	BackendURL     string `json:"backend_url"`
	WizardToken    string `json:"wizard_token"`
	TargetVersion  string `json:"target_version"`
	ExpiresAtUnix  int64  `json:"expires_at_unix"`
	EnqueuedAtUnix int64  `json:"enqueued_at_unix,omitempty"`
}

func buildDeferredAgentUpdateConfig(p deferredAgentUpdateConfigParams) (deferredAgentUpdateConfig, error) {
	nick := strings.TrimSpace(p.Agent.Nickname)
	target := strings.TrimSpace(p.TargetVersion)
	backendURL := strings.TrimRight(strings.TrimSpace(p.BackendURL), "/")
	if nick == "" || target == "" {
		return deferredAgentUpdateConfig{}, fmt.Errorf("nickname and target version are required")
	}
	if backendURL == "" || strings.TrimSpace(p.WizardToken) == "" {
		return deferredAgentUpdateConfig{}, fmt.Errorf("backend URL and wizard token are required")
	}
	kind := strings.TrimSpace(p.Agent.Kind)
	if kind == "" {
		kind = "static"
	}
	return deferredAgentUpdateConfig{
		Nickname:      nick,
		Kind:          kind,
		BackendURL:    backendURL,
		WizardToken:   strings.TrimSpace(p.WizardToken),
		TargetVersion: target,
		ExpiresAtUnix: p.ExpiresAt.Unix(),
	}, nil
}

func renderDeferredAgentUpdateRunnerScript() string {
	return `#!/usr/bin/env python3
import datetime as dt
import glob
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

JOB_DIR = "/var/lib/wg-monitor/deferred-agent-update"
FRESH_SECONDS = 5 * 60
REENQUEUE_SECONDS = 45 * 60
FUTURE_SKEW_SECONDS = 2 * 60

def log(msg):
    print("wg-monitor deferred-agent-update: " + msg, flush=True)

def load_json(path):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)

def save_json(path, data):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")
    os.replace(tmp, path)

def request_json(method, url, token, payload=None):
    body = None
    headers = {"Authorization": "Bearer " + token}
    if payload is not None:
        body = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            raw = resp.read().decode("utf-8", "replace")
            if not raw:
                return resp.status, {}
            return resp.status, json.loads(raw)
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8", "replace")
        return e.code, {"error": raw}

def parse_time(value):
    if not value:
        return None
    value = value.replace("Z", "+00:00")
    try:
        parsed = dt.datetime.fromisoformat(value)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    return parsed.astimezone(dt.timezone.utc)

def heartbeat_fresh(agent, now):
    ts = parse_time(agent.get("last_seen_at"))
    if ts is None:
        return False
    age = (now - ts).total_seconds()
    return age <= FRESH_SECONDS and age >= -FUTURE_SKEW_SECONDS

def process_job(path):
    cfg = load_json(path)
    now_unix = int(time.time())
    if int(cfg.get("expires_at_unix") or 0) <= now_unix:
        log("expired " + path + ", removing")
        os.remove(path)
        return

    base = str(cfg["backend_url"]).rstrip("/")
    token = str(cfg["wizard_token"])
    nick = str(cfg["nickname"])
    target = str(cfg["target_version"])

    status, payload = request_json("GET", base + "/v1/wizard/agents", token)
    if status != 200:
        log("GET agents failed status=%s job=%s" % (status, path))
        return

    agent = None
    for item in payload.get("agents", []):
        if item.get("nickname") == nick:
            agent = item
            break
    if agent is None:
        log("agent %s not found yet" % nick)
        return

    if agent.get("last_deployed_version") == target:
        log("agent %s confirmed %s, removing job" % (nick, target))
        os.remove(path)
        return

    if not heartbeat_fresh(agent, dt.datetime.now(dt.timezone.utc)):
        log("agent %s heartbeat is not fresh yet" % nick)
        return

    enqueued_at = int(cfg.get("enqueued_at_unix") or 0)
    if enqueued_at and now_unix - enqueued_at < REENQUEUE_SECONDS:
        log("agent %s already has a recent pending enqueue" % nick)
        return

    deploy_url = base + "/v1/wizard/agents/" + urllib.parse.quote(nick, safe="") + "/deploy"
    status, payload = request_json("POST", deploy_url, token, {"target_version": target})
    if status != 202:
        log("deploy enqueue failed for %s status=%s payload=%s" % (nick, status, payload))
        return
    cfg["enqueued_at_unix"] = now_unix
    save_json(path, cfg)
    log("enqueued self_update for %s -> %s" % (nick, target))

def main():
    os.makedirs(JOB_DIR, mode=0o700, exist_ok=True)
    for path in glob.glob(JOB_DIR + "/*.json"):
        try:
            process_job(path)
        except Exception as exc:
            log("job failed %s: %s" % (path, exc))
    return 0

if __name__ == "__main__":
    sys.exit(main())
`
}

func renderDeferredAgentUpdateService() string {
	return `[Unit]
Description=wg-monitor deferred agent update runner
After=network-online.target wg-monitor-backend.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/wg-monitor-deferred-agent-update-runner
`
}

func renderDeferredAgentUpdateTimer() string {
	return `[Unit]
Description=Run wg-monitor deferred agent update jobs

[Timer]
OnBootSec=1min
OnUnitActiveSec=2min
AccuracySec=30s
Persistent=true

[Install]
WantedBy=timers.target
`
}

func deferredAgentUpdateBackendURL(state *State) string {
	if state == nil || strings.TrimSpace(state.Backend.Domain) == "" {
		return ""
	}
	raw := strings.TrimRight(strings.TrimSpace(state.Backend.Domain), "/")
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return ""
		}
		return u.Scheme + "://" + u.Host
	}
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[:i]
	}
	return "https://" + raw
}

func installDeferredAgentUpdateViaVPS(state *State, secrets *SecretStore, ag *AgentState, t updateTarget, wizardToken string) error {
	if state == nil || ag == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return fmt.Errorf("backend SSH host is not configured; install backend first")
	}
	cfg, err := buildDeferredAgentUpdateConfig(deferredAgentUpdateConfigParams{
		Agent:         *ag,
		BackendURL:    deferredAgentUpdateBackendURL(state),
		WizardToken:   wizardToken,
		TargetVersion: t.LatestVersion,
		ExpiresAt:     time.Now().UTC().Add(7 * 24 * time.Hour),
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

	if _, err := bs.MustRun("install -d -m 700 " + shellSingleQuote(deferredAgentUpdateJobDir)); err != nil {
		return err
	}
	if err := bs.UploadStdin(deferredAgentUpdateRunnerPath, []byte(renderDeferredAgentUpdateRunnerScript())); err != nil {
		return fmt.Errorf("upload deferred agent update runner: %w", err)
	}
	jobPath := deferredAgentUpdateJobDir + "/" + safeRelayName(ag.Nickname) + ".json"
	if err := bs.UploadStdin(jobPath, cfgBytes); err != nil {
		return fmt.Errorf("upload deferred agent update job: %w", err)
	}
	if err := bs.UploadStdin("/etc/systemd/system/wg-monitor-deferred-agent-update.service", []byte(renderDeferredAgentUpdateService())); err != nil {
		return fmt.Errorf("upload deferred agent update service: %w", err)
	}
	if err := bs.UploadStdin("/etc/systemd/system/wg-monitor-deferred-agent-update.timer", []byte(renderDeferredAgentUpdateTimer())); err != nil {
		return fmt.Errorf("upload deferred agent update timer: %w", err)
	}
	chmod := "chmod 700 " + shellSingleQuote(deferredAgentUpdateRunnerPath) +
		" && chmod 600 " + shellSingleQuote(jobPath) +
		" && chmod 644 /etc/systemd/system/wg-monitor-deferred-agent-update.service /etc/systemd/system/wg-monitor-deferred-agent-update.timer" +
		" && systemctl daemon-reload && systemctl enable --now wg-monitor-deferred-agent-update.timer && systemctl start wg-monitor-deferred-agent-update.service"
	if _, err := bs.MustRun(chmod); err != nil {
		return err
	}
	return nil
}

func canDeferAgentUpdateAfterReadinessError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "pull deploy preflight") {
		return false
	}
	for _, marker := range []string{
		"vps heartbeat stale",
		"vps heartbeat never",
		"не забирает команды",
		"не найден",
		"stale",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func scheduleDeferredAgentUpdateIfWanted(state *State, secrets *SecretStore, c *VPSClient, t updateTarget, cause error) (bool, error) {
	if !t.IsAgent || strings.EqualFold(t.Kind, "mobile") || !canDeferAgentUpdateAfterReadinessError(cause) {
		return false, nil
	}
	if state == nil {
		return false, fmt.Errorf("state unavailable")
	}
	ag := state.FindAgent(t.AgentNickname)
	if ag == nil {
		return false, fmt.Errorf("agent %s not found in local state", t.AgentNickname)
	}
	wizardToken := ""
	if secrets != nil {
		wizardToken = secrets.GetNonInteractive("WIZARD_TOKEN")
	}
	if strings.TrimSpace(wizardToken) == "" || deferredAgentUpdateBackendURL(state) == "" {
		return false, nil
	}

	PrintWarn("agent is not ready for immediate pull update: " + cause.Error())
	PrintInfo("Можно поставить ждущее обновление на VPS: он дождётся свежего heartbeat от агента и сам отправит self_update.")
	if os.Getenv("WG_YES_TO_ALL") != "1" && !Confirm("Поставить ждущее обновление агента на VPS?", true) {
		return false, nil
	}
	if err := installDeferredAgentUpdateViaVPS(state, secrets, ag, t, wizardToken); err != nil {
		return true, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	ag.PendingVersion = t.LatestVersion
	ag.PendingSince = now
	if c == nil {
		c = NewResilientVPSClientForBackend(state, secrets, wizardToken, 8*time.Second)
	}
	if c != nil {
		pushCtx, pushCancel := context.WithTimeout(context.Background(), 25*time.Second)
		if pushErr := c.PushAgent(pushCtx, AgentStateToRemote(*ag)); pushErr != nil {
			PrintWarn("VPS pending sync failed: " + pushErr.Error())
		}
		pushCancel()
	}
	PrintOK("ждущее обновление поставлено на VPS: " + t.AgentNickname + " -> " + t.LatestVersion)
	PrintInfo("статус на VPS: systemctl status wg-monitor-deferred-agent-update.timer; job лежит в " + deferredAgentUpdateJobDir)
	return true, nil
}
