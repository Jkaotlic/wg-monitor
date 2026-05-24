package callbacks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultAmneziaSecretsPath = "/var/lib/wg-monitor/amnezia-premium.json"

type amneziaSecretFile struct {
	Version int                          `json:"version"`
	Routers map[string]amneziaRouterKeys `json:"routers"`
}

type amneziaRouterKeys struct {
	ActiveID string             `json:"active_id"`
	Keys     []amneziaStoredKey `json:"keys"`
}

type amneziaStoredKey struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	VPNKey string `json:"vpn_key"`
}

func (r *Router) amneziaSecretsPath() string {
	if strings.TrimSpace(r.cfg.AmneziaSecretsPath) != "" {
		return strings.TrimSpace(r.cfg.AmneziaSecretsPath)
	}
	return defaultAmneziaSecretsPath
}

func (r *Router) getAmneziaKey(userID int64) (string, error) {
	return r.getAmneziaKeyByID(userID, "")
}

func (r *Router) getAmneziaKeyByID(userID int64, keyID string) (string, error) {
	keys, err := r.listAmneziaKeys(userID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(keyID) == "" || keyID == "active" {
		keyID = keys.ActiveID
	}
	for _, key := range keys.Keys {
		if key.ID == keyID {
			return key.VPNKey, nil
		}
	}
	return "", nil
}

func (r *Router) amneziaStoredKey(userID int64, keyID string) (amneziaStoredKey, bool) {
	keys, err := r.listAmneziaKeys(userID)
	if err != nil {
		return amneziaStoredKey{}, false
	}
	if strings.TrimSpace(keyID) == "" || keyID == "active" {
		keyID = keys.ActiveID
	}
	for _, key := range keys.Keys {
		if key.ID == keyID {
			return key, true
		}
	}
	return amneziaStoredKey{}, false
}

func (r *Router) listAmneziaKeys(userID int64) (amneziaRouterKeys, error) {
	env, err := readAmneziaSecrets(r.amneziaSecretsPath())
	if err != nil {
		return amneziaRouterKeys{}, err
	}
	keys := env.Routers[strconv.FormatInt(userID, 10)]
	normalizeAmneziaRouterKeys(&keys)
	return keys, nil
}

func (r *Router) addAmneziaKey(userID int64, vpnKey string) (amneziaStoredKey, error) {
	vpnKey = strings.TrimSpace(vpnKey)
	if !strings.HasPrefix(vpnKey, "vpn://") {
		return amneziaStoredKey{}, fmt.Errorf("amnezia premium key must start with vpn://")
	}
	path := r.amneziaSecretsPath()
	env, err := readAmneziaSecrets(path)
	if err != nil {
		return amneziaStoredKey{}, err
	}
	routerID := strconv.FormatInt(userID, 10)
	keys := env.Routers[routerID]
	normalizeAmneziaRouterKeys(&keys)
	id := amneziaKeyID(vpnKey)
	for i, existing := range keys.Keys {
		if existing.ID == id {
			keys.Keys[i].VPNKey = vpnKey
			if keys.Keys[i].Label == "" {
				keys.Keys[i].Label = fmt.Sprintf("Ключ #%d", i+1)
			}
			keys.ActiveID = id
			env.Routers[routerID] = keys
			return keys.Keys[i], writeAmneziaSecrets(path, env)
		}
	}
	key := amneziaStoredKey{
		ID:     id,
		Label:  fmt.Sprintf("Ключ #%d", len(keys.Keys)+1),
		VPNKey: vpnKey,
	}
	keys.Keys = append(keys.Keys, key)
	keys.ActiveID = id
	env.Routers[routerID] = keys
	return key, writeAmneziaSecrets(path, env)
}

func (r *Router) deleteAmneziaKey(userID int64, keyID string) error {
	path := r.amneziaSecretsPath()
	env, err := readAmneziaSecrets(path)
	if err != nil {
		return err
	}
	routerID := strconv.FormatInt(userID, 10)
	keys := env.Routers[routerID]
	normalizeAmneziaRouterKeys(&keys)
	next := keys.Keys[:0]
	deletedActive := keys.ActiveID == keyID
	for _, key := range keys.Keys {
		if key.ID == keyID {
			continue
		}
		next = append(next, key)
	}
	keys.Keys = next
	if len(keys.Keys) == 0 {
		delete(env.Routers, routerID)
		return writeAmneziaSecrets(path, env)
	}
	if deletedActive || keys.ActiveID == "" {
		keys.ActiveID = keys.Keys[0].ID
	}
	env.Routers[routerID] = keys
	return writeAmneziaSecrets(path, env)
}

func readAmneziaSecrets(path string) (amneziaSecretFile, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return emptyAmneziaSecretFile(), nil
	}
	if err != nil {
		return amneziaSecretFile{}, fmt.Errorf("read amnezia secrets: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return emptyAmneziaSecretFile(), nil
	}
	var env amneziaSecretFile
	if err := json.Unmarshal(body, &env); err == nil && (env.Version != 0 || env.Routers != nil) {
		if env.Version == 0 {
			env.Version = 1
		}
		if env.Routers == nil {
			env.Routers = map[string]amneziaRouterKeys{}
		}
		for routerID, keys := range env.Routers {
			normalizeAmneziaRouterKeys(&keys)
			env.Routers[routerID] = keys
		}
		return env, nil
	}
	var legacy map[string]string
	if err := json.Unmarshal(body, &legacy); err != nil {
		return amneziaSecretFile{}, fmt.Errorf("parse amnezia secrets: %w", err)
	}
	env = emptyAmneziaSecretFile()
	for routerID, vpnKey := range legacy {
		vpnKey = strings.TrimSpace(vpnKey)
		if vpnKey == "" {
			continue
		}
		id := amneziaKeyID(vpnKey)
		env.Routers[routerID] = amneziaRouterKeys{
			ActiveID: id,
			Keys: []amneziaStoredKey{{
				ID:     id,
				Label:  "Ключ #1",
				VPNKey: vpnKey,
			}},
		}
	}
	return env, nil
}

func writeAmneziaSecrets(path string, env amneziaSecretFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create amnezia secrets dir: %w", err)
	}
	if env.Version == 0 {
		env.Version = 1
	}
	if env.Routers == nil {
		env.Routers = map[string]amneziaRouterKeys{}
	}
	body, err := json.MarshalIndent(env, "", "  ")
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

func emptyAmneziaSecretFile() amneziaSecretFile {
	return amneziaSecretFile{Version: 1, Routers: map[string]amneziaRouterKeys{}}
}

func normalizeAmneziaRouterKeys(keys *amneziaRouterKeys) {
	seen := map[string]bool{}
	out := keys.Keys[:0]
	activeSeen := false
	for i, key := range keys.Keys {
		key.VPNKey = strings.TrimSpace(key.VPNKey)
		if key.VPNKey == "" {
			continue
		}
		if key.ID == "" {
			key.ID = amneziaKeyID(key.VPNKey)
		}
		if seen[key.ID] {
			continue
		}
		seen[key.ID] = true
		if key.Label == "" {
			key.Label = fmt.Sprintf("Ключ #%d", i+1)
		}
		if key.ID == keys.ActiveID {
			activeSeen = true
		}
		out = append(out, key)
	}
	keys.Keys = out
	if len(keys.Keys) == 0 {
		keys.ActiveID = ""
		return
	}
	if keys.ActiveID == "" || !activeSeen {
		keys.ActiveID = keys.Keys[0].ID
	}
}

func amneziaKeyID(vpnKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(vpnKey)))
	return fmt.Sprintf("%x", sum[:6])
}
