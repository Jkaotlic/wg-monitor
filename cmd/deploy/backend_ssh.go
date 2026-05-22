package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	backendSSHAuthPassword = "password"
	backendSSHAuthKey      = "key"
)

func configureBackendSSHAuth(state *State) {
	current := normalizeBackendSSHAuth(state.Backend.SSHAuth)
	answer := Ask("SSH auth для VPS (password/key; можно вставить путь к ключу)", current)
	auth, keyPath, askKeyPath := parseBackendSSHAuthAnswer(answer)
	if auth != backendSSHAuthKey {
		state.Backend.SSHAuth = backendSSHAuthPassword
		state.Backend.KeyPath = ""
		return
	}
	state.Backend.SSHAuth = backendSSHAuthKey
	if askKeyPath {
		keyPath = Ask("SSH private key path", state.Backend.KeyPath)
	}
	state.Backend.KeyPath = expandLocalPath(keyPath)
}

func parseBackendSSHAuthAnswer(raw string) (auth, keyPath string, askKeyPath bool) {
	answer := strings.TrimSpace(raw)
	lower := strings.ToLower(answer)
	switch lower {
	case "k", "key", "ssh-key", "private-key", "ключ":
		return backendSSHAuthKey, "", true
	case "p", "pass", "password", "пароль":
		return backendSSHAuthPassword, "", false
	}
	if looksLikePrivateKeyPath(answer) {
		return backendSSHAuthKey, expandLocalPath(answer), false
	}
	return backendSSHAuthPassword, "", false
}

func looksLikePrivateKeyPath(answer string) bool {
	if answer == "" {
		return false
	}
	return strings.ContainsAny(answer, `\/`) ||
		strings.HasPrefix(answer, "~") ||
		strings.Contains(answer, ":") ||
		strings.HasPrefix(answer, "$") ||
		strings.HasPrefix(answer, "%")
}

func normalizeBackendSSHAuth(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case backendSSHAuthKey, "ssh-key", "private-key":
		return backendSSHAuthKey
	default:
		return backendSSHAuthPassword
	}
}

func connectBackendSSH(state *State, secrets *SecretStore, kh *KnownHosts) (*SSH, error) {
	port := portOrDefault(state.Backend.Port, 22)
	user := userOrDefault(state.Backend.User, "root")
	auth, source, err := backendSSHAuthMethods(state, secrets, true)
	if err != nil {
		return nil, err
	}
	s, err := ConnectSSHWithAuth(state.Backend.Host, port, user, auth, kh, "backend")
	for attempt := 1; err != nil && isTransientSSHTimeout(err) && attempt < 3; attempt++ {
		PrintWarn(fmt.Sprintf("VPS SSH timeout, retry %d/3 через %ds", attempt+1, attempt*2))
		time.Sleep(time.Duration(attempt*2) * time.Second)
		s, err = ConnectSSHWithAuth(state.Backend.Host, port, user, auth, kh, "backend")
	}
	if err == nil {
		return s, nil
	}
	if normalizeBackendSSHAuth(state.Backend.SSHAuth) != backendSSHAuthPassword || !isSSHAuthFailure(err) || source == SourcePrompt {
		return nil, err
	}

	PrintWarn("VPS SSH password из " + source.String() + " не подошёл — можно ввести новый без ручной правки secrets.env")
	fresh := AskSecret("VPS SSH пароль (новый, Enter — отмена)")
	if fresh == "" {
		return nil, err
	}
	os.Setenv("WG_VPS_PASS", fresh)
	if setErr := secrets.Set("WG_VPS_PASS", fresh); setErr != nil && !strings.Contains(setErr.Error(), "disabled") {
		PrintWarn("не смог сохранить новый WG_VPS_PASS: " + setErr.Error())
	}
	return ConnectSSHWithAuth(state.Backend.Host, port, user, []ssh.AuthMethod{ssh.Password(fresh)}, kh, "backend")
}

func backendSSHAuthMethods(state *State, secrets *SecretStore, interactive bool) ([]ssh.AuthMethod, SecretSource, error) {
	switch normalizeBackendSSHAuth(state.Backend.SSHAuth) {
	case backendSSHAuthKey:
		keyPath := expandLocalPath(state.Backend.KeyPath)
		if keyPath == "" {
			return nil, SourceMissing, fmt.Errorf("missing VPS SSH key path")
		}
		passphrase := secrets.GetNonInteractive("WG_VPS_KEY_PASSPHRASE")
		signer, err := loadPrivateKeySigner(keyPath, passphrase)
		if err != nil && passphrase == "" && interactive && looksEncryptedKeyErr(err) {
			passphrase = AskSecret("SSH key passphrase (Enter если ключ без пароля)")
			if passphrase != "" {
				if setErr := secrets.Set("WG_VPS_KEY_PASSPHRASE", passphrase); setErr != nil && !strings.Contains(setErr.Error(), "disabled") {
					PrintWarn("не смог сохранить WG_VPS_KEY_PASSPHRASE: " + setErr.Error())
				}
				signer, err = loadPrivateKeySigner(keyPath, passphrase)
			}
		}
		if err != nil {
			return nil, SourceMissing, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, SourceDisk, nil
	default:
		pass, source := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
		if pass == "" {
			return nil, SourceMissing, fmt.Errorf("missing VPS password")
		}
		return []ssh.AuthMethod{ssh.Password(pass)}, source, nil
	}
}

func backendSSHAuthMethodsNonInteractive(state *State, secrets *SecretStore) ([]ssh.AuthMethod, error) {
	switch normalizeBackendSSHAuth(state.Backend.SSHAuth) {
	case backendSSHAuthKey:
		keyPath := expandLocalPath(state.Backend.KeyPath)
		if keyPath == "" {
			return nil, fmt.Errorf("missing VPS SSH key path")
		}
		passphrase := secrets.GetNonInteractive("WG_VPS_KEY_PASSPHRASE")
		signer, err := loadPrivateKeySigner(keyPath, passphrase)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		pass := secrets.GetNonInteractive("WG_VPS_PASS")
		if pass == "" {
			return nil, fmt.Errorf("WG_VPS_PASS неизвестен")
		}
		return []ssh.AuthMethod{ssh.Password(pass)}, nil
	}
}

func isSSHAuthFailure(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unable to authenticate")
}

func looksEncryptedKeyErr(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "passphrase") || strings.Contains(lower, "encrypted")
}

func expandLocalPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), `"`)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				path = home
			} else if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
				path = filepath.Join(home, path[2:])
			}
		}
	}
	path = os.ExpandEnv(path)
	if strings.Contains(path, "%USERPROFILE%") {
		path = strings.ReplaceAll(path, "%USERPROFILE%", os.Getenv("USERPROFILE"))
	}
	if strings.Contains(path, "%APPDATA%") {
		path = strings.ReplaceAll(path, "%APPDATA%", os.Getenv("APPDATA"))
	}
	return filepath.Clean(path)
}
