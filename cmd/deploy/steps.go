package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// stepCheckSSH connects and reports OK/fail.
func stepCheckSSH(s *SSH, label string) error {
	out, err := s.MustRun("uname -a")
	if err != nil {
		PrintFail(fmt.Sprintf("SSH к %s не отвечает: %v", label, err))
		return err
	}
	PrintOK(fmt.Sprintf("SSH к %s OK (%s)", label, strings.TrimSpace(out)))
	return nil
}

// stepDownloadAsset fetches a binary from GitHub Releases (cached).
// Returns local path.
func stepDownloadAsset(dl *Downloader, rel *Release, assetName string) (string, error) {
	asset := rel.AssetByName(assetName)
	if asset == nil {
		PrintFail(fmt.Sprintf("в релизе %s нет артефакта %s", rel.TagName, assetName))
		return "", fmt.Errorf("missing asset %s", assetName)
	}
	checks := rel.AssetByName("checksums.txt")
	if checks == nil {
		PrintFail("в релизе нет checksums.txt — отказываюсь без верификации")
		return "", fmt.Errorf("missing checksums.txt")
	}
	PrintInfo(fmt.Sprintf("скачиваю %s...", assetName))
	path, err := dl.GetAsset(asset.DownloadURL, assetName, checks.DownloadURL, rel.TagName)
	if err != nil {
		PrintFail(err.Error())
		return "", err
	}
	PrintOK(fmt.Sprintf("%s готов (%s)", assetName, path))
	return path, nil
}

// stepUploadAndSwap: upload TMP via SFTP → sha256 verify → stop service →
// backup current binary as .bak → atomic mv → start. On start failure auto-
// rollback: stop, restore .bak, start old binary; surface the original
// systemctl error to the caller.
//
// Sequence rationale:
//   - Verify upload integrity (sha) BEFORE we touch the running service —
//     a CDN-corrupted download must not trigger a stop.
//   - Backup current binary before swap so a broken new binary is recoverable
//     without manual intervention.
//   - On systemctl start failure, we know the binary is at fault (not the
//     filesystem swap) and the .bak is intact — auto-restore.
//
// service = systemd unit name (e.g. "wg-monitor-backend") or empty to skip systemctl.
func stepUploadAndSwap(s *SSH, localPath, remotePath, service string) error {
	data, err := readFile(localPath)
	if err != nil {
		PrintFail("read local: " + err.Error())
		return err
	}
	wantSha := hashHex(data)

	tmp := remotePath + ".new"
	bak := remotePath + ".bak"
	PrintInfo(fmt.Sprintf("upload → %s (%d bytes)", tmp, len(data)))
	if err := s.UploadSFTP(tmp, data); err != nil {
		PrintFail("upload: " + err.Error())
		return err
	}

	out, err := s.MustRun("sha256sum " + tmp + " | awk '{print $1}'")
	if err != nil {
		PrintFail("sha256sum: " + err.Error())
		s.Run("rm -f " + tmp)
		return err
	}
	gotSha := strings.TrimSpace(out)
	if gotSha != wantSha {
		PrintFail(fmt.Sprintf("sha256 mismatch: local %s remote %s", wantSha[:16], gotSha[:16]))
		s.Run("rm -f " + tmp)
		return fmt.Errorf("sha256 mismatch")
	}
	PrintOK("sha256 совпадает")

	if service != "" {
		PrintInfo("systemctl stop " + service)
		if _, err := s.MustRun("systemctl stop " + service); err != nil {
			PrintFail(err.Error())
			s.Run("rm -f " + tmp)
			return err
		}
	}

	// Backup current binary (best-effort: missing remotePath on first install
	// → cp returns rc=1, that's fine, we just won't have rollback capability).
	s.Run(fmt.Sprintf("cp -p %s %s 2>/dev/null", remotePath, bak))

	cmd := fmt.Sprintf("mv %s %s && chmod 755 %s", tmp, remotePath, remotePath)
	if _, err := s.MustRun(cmd); err != nil {
		PrintFail("atomic swap: " + err.Error())
		// tmp may or may not exist after a partial mv; clean up best-effort.
		s.Run("rm -f " + tmp)
		// Swap failed — old binary still in place; just restart.
		if service != "" {
			s.Run("systemctl start " + service)
		}
		return err
	}
	PrintOK("бинарь обновлён")

	if service != "" {
		PrintInfo("systemctl start " + service)
		if _, startErr := s.MustRun("systemctl start " + service); startErr != nil {
			PrintFail("новый бинарь не запустился: " + startErr.Error())
			// Auto-rollback to .bak if it exists.
			if _, _, rc, _ := s.Run("test -f " + bak); rc == 0 {
				PrintWarn("автоматический откат на " + bak)
				s.Run("systemctl stop " + service)
				if _, rbErr := s.MustRun(fmt.Sprintf("mv %s %s && chmod 755 %s", bak, remotePath, remotePath)); rbErr != nil {
					PrintFail("откат не удался: " + rbErr.Error())
					return startErr
				}
				if _, rbErr := s.MustRun("systemctl start " + service); rbErr != nil {
					PrintFail("старый бинарь тоже не стартует: " + rbErr.Error())
					return startErr
				}
				PrintOK("откат успешен — работает старая версия")
			} else {
				PrintWarn(".bak отсутствует, откатывать нечего (вероятно, это был первый install)")
			}
			return startErr
		}
		PrintOK(service + " запущен")
	}
	return nil
}

