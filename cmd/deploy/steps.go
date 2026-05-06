package main

import (
	"fmt"
	"os"
	"strings"
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
