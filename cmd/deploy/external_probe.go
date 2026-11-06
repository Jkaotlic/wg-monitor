package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	externalProbeAlias       = "external-probe"
	externalProbeScriptPath  = "/usr/local/bin/wg-monitor-probe"
	externalProbeEnvPath     = "/etc/wg-monitor-probe.env"
	externalProbeServicePath = "/etc/systemd/system/wg-monitor-probe.service"
	externalProbeTimerPath   = "/etc/systemd/system/wg-monitor-probe.timer"
)

type externalProbeFiles struct {
	Script  []byte
	Env     []byte
	Service []byte
	Timer   []byte
}

func validateExternalProbeURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "/readyz" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("probe URL must be exactly https://<host>/readyz")
	}
	return nil
}

func systemdEnvValue(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", fmt.Errorf("secret contains a forbidden newline or NUL")
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`, nil
}

func renderExternalProbeFiles(readyURL, token, chat string) (externalProbeFiles, error) {
	if err := validateExternalProbeURL(readyURL); err != nil {
		return externalProbeFiles{}, err
	}
	tokenValue, err := systemdEnvValue(token)
	if err != nil {
		return externalProbeFiles{}, err
	}
	chatValue, err := systemdEnvValue(chat)
	if err != nil {
		return externalProbeFiles{}, err
	}
	if token == "" || chat == "" {
		return externalProbeFiles{}, fmt.Errorf("independent notification token and chat are required")
	}

	script := `#!/bin/sh
set -eu
URL=` + shellSingleQuote(readyURL) + `
STATE=${WG_PROBE_STATE:-/var/lib/wg-monitor-probe/state}
TMP="${STATE}.tmp.$$"

write_state() {
  umask 077
  printf '%s %s\n' "$1" "$2" > "$TMP"
  mv -f "$TMP" "$STATE"
}

notify() {
  curl -fsS --max-time 5 --retry 0 "https://api.telegram.org/bot${TG_TOKEN}/sendMessage" \
    --data-urlencode "chat_id=${TG_CHAT}" --data-urlencode "text=$1" >/dev/null
}

mkdir -p "$(dirname "$STATE")"
status=ok
failures=0
if [ -r "$STATE" ]; then
  read -r saved_status saved_failures < "$STATE" || true
  case "$saved_status" in ok|down) status=$saved_status;; esac
  case "$saved_failures" in ''|*[!0-9]*) failures=0;; *) failures=$saved_failures;; esac
fi

body=$(curl -fsS --max-time 5 --retry 0 "$URL" 2>/dev/null || true)
if [ "$body" = "ready" ]; then
  if [ "$status" = "down" ]; then
    notify "wg-monitor probe: RECOVERED ($URL)"
  fi
  write_state ok 0
  exit 0
fi

failures=$((failures + 1))
if [ "$failures" -ge 2 ]; then
  if [ "$status" != "down" ]; then
    notify "wg-monitor probe: DOWN ($URL)"
  fi
  write_state down "$failures"
else
  write_state ok "$failures"
fi
exit 1
`

	service := `[Unit]
Description=External readiness probe for wg-monitor
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=/etc/wg-monitor-probe.env
ExecStart=/usr/local/bin/wg-monitor-probe
DynamicUser=yes
StateDirectory=wg-monitor-probe
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
NoNewPrivileges=yes
`
	timer := `[Unit]
Description=Run wg-monitor external readiness probe every minute

[Timer]
OnBootSec=1min
OnUnitActiveSec=1min
AccuracySec=5s
Persistent=true

