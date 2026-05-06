package main

import (
	"fmt"
	"time"
)

func actionUpdateBackend(state *State, secrets *SecretStore, dl *Downloader) error {
	if state.Backend.Host == "" {
		PrintFail("В wizard.toml нет [backend] — сначала запусти install-backend")
		return fmt.Errorf("no backend configured")
	}

	rel, err := dl.GetLatestRelease()
	if err != nil {
		PrintFail("GitHub API: " + err.Error())
		return err
	}
	PrintOK(fmt.Sprintf("последний релиз: %s", rel.TagName))

	pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
	if pass == "" {
		PrintFail("пароль обязателен")
		return fmt.Errorf("missing password")
	}

	khPath := defaultCacheDir() + "/known_hosts"
	kh, err := NewKnownHosts(khPath)
	if err != nil {
		return err
	}

	port := state.Backend.Port
	if port == 0 {
		port = 22
	}
	user := state.Backend.User
	if user == "" {
		user = "root"
	}

	PrintStep(1, 4, "SSH к VPS")
	s, err := ConnectSSH(state.Backend.Host, port, user, pass, kh)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	defer s.Close()
	if err := stepCheckSSH(s, state.Backend.Host); err != nil {
		return err
	}

	PrintStep(2, 4, "Скачать бэкенд бинарь")
	localPath, err := stepDownloadAsset(dl, rel, "wg-monitor-backend-linux-amd64")
	if err != nil {
		return err
	}

	PrintStep(3, 4, "Atomic upload + restart")
	if err := stepUploadAndSwap(s, localPath, "/usr/local/bin/wg-monitor-backend", "wg-monitor-backend"); err != nil {
		return err
	}

	PrintStep(4, 4, "Verify /health")
	if state.Backend.Domain == "" {
		PrintWarn("домен не задан в wizard.toml — пропускаю /health проверку")
	} else {
		url := "https://" + state.Backend.Domain + "/health"
		if err := stepVerifyHTTP(s, url); err != nil {
			return err
		}
	}

	state.Backend.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	state.Backend.LastDeployedVersion = rel.TagName
	return nil
}
