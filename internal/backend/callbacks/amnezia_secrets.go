package callbacks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultAmneziaSecretsPath = "/var/lib/wg-monitor/amnezia-premium.json"

func (r *Router) amneziaSecretsPath() string {
	if strings.TrimSpace(r.cfg.AmneziaSecretsPath) != "" {
		return strings.TrimSpace(r.cfg.AmneziaSecretsPath)
	}
	return defaultAmneziaSecretsPath
}

func (r *Router) getAmneziaKey(userID int64) (string, error) {
	keys, err := readAmneziaSecrets(r.amneziaSecretsPath())
	if err != nil {
		return "", err
	}
	return keys[strconv.FormatInt(userID, 10)], nil
}

func (r *Router) setAmneziaKey(userID int64, vpnKey string) error {
	vpnKey = strings.TrimSpace(vpnKey)
	if !strings.HasPrefix(vpnKey, "vpn://") {
		return fmt.Errorf("amnezia premium key must start with vpn://")
	}
	path := r.amneziaSecretsPath()
	keys, err := readAmneziaSecrets(path)
	if err != nil {
		return err
	}
	keys[strconv.FormatInt(userID, 10)] = vpnKey
	return writeAmneziaSecrets(path, keys)
}

func readAmneziaSecrets(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read amnezia secrets: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return map[string]string{}, nil
	}
	var keys map[string]string
	if err := json.Unmarshal(body, &keys); err != nil {
		return nil, fmt.Errorf("parse amnezia secrets: %w", err)
	}
	if keys == nil {
		keys = map[string]string{}
	}
	return keys, nil
}

func writeAmneziaSecrets(path string, keys map[string]string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create amnezia secrets dir: %w", err)
	}
	body, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal amnezia secrets: %w", err)
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(dir, ".amnezia-premium-*.tmp")
	if err != nil {
		return fmt.Errorf("create amnezia secrets temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod amnezia secrets temp: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write amnezia secrets temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close amnezia secrets temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace amnezia secrets: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}
