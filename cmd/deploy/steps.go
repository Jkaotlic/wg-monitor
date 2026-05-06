package main

import (
	"fmt"
	"os"
	"strings"
	"time"
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

// stepUploadAndSwap: stop service → upload to TMP via dd-stdin → sha256 verify → atomic mv → start.
// service = systemd unit name (e.g. "wg-monitor-backend") or empty to skip systemctl
func stepUploadAndSwap(s *SSH, localPath, remotePath, service string) error {
	data, err := readFile(localPath)
	if err != nil {
		PrintFail("read local: " + err.Error())
		return err
	}
	wantSha := hashHex(data)

	if service != "" {
		PrintInfo("systemctl stop " + service)
		if _, err := s.MustRun("systemctl stop " + service); err != nil {
			PrintFail(err.Error())
			return err
		}
	}

	tmp := remotePath + ".new"
	PrintInfo(fmt.Sprintf("upload → %s (%d bytes)", tmp, len(data)))
	if err := s.UploadSFTP(tmp, data); err != nil {
		PrintFail("upload: " + err.Error())
		return err
	}

	out, err := s.MustRun("sha256sum " + tmp + " | awk '{print $1}'")
	if err != nil {
		PrintFail("sha256sum: " + err.Error())
		return err
	}
	gotSha := strings.TrimSpace(out)
	if gotSha != wantSha {
		PrintFail(fmt.Sprintf("sha256 mismatch: local %s remote %s", wantSha[:16], gotSha[:16]))
		s.Run("rm -f " + tmp)
		return fmt.Errorf("sha256 mismatch")
	}
	PrintOK("sha256 совпадает")

	cmd := fmt.Sprintf("mv %s %s && chmod 755 %s", tmp, remotePath, remotePath)
	if _, err := s.MustRun(cmd); err != nil {
		PrintFail("atomic swap: " + err.Error())
		return err
	}
	PrintOK("бинарь обновлён")

	if service != "" {
		PrintInfo("systemctl start " + service)
		if _, err := s.MustRun("systemctl start " + service); err != nil {
			PrintFail(err.Error())
			return err
		}
		PrintOK(service + " запущен")
	}
	return nil
}

// stepVerifyHTTP: curl URL, expect 200.
func stepVerifyHTTP(s *SSH, url string) error {
	cmd := fmt.Sprintf("curl -sS -o /dev/null -w '%%{http_code}' %s", url)
	out, err := s.MustRun(cmd)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	code := strings.TrimSpace(out)
	if code != "200" {
		PrintFail(fmt.Sprintf("%s → HTTP %s", url, code))
		return fmt.Errorf("expected 200 got %s", code)
	}
	PrintOK(fmt.Sprintf("%s → 200 OK", url))
	return nil
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
	if _, err := s.MustRun("chmod " + mode + " " + remotePath); err != nil {
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

	PrintInfo("остановка агента")
	s.Run("/opt/etc/init.d/S99wg-monitor stop 2>/dev/null; killall -9 wg-monitor 2>/dev/null; sleep 1; true")

	tmp := remotePath + ".new"
	PrintInfo(fmt.Sprintf("upload → %s (%d bytes, через stdin pipe)", tmp, len(data)))
	if err := s.UploadStdin(tmp, data); err != nil {
		PrintFail("upload: " + err.Error())
		return err
	}
	if _, err := s.MustRun("chmod 755 " + tmp); err != nil {
		PrintFail(err.Error())
		return err
	}

	out, err := s.MustRun("sha256sum " + tmp + " | awk '{print $1}'")
	if err != nil {
		PrintFail("sha256sum: " + err.Error())
		return err
	}
	gotSha := strings.TrimSpace(out)
	if gotSha != wantSha {
		PrintFail(fmt.Sprintf("sha256 mismatch: local %s remote %s", wantSha[:16], gotSha[:16]))
		s.Run("rm -f " + tmp)
		return fmt.Errorf("sha256 mismatch")
	}
	PrintOK("sha256 совпадает")

	if _, err := s.MustRun("mv " + tmp + " " + remotePath); err != nil {
		PrintFail("atomic swap: " + err.Error())
		return err
	}
	PrintOK("бинарь обновлён")

	if _, err := s.MustRun("/opt/etc/init.d/S99wg-monitor start"); err != nil {
		PrintFail("start: " + err.Error())
		return err
	}

	// Wait briefly for the daemon to come up.
	time.Sleep(2 * time.Second)
	out, _ = s.MustRun("pidof wg-monitor")
	if strings.TrimSpace(out) == "" {
		PrintFail("процесс wg-monitor не появился после старта")
		return fmt.Errorf("agent did not start")
	}
	PrintOK("агент запущен (PID " + strings.TrimSpace(out) + ")")
	return nil
}
