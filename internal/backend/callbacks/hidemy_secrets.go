package callbacks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/hidemy"
)

const defaultHideMySecretsPath = "/var/lib/wg-monitor/hidemyname.json"

type hideMySecretFile struct {
	Version int                    `json:"version"`
	Routers map[string]hideMyCodes `json:"routers"`
}

type hideMyCodes struct {
	ActiveID string             `json:"active_id"`
	Codes    []hideMyStoredCode `json:"codes"`
}

type hideMyStoredCode struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	AccessCode string `json:"access_code"`
}

func (r *Router) hideMySecretsPath() string {
	if strings.TrimSpace(r.cfg.HideMySecretsPath) != "" {
		return strings.TrimSpace(r.cfg.HideMySecretsPath)
	}
	return defaultHideMySecretsPath
}

func (r *Router) getHideMyCode(userID int64) (string, error) {
	return r.getHideMyCodeByID(userID, "")
}

func (r *Router) getHideMyCodeByID(userID int64, codeID string) (string, error) {
	codes, err := r.listHideMyCodes(userID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(codeID) == "" || codeID == "active" {
		codeID = codes.ActiveID
	}
	for _, code := range codes.Codes {
		if code.ID == codeID {
			return code.AccessCode, nil
		}
	}
	return "", nil
}

func (r *Router) hideMyStoredCode(userID int64, codeID string) (hideMyStoredCode, bool) {
	codes, err := r.listHideMyCodes(userID)
	if err != nil {
		return hideMyStoredCode{}, false
	}
	if strings.TrimSpace(codeID) == "" || codeID == "active" {
		codeID = codes.ActiveID
	}
	for _, code := range codes.Codes {
		if code.ID == codeID {
			return code, true
		}
	}
	return hideMyStoredCode{}, false
}

func (r *Router) listHideMyCodes(userID int64) (hideMyCodes, error) {
	env, err := readHideMySecrets(r.hideMySecretsPath())
	if err != nil {
		return hideMyCodes{}, err
	}
	codes := env.Routers[strconv.FormatInt(userID, 10)]
	normalizeHideMyCodes(&codes)
	return codes, nil
}

func (r *Router) addHideMyCode(userID int64, accessCode string) (hideMyStoredCode, error) {
	accessCode = strings.TrimSpace(accessCode)
	if !hidemy.ValidAccessCode(accessCode) {
		return hideMyStoredCode{}, fmt.Errorf("hidemy access code must be 10-20 digits")
	}
	path := r.hideMySecretsPath()
	env, err := readHideMySecrets(path)
	if err != nil {
		return hideMyStoredCode{}, err
	}
	routerID := strconv.FormatInt(userID, 10)
	codes := env.Routers[routerID]
	normalizeHideMyCodes(&codes)
	id := hideMyCodeID(accessCode)
	for i, existing := range codes.Codes {
		if existing.ID == id {
			codes.Codes[i].AccessCode = accessCode
			if codes.Codes[i].Label == "" {
				codes.Codes[i].Label = fmt.Sprintf("Code #%d", i+1)
			}
			codes.ActiveID = id
			env.Routers[routerID] = codes
			return codes.Codes[i], writeHideMySecrets(path, env)
		}
	}
	code := hideMyStoredCode{ID: id, Label: fmt.Sprintf("Code #%d", len(codes.Codes)+1), AccessCode: accessCode}
	codes.Codes = append(codes.Codes, code)
	codes.ActiveID = id
	env.Routers[routerID] = codes
	return code, writeHideMySecrets(path, env)
}

func (r *Router) deleteHideMyCode(userID int64, codeID string) error {
	path := r.hideMySecretsPath()
	env, err := readHideMySecrets(path)
	if err != nil {
		return err
	}
	routerID := strconv.FormatInt(userID, 10)
	codes := env.Routers[routerID]
	normalizeHideMyCodes(&codes)
	next := codes.Codes[:0]
	deletedActive := codes.ActiveID == codeID
	for _, code := range codes.Codes {
		if code.ID == codeID {
			continue
		}
		next = append(next, code)
	}
	codes.Codes = next
	if len(codes.Codes) == 0 {
		delete(env.Routers, routerID)
		return writeHideMySecrets(path, env)
	}
	if deletedActive || codes.ActiveID == "" {
		codes.ActiveID = codes.Codes[0].ID
	}
	env.Routers[routerID] = codes
	return writeHideMySecrets(path, env)
}

func readHideMySecrets(path string) (hideMySecretFile, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return emptyHideMySecretFile(), nil
	}
	if err != nil {
		return hideMySecretFile{}, fmt.Errorf("read hidemy secrets: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return emptyHideMySecretFile(), nil
	}
	var env hideMySecretFile
	if err := json.Unmarshal(body, &env); err != nil {
		return hideMySecretFile{}, fmt.Errorf("parse hidemy secrets: %w", err)
	}
	if env.Version == 0 {
		env.Version = 1
	}
	if env.Routers == nil {
		env.Routers = map[string]hideMyCodes{}
	}
	for routerID, codes := range env.Routers {
		normalizeHideMyCodes(&codes)
		env.Routers[routerID] = codes
	}
	return env, nil
}

func writeHideMySecrets(path string, env hideMySecretFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create hidemy secrets dir: %w", err)
	}
	if env.Version == 0 {
		env.Version = 1
	}
	if env.Routers == nil {
		env.Routers = map[string]hideMyCodes{}
	}
	body, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hidemy secrets: %w", err)
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(dir, ".hidemyname-*.tmp")
	if err != nil {
		return fmt.Errorf("create hidemy secrets temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod hidemy secrets temp: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write hidemy secrets temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close hidemy secrets temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace hidemy secrets: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func emptyHideMySecretFile() hideMySecretFile {
	return hideMySecretFile{Version: 1, Routers: map[string]hideMyCodes{}}
}

func normalizeHideMyCodes(codes *hideMyCodes) {
	seen := map[string]bool{}
	out := codes.Codes[:0]
	activeSeen := false
	for i, code := range codes.Codes {
		code.AccessCode = strings.TrimSpace(code.AccessCode)
		if code.AccessCode == "" {
			continue
		}
		if code.ID == "" {
			code.ID = hideMyCodeID(code.AccessCode)
		}
		if seen[code.ID] {
			continue
		}
		seen[code.ID] = true
		if code.Label == "" {
			code.Label = fmt.Sprintf("Code #%d", i+1)
		}
		if code.ID == codes.ActiveID {
			activeSeen = true
		}
		out = append(out, code)
	}
	codes.Codes = out
	if len(codes.Codes) == 0 {
		codes.ActiveID = ""
		return
	}
	if codes.ActiveID == "" || !activeSeen {
		codes.ActiveID = codes.Codes[0].ID
	}
}

func hideMyCodeID(accessCode string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(accessCode)))
	return fmt.Sprintf("%x", sum[:6])
}