[Install]
WantedBy=timers.target
`
	env := "TG_TOKEN=" + tokenValue + "\nTG_CHAT=" + chatValue + "\n"
	return externalProbeFiles{[]byte(script), []byte(env), []byte(service), []byte(timer)}, nil
}

func externalProbePreview(state *State) (string, error) {
	if err := validateExternalProbeURL(state.ExternalProbe.ReadyURL); err != nil {
		return "", err
	}
	p := state.ExternalProbe
	port := p.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("host=%s:%d user=%s url=%s timeout=5s failures=2 timer=1m host_key=%s credentials=separate",
		p.Host, port, p.User, p.ReadyURL, externalProbeAlias), nil
}

func installExternalProbe(r remoteExec, files externalProbeFiles) error {
	tmpOut, err := r.MustRun("umask 077; mktemp -d /tmp/wg-monitor-probe.XXXXXXXX")
	if err != nil {
		return err
	}
	tmpDir := strings.TrimSpace(tmpOut)
	if !regexp.MustCompile(`^/tmp/wg-monitor-probe\.[A-Za-z0-9]+$`).MatchString(tmpDir) {
		return fmt.Errorf("remote mktemp returned an unsafe path")
	}
	uploads := []struct {
		path, mode string
		data       []byte
	}{
		{tmpDir + "/probe", "0700", files.Script},
		{tmpDir + "/probe.env", "0600", files.Env},
		{tmpDir + "/probe.service", "0644", files.Service},
		{tmpDir + "/probe.timer", "0644", files.Timer},
	}
	for _, f := range uploads {
		if err := r.UploadSFTPMode(f.path, f.data, f.mode); err != nil {
			return err
		}
	}
	cmd := "sudo install -o root -g root -m 0755 " + tmpDir + "/probe " + externalProbeScriptPath +
		" && sudo install -o root -g root -m 0600 " + tmpDir + "/probe.env " + externalProbeEnvPath +
		" && sudo install -o root -g root -m 0644 " + tmpDir + "/probe.service " + externalProbeServicePath +
		" && sudo install -o root -g root -m 0644 " + tmpDir + "/probe.timer " + externalProbeTimerPath +
		" && rm -rf " + tmpDir +
		" && sudo systemctl daemon-reload && sudo systemctl enable --now wg-monitor-probe.timer" +
		" && sudo systemctl is-enabled --quiet wg-monitor-probe.timer && sudo test -x " + externalProbeScriptPath + " && sudo test -r " + externalProbeEnvPath
	_, err = r.MustRun(cmd)
	return err
}

func externalProbeStatus(r remoteExec) (string, error) {
	return r.MustRun("systemctl is-enabled wg-monitor-probe.timer 2>/dev/null || true; systemctl is-active wg-monitor-probe.timer 2>/dev/null || true; sudo cat /var/lib/wg-monitor-probe/state 2>/dev/null || true")
}

func removeExternalProbe(r remoteExec) error {
	cmd := "sudo systemctl disable --now wg-monitor-probe.timer >/dev/null 2>&1 || true" +
		" && sudo rm -f " + externalProbeScriptPath + " " + externalProbeEnvPath + " " + externalProbeServicePath + " " + externalProbeTimerPath +
		" && sudo rm -rf /var/lib/wg-monitor-probe && sudo systemctl daemon-reload" +
		" && test ! -e " + externalProbeScriptPath + " && test ! -e " + externalProbeEnvPath + " && test ! -e " + externalProbeTimerPath
	_, err := r.MustRun(cmd)
	return err
}

var connectExternalProbe = func(state *State, secrets *SecretStore) (remoteSession, error) {
	p := state.ExternalProbe
	port := p.Port
	if port == 0 {
		port = 22
	}
	user := p.User
	if user == "" {
		user = "root"
	}
	var auth []ssh.AuthMethod
	if normalizeBackendSSHAuth(p.SSHAuth) == backendSSHAuthKey {
		signer, err := loadPrivateKeySigner(expandLocalPath(p.KeyPath), secrets.GetNonInteractive("EXTERNAL_PROBE_SSH_KEY_PASSPHRASE"))
		if err != nil {
			return nil, err
		}
		auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else {
		password, _ := secrets.Get("EXTERNAL_PROBE_SSH_PASSWORD", "Пароль SSH внешнего probe-хоста", nil)
		if password == "" {
			return nil, fmt.Errorf("external probe SSH password is missing")
		}
		auth = []ssh.AuthMethod{ssh.Password(password)}
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return nil, err
	}
	return ConnectSSHWithAuth(p.Host, port, user, auth, kh, externalProbeAlias)
}

func actionExternalProbe(state *State, secrets *SecretStore, mode string) error {
	if mode == "preview" {
		preview, err := externalProbePreview(state)
		if err == nil {
			PrintInfo(preview)
		}
		return err
	}
	if state.ExternalProbe.Host == "" {
		return fmt.Errorf("external_probe.host is not configured")
	}
	r, err := connectExternalProbe(state, secrets)
	if err != nil {
		return err
	}
	defer r.Close()
	switch mode {
	case "status":
		out, err := externalProbeStatus(r)
		if strings.TrimSpace(out) == "" {
			PrintWarn("external probe is not installed")
		} else {
			PrintInfo(strings.TrimSpace(out))
		}
		return err
	case "install":
		token, _ := secrets.Get("EXTERNAL_PROBE_TG_TOKEN", "Отдельный Telegram bot token для внешнего probe", nil)
		chat, _ := secrets.Get("EXTERNAL_PROBE_TG_CHAT", "Chat ID внешнего probe", nil)
		files, err := renderExternalProbeFiles(state.ExternalProbe.ReadyURL, token, chat)
		if err != nil {
			return err
		}
		if err := installExternalProbe(r, files); err != nil {
			return err
		}
		state.ExternalProbe.LastInstall = time.Now().UTC().Format(time.RFC3339)
		return nil
	case "remove":
		return removeExternalProbe(r)
	default:
		return fmt.Errorf("unknown external probe action %q", mode)
	}
}

func parseExternalProbePort(raw string) (int, error) {
	p, err := strconv.Atoi(raw)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port")
	}
	return p, nil
}

func configureExternalProbeInteractive(state *State) {
	p := &state.ExternalProbe
	p.Host = strings.TrimSpace(Ask("SSH host внешнего probe (не backend VPS)", p.Host))
	p.User = strings.TrimSpace(Ask("SSH user", firstNonEmpty(p.User, "root")))
	portRaw := strings.TrimSpace(Ask("SSH port", strconv.Itoa(firstNonZero(p.Port, 22))))
	if port, err := parseExternalProbePort(portRaw); err == nil {
		p.Port = port
	}
	p.ReadyURL = strings.TrimSpace(Ask("Публичный HTTPS URL /readyz", p.ReadyURL))
	auth := strings.ToLower(strings.TrimSpace(Ask("SSH auth: password или key", firstNonEmpty(p.SSHAuth, "password"))))
	p.SSHAuth = normalizeBackendSSHAuth(auth)
	if p.SSHAuth == backendSSHAuthKey {
		p.KeyPath = expandLocalPath(strings.TrimSpace(Ask("Путь к private key", p.KeyPath)))
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}
