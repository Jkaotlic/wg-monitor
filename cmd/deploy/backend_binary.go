package main

import (
	"fmt"
	"strings"
)

type backendSwapPlan struct {
	Name           string
	RemotePath     string
	StopCmd        string
	StartCmd       string
	StopBeforeSwap bool
}

func normalizeBackendArch(arch string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(arch)) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported backend arch: %s (supported: x86_64/amd64, aarch64/arm64)", strings.TrimSpace(arch))
	}
}

func backendReleaseAssetName(arch string) (string, error) {
	normalized, err := normalizeBackendArch(arch)
	if err != nil {
		return "", err
	}
	return "wg-monitor-backend-linux-" + normalized, nil
}

func stepDownloadBackendAsset(s *SSH, dl *Downloader, rel *Release) (string, error) {
	out, err := s.MustRun("uname -m")
	if err != nil {
		return "", err
	}
	assetName, err := backendReleaseAssetName(out)
	if err != nil {
		PrintFail(err.Error())
		return "", err
	}
	PrintOK("backend architecture: " + strings.TrimPrefix(assetName, "wg-monitor-backend-linux-"))
	return stepDownloadAsset(dl, rel, assetName)
}

func detectBackendSwapPlan(s *SSH) backendSwapPlan {
	if _, _, rc, _ := s.Run("test -f /home/user/wg-monitor/bin/wg-monitor-backend && command -v docker >/dev/null 2>&1 && docker inspect wg-monitor-backend >/dev/null 2>&1"); rc == 0 {
		return backendSwapPlan{
			Name:       "docker:wg-monitor-backend",
			RemotePath: "/home/user/wg-monitor/bin/wg-monitor-backend",
			StartCmd:   "docker restart wg-monitor-backend >/dev/null || docker start wg-monitor-backend >/dev/null",
		}
	}
	return backendSystemdSwapPlan("wg-monitor-backend")
}

func backendSystemdSwapPlan(service string) backendSwapPlan {
	plan := backendSwapPlan{
		Name:       "systemd:" + service,
		RemotePath: "/usr/local/bin/wg-monitor-backend",
	}
	if strings.TrimSpace(service) != "" {
		plan.StopCmd = "systemctl stop " + service
		plan.StartCmd = "systemctl start " + service
		plan.StopBeforeSwap = true
	}
	return plan
}

func stepUploadBackendAndRestart(s *SSH, localPath string, plan backendSwapPlan) error {
	data, err := readFile(localPath)
	if err != nil {
		PrintFail("read local: " + err.Error())
		return err
	}
	if strings.TrimSpace(plan.RemotePath) == "" {
		return fmt.Errorf("backend remote path is empty")
	}
	wantSha := hashHex(data)
	tmp := plan.RemotePath + ".new"
	bak := plan.RemotePath + ".bak"

	PrintInfo(fmt.Sprintf("upload -> %s (%d bytes)", tmp, len(data)))
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

	if plan.StopBeforeSwap && plan.StopCmd != "" {
		PrintInfo(plan.StopCmd)
		if _, err := s.MustRun(plan.StopCmd); err != nil {
			PrintFail(err.Error())
			s.Run("rm -f " + tmp)
			return err
		}
	}

	s.Run(fmt.Sprintf("cp -p %s %s 2>/dev/null", plan.RemotePath, bak))
	if _, err := s.MustRun(fmt.Sprintf("mv %s %s && chmod 755 %s", tmp, plan.RemotePath, plan.RemotePath)); err != nil {
		PrintFail("atomic swap: " + err.Error())
		s.Run("rm -f " + tmp)
		if plan.StartCmd != "" {
			s.Run(plan.StartCmd)
		}
		return err
	}
	PrintOK("backend binary updated")

	if plan.StartCmd == "" {
		return nil
	}
	PrintInfo(plan.StartCmd)
	if _, err := s.MustRun(plan.StartCmd); err != nil {
		PrintFail("new backend did not start: " + err.Error())
		if _, _, rc, _ := s.Run("test -f " + bak); rc == 0 {
			PrintWarn("automatic rollback to " + bak)
			if plan.StopCmd != "" {
				s.Run(plan.StopCmd)
			}
			if _, rbErr := s.MustRun(fmt.Sprintf("mv %s %s && chmod 755 %s", bak, plan.RemotePath, plan.RemotePath)); rbErr != nil {
				PrintFail("rollback failed: " + rbErr.Error())
				return err
			}
			if _, rbErr := s.MustRun(plan.StartCmd); rbErr != nil {
				PrintFail("old backend also failed to start: " + rbErr.Error())
				return err
			}
			PrintOK("rollback complete - previous backend is running")
		}
		return err
	}
	PrintOK(nonEmpty(plan.Name, "backend") + " restarted")
	return nil
}

func stepRollbackBackendBinary(s *SSH, plan backendSwapPlan) error {
	bak := plan.RemotePath + ".bak"
	if _, _, rc, _ := s.Run("test -f " + bak); rc != 0 {
		return fmt.Errorf(".bak absent, nothing to rollback")
	}
	PrintWarn("automatic rollback to " + bak)
	if plan.StopCmd != "" {
		s.Run(plan.StopCmd)
	}
	if _, err := s.MustRun(fmt.Sprintf("mv %s %s && chmod 755 %s", bak, plan.RemotePath, plan.RemotePath)); err != nil {
		return err
	}
	if plan.StartCmd != "" {
		if _, err := s.MustRun(plan.StartCmd); err != nil {
			return err
		}
	}
	PrintOK("rollback complete - previous backend is running")
	return nil
}