// stepVerifyHTTP: curl URL, expect 200. Backend restarts can return
// connection-refused for a short window after systemctl start succeeds, so
// give the listener a few seconds before surfacing the failure.
func stepVerifyHTTP(s *SSH, url string) error {
	cmd := curlHTTPCodeCommand(url)
	deadline := time.Now().Add(12 * time.Second)
	var lastOut string
	var lastErr error
	for {
		out, err := s.MustRun(cmd)
		lastOut, lastErr = out, err
		if err == nil && strings.TrimSpace(out) == "200" {
			PrintOK(fmt.Sprintf("%s → 200 OK", url))
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		PrintFail(lastErr.Error())
		return lastErr
	}
	code := strings.TrimSpace(lastOut)
	if code != "200" {
		PrintFail(fmt.Sprintf("%s → HTTP %s", url, code))
		return fmt.Errorf("expected 200 got %s", code)
	}
	PrintOK(fmt.Sprintf("%s → 200 OK", url))
	return nil
}

func curlHTTPCodeCommand(url string) string {
	return "curl -sS -o /dev/null -w '%{http_code}' " + shellSingleQuote(url)
}

func stepVerifyBackendHealth(s *SSH, domain string) error {
	if err := stepVerifyHTTP(s, backendHealthURL(domain)); err != nil {
		return err
	}
	return nil
}

func backendHealthURL(domain string) string {
	host := domainHost(domain)
	if isLoopbackDeployHost(host) {
		base := strings.TrimRight(strings.TrimSpace(domain), "/")
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
		return base + "/healthz"
	}
	return "http://127.0.0.1:8080/healthz"
}

func isLoopbackDeployHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func domainHost(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	if strings.Contains(domain, "://") {
		if u, err := url.Parse(domain); err == nil {
			return u.Hostname()
		}
	}
	host := strings.TrimSuffix(domain, "/")
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// stepEnsureUser creates a system user if missing.
func stepEnsureUser(s *SSH, name string) error {
	out, _ := s.MustRun("id -u " + name + " 2>/dev/null; true")
	if strings.TrimSpace(out) != "" {
		PrintSkip("user " + name + " существует")
		return nil
	}
	cmd := fmt.Sprintf("useradd --system --no-create-home --shell /usr/sbin/nologin %s", name)
	if _, err := s.MustRun(cmd); err != nil {
		PrintFail(err.Error())
		return err
	}
	PrintOK("user " + name + " создан")
	return nil
}

// stepEnsureDir mkdir -p with chown.
func stepEnsureDir(s *SSH, path, owner string) error {
	if _, err := s.MustRun("mkdir -p " + path); err != nil {
		PrintFail(err.Error())
		return err
	}
	if owner != "" {
		s.MustRun("chown " + owner + " " + path)
	}
	PrintOK(path)
	return nil
}

// stepUploadFile uploads bytes via UploadSFTP and chmod's.
func stepUploadFile(s *SSH, remotePath string, data []byte, mode string) error {
	if err := s.UploadSFTP(remotePath, data); err != nil {
		PrintFail("upload: " + err.Error())
		return err
	}
	if _, err := s.MustRun("chmod " + mode + " " + shellSingleQuote(remotePath)); err != nil {
		PrintFail(err.Error())
		return err
	}
	PrintOK(remotePath)
	return nil
}

// stepCheckCaddyInstalled returns true if caddy is on PATH.
func stepCheckCaddyInstalled(s *SSH) bool {
	_, _, rc, _ := s.Run("which caddy")
	return rc == 0
}

// stepInstallCaddy: A/M/S choice. A only if Debian-family.
func stepInstallCaddy(s *SSH) error {
	if stepCheckCaddyInstalled(s) {
		PrintSkip("caddy уже установлен")
		return nil
	}
	PrintWarn("Caddy не установлен. Команды для установки на Debian/Ubuntu:")
	fmt.Println(Colorize("    apt install -y debian-keyring debian-archive-keyring apt-transport-https", ColorDim))
	fmt.Println(Colorize("    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \\", ColorDim))
	fmt.Println(Colorize("      | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg", ColorDim))
	fmt.Println(Colorize("    echo 'deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] \\", ColorDim))
	fmt.Println(Colorize("      https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main' \\", ColorDim))
	fmt.Println(Colorize("      > /etc/apt/sources.list.d/caddy-stable.list", ColorDim))
	fmt.Println(Colorize("    apt update && apt install -y caddy", ColorDim))
	fmt.Println()

	// Detect Debian family for [A] availability.
	_, _, rc, _ := s.Run("test -f /etc/debian_version")
	debian := rc == 0

	opts := []ChoiceOption{}
	if debian {
		opts = append(opts, ChoiceOption{"A", "Сделай за меня по SSH"})
	}
	opts = append(opts,
		ChoiceOption{"M", "Я сам поставлю — нажму Enter когда готов"},
		ChoiceOption{"S", "Скипнуть"},
	)
	choice := AskChoice("Что делаем?", opts)

	switch choice {
	case "A":
		install := strings.Join([]string{
			"apt install -y debian-keyring debian-archive-keyring apt-transport-https",
			"curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg",
			"echo 'deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main' > /etc/apt/sources.list.d/caddy-stable.list",
			"apt update",
			"apt install -y caddy",
		}, " && ")
		if _, err := s.MustRun(install); err != nil {
			PrintFail(err.Error())
			return err
		}
		PrintOK("caddy установлен")
	case "M":
		Ask("Поставь Caddy и нажми Enter", "")
		if !stepCheckCaddyInstalled(s) {
			PrintFail("Caddy всё ещё не найден. Прерываю.")
			return fmt.Errorf("caddy not installed")
		}
		PrintOK("caddy найден")
	case "S":
		PrintWarn("Caddy скипнут. /health не сможет ответить через TLS.")
	}
	return nil
}

// stepDetectKeeneticArch returns "arm64" or "mipsle" based on `uname -m`.
// Aborts on unsupported arch.
func stepDetectKeeneticArch(s *SSH) (string, error) {
	out, err := s.MustRun("uname -m")
	if err != nil {
		return "", err
	}
	arch := strings.TrimSpace(out)
	switch arch {
	case "aarch64", "arm64":
		PrintOK("архитектура: arm64")
		return "arm64", nil
	case "mips", "mipsel", "mipsle":
		PrintOK("архитектура: mipsle")
		return "mipsle", nil
	default:
		PrintFail(fmt.Sprintf("неподдерживаемая архитектура %q (поддержано: aarch64, mipsel)", arch))
		return "", fmt.Errorf("unsupported arch: %s", arch)
	}
}

// stepUploadAgentBinary stops, swaps, restarts the Keenetic agent.
// Uses UploadStdin (dropbear has no SFTP/dd-friendly stack on some firmwares).
//
// Sequence: upload to .new → sha256 verify (BEFORE killing the agent — a bad
// upload must NOT take the agent down) → backup current binary as .bak →
// killall + atomic mv → start. On start failure, auto-rollback to .bak.
func stepUploadAgentBinary(s *SSH, localPath, remotePath string) error {
	data, err := readFile(localPath)
	if err != nil {
		PrintFail("read local: " + err.Error())
		return err
	}
	wantSha := hashHex(data)

	PrintInfo("ensure /opt/var/wg-monitor exists")
	if _, err := s.MustRun("mkdir -p /opt/var/wg-monitor /opt/bin /opt/etc/wg-monitor /opt/etc/init.d"); err != nil {
		PrintFail(err.Error())
		return err
	}

	tmp := remotePath + ".new"
	bak := remotePath + ".bak"
	PrintInfo(fmt.Sprintf("upload → %s (%d bytes, через stdin pipe)", tmp, len(data)))
	if err := s.UploadStdin(tmp, data); err != nil {
		PrintFail("upload: " + err.Error())
		return err
	}
	if _, err := s.MustRun("chmod 755 " + tmp); err != nil {
		PrintFail(err.Error())
		s.Run("rm -f " + tmp)
		return err
	}

	out, err := s.MustRun("sha256sum " + tmp + " | awk '{print $1}'")
	if err != nil {
		PrintFail("sha256sum: " + err.Error())
		s.Run("rm -f " + tmp)
		return err
	}
	gotSha := strings.TrimSpace(out)
	if gotSha != wantSha {
		PrintFail(fmt.Sprintf("sha256 mismatch: local %s remote %s", wantSha[:16], gotSha[:16]))
		s.Run("rm -f " + tmp)
		// Агент при этом не остановлен — продолжает работать на старом бинаре.
		return fmt.Errorf("sha256 mismatch")
	}
	PrintOK("sha256 совпадает")

	// Backup ДО kill — иначе если cp упадёт, у нас нет .bak для отката.
	s.Run(fmt.Sprintf("cp -p %s %s 2>/dev/null", remotePath, bak))

	PrintInfo("остановка агента")
	s.Run("/opt/etc/init.d/S99wg-monitor stop 2>/dev/null; killall -9 wg-monitor 2>/dev/null; sleep 1; true")

	if _, err := s.MustRun("mv " + tmp + " " + remotePath); err != nil {
		PrintFail("atomic swap: " + err.Error())
		// mv упал, агент остановлен — пытаемся восстановить старый.
		s.Run("rm -f " + tmp)
		s.Run(fmt.Sprintf("cp -p %s %s && /opt/etc/init.d/S99wg-monitor start", bak, remotePath))
		return err
	}
	PrintOK("бинарь обновлён")

	if _, err := s.MustRun("/opt/etc/init.d/S99wg-monitor start"); err != nil {
		PrintFail("start: " + err.Error())
		return tryRestoreAgentBak(s, remotePath, bak, err)
	}

	// Wait briefly for the daemon to come up.
	time.Sleep(2 * time.Second)
	out, _ = s.MustRun("pidof wg-monitor")
	if strings.TrimSpace(out) == "" {
		PrintFail("процесс wg-monitor не появился после старта")
		return tryRestoreAgentBak(s, remotePath, bak, fmt.Errorf("agent did not start"))
	}
	PrintOK("агент запущен (PID " + strings.TrimSpace(out) + ")")
	return nil
}

// tryRestoreAgentBak rolls back /opt/bin/wg-monitor to its .bak after a failed
// start of the new binary. Best-effort: if .bak doesn't exist (first install)
// or the rollback itself fails, surface the original startErr unchanged so
// the caller doesn't see misleading "rollback failed" noise.
func tryRestoreAgentBak(s *SSH, remotePath, bak string, startErr error) error {
	if _, _, rc, _ := s.Run("test -f " + bak); rc != 0 {
		PrintWarn(".bak отсутствует, откатывать нечего (первый install?)")
		return startErr
	}
	PrintWarn("автоматический откат на " + bak)
	s.Run("killall -9 wg-monitor 2>/dev/null; sleep 1; true")
	if _, err := s.MustRun(fmt.Sprintf("cp -p %s %s && chmod 755 %s", bak, remotePath, remotePath)); err != nil {
		PrintFail("откат не удался: " + err.Error())
		return startErr
	}
	if _, err := s.MustRun("/opt/etc/init.d/S99wg-monitor start"); err != nil {
		PrintFail("старый бинарь тоже не стартует: " + err.Error())
		return startErr
	}
	time.Sleep(2 * time.Second)
	if pid, _ := s.MustRun("pidof wg-monitor"); strings.TrimSpace(pid) != "" {
		PrintOK("откат успешен — работает старая версия (PID " + strings.TrimSpace(pid) + ")")
	} else {
		PrintFail("после отката pidof пуст — агент не поднялся")
	}
	return startErr
}

// stepReadOrEmpty runs `cmd` and returns trimmed stdout. Any non-zero rc or
// transport error becomes "" — caller treats absence as missing data, not a
// fatal condition. Used for purely diagnostic reads (hostname, MAC, etc.).
func stepReadOrEmpty(s *SSH, cmd string) string {
	out, _, rc, err := s.Run(cmd)
	if err != nil || rc != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// readRemoteFile cat's a file via SSH; returns (content, true) on success,
// ("", false) on any failure (file missing, permission denied, transport
// error). Caller distinguishes "no existing file" from "read failed" only via
// the boolean.
func readRemoteFile(s *SSH, path string) (string, bool) {
	out, _, rc, err := s.Run("cat " + path + " 2>/dev/null")
	if err != nil || rc != 0 {
		return "", false
	}
	return out, true
}

// existingInstallDetected returns true when /etc/wg-monitor/backend.yaml is
// present on the remote — meaning install-backend has already run against
// this VPS and a re-run will overwrite it. Permissions denied counts as
// "exists" (file exists, we just can't read it as the SSH user — still a
// rewrite-risk).
func existingInstallDetected(s *SSH) bool {
	_, _, rc, err := s.Run("test -f /etc/wg-monitor/backend.yaml")
	return err == nil && rc == 0
}

func backendInstallSwapService(existingInstall bool) string {
	if existingInstall {
		return "wg-monitor-backend"
	}
	return ""
}

func backendBackupEnableCommand() string {
	return "systemctl daemon-reload && systemctl enable --now wg-monitor-backup.timer"
}

// readDeployedTelegramMeta extracts chat_id and admin_user_id from the
// currently-deployed /etc/wg-monitor/backend.yaml on the VPS. Used by
// install-backend's preflight to show the operator what they're about to
// overwrite. Best-effort — returns 0/0 on any failure.
func readDeployedTelegramMeta(s *SSH) (chatID, adminUserID int64) {
	out, ok := readRemoteFile(s, "/etc/wg-monitor/backend.yaml")
	if !ok {
		return 0, 0
	}
	return parseDeployedTelegramMeta(out)
}

func parseDeployedTelegramMeta(raw string) (chatID, adminUserID int64) {
	var cfg struct {
		Telegram struct {
			ChatID      int64 `yaml:"chat_id"`
			AdminUserID int64 `yaml:"admin_user_id"`
		} `yaml:"telegram"`
	}
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return 0, 0
	}
	return cfg.Telegram.ChatID, cfg.Telegram.AdminUserID
}

// stepDetectPrimaryMAC reads the MAC of the first non-loopback ethernet
// interface that has one. Returns "" when nothing matches (e.g. dropbear's
// minimal toolchain doesn't surface /sys/class/net contents). Diagnostic
// only — used by the install-agent identity-check banner so the operator
// can sanity-check which physical box they're talking to before any write.
func stepDetectPrimaryMAC(s *SSH) string {
	cmd := `for iface in $(ls /sys/class/net 2>/dev/null); do
		[ "$iface" = "lo" ] && continue
		mac=$(cat /sys/class/net/$iface/address 2>/dev/null)
		[ -n "$mac" ] && [ "$mac" != "00:00:00:00:00:00" ] && { echo "$iface=$mac"; break; }
	done`
	return stepReadOrEmpty(s, cmd)
}

// extractMAC pulls the MAC half out of the "iface=mac" form returned by
// stepDetectPrimaryMAC, normalises to lowercase with no separators so two
// captures of the same physical NIC compare equal regardless of how the
// raw read formatted them (colons vs dashes vs raw hex). Empty input → "".
func extractMAC(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	if idx := strings.Index(out, "="); idx >= 0 {
		out = out[idx+1:]
	}
	out = strings.TrimSpace(out)
	out = strings.ReplaceAll(out, ":", "")
	out = strings.ReplaceAll(out, "-", "")
	return strings.ToLower(out)
}

// verifyExpectedMAC reads the router's primary MAC over the live SSH
// session and compares it (after normalisation) to the value pinned in
// wizard.toml at install time. Returns nil when the values match or when
// no MAC is pinned (back-compat for agents added before this feature).
// Returns an error with an actionable hint on mismatch — caller must
// abort BEFORE any write op.
//
// Empty router-side MAC (busybox without /sys/class/net/*/address) is
// treated as "can't verify" → nil, like no-pin. We don't want a dropbear
// quirk on one model to brick deploys; the known_hosts alias TOFU plus
// stepReadExistingAgentNickname still catch the most common wrong-router
// scenarios.
func verifyExpectedMAC(s *SSH, expected string) error {
	if expected == "" {
		return nil
	}
	raw := stepDetectPrimaryMAC(s)
	actual := extractMAC(raw)
	if actual == "" {
		PrintWarn("не смог прочитать MAC роутера (busybox без /sys/class/net?) — пропускаю проверку")
		return nil
	}
	want := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(expected, ":", ""), "-", ""))
	if actual != want {
		return fmt.Errorf(
			"MAC роутера %s, ожидаю %s — это другое физическое устройство. "+
				"Возможные причины: (a) активен не тот SSTP; (b) клиент заменил Keenetic — обнови expected_mac в wizard.toml вручную",
			actual, want)
	}
	PrintOK("MAC совпадает с pinned (" + actual + ")")
	return nil
}

// stepReadExistingAgentNickname returns the `agent.nickname` value found in
// /opt/etc/wg-monitor/config.yaml on the router, or "" if the file doesn't
// exist or has no nickname line. Used as a pre-flight guard against
// accidentally clobbering an agent set up under a different name (typical
// trigger: VPN is down and the operator is unintentionally talking to the
// LAN-side router on 192.168.31.1 that already hosts another wg-monitor).
//
// Match is line-based to avoid pulling a YAML decoder onto the
// pre-mkdir code path. The template's `nickname:` line is fully under our
// control (cmd/deploy/templates/agent.yaml.tmpl).
func stepReadExistingAgentNickname(s *SSH) string {
	out := stepReadOrEmpty(s, "cat /opt/etc/wg-monitor/config.yaml 2>/dev/null")
	if out == "" {
		return ""
	}
	// Look for the line under `agent:` block. Tolerant — trims whitespace
	// and quotes, ignores commented lines.
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "nickname:"); ok {
			return strings.Trim(strings.TrimSpace(rest), `"'`)
		}
	}
	return ""
}
