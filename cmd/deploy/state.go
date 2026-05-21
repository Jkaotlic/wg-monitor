package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

const CurrentSchemaVersion = 1

type State struct {
	SchemaVersion int           `toml:"schema_version"`
	Backend       BackendState  `toml:"backend"`
	Telegram      TelegramState `toml:"telegram"`
	Agents        []AgentState  `toml:"agents"`
}

type BackendState struct {
	Host                string `toml:"host"`
	Port                int    `toml:"port"`
	User                string `toml:"user"`
	SSHAuth             string `toml:"ssh_auth,omitempty"`
	KeyPath             string `toml:"key_path,omitempty"`
	Domain              string `toml:"domain"`
	LastDeploy          string `toml:"last_deploy"`
	LastDeployedVersion string `toml:"last_deployed_version"`
}

type TelegramState struct {
	ChatID      int64 `toml:"chat_id"`
	AdminUserID int64 `toml:"admin_user_id"`
}

type AgentState struct {
	Nickname            string `toml:"nickname"`
	Host                string `toml:"host"`
	Port                int    `toml:"port"`
	User                string `toml:"user"`
	Arch                string `toml:"arch"`
	ThreadID            int    `toml:"thread_id"`
	Kind                string `toml:"kind,omitempty"` // "static" (default) or "mobile" — пробрасывается в DB и решает StaleAfterMobileSec в backend
	Ring                string `toml:"ring,omitempty"` // rollout ring: canary, beta, stable (empty means stable)
	LastDeploy          string `toml:"last_deploy"`
	LastDeployedVersion string `toml:"last_deployed_version"`
	PendingVersion      string `toml:"pending_version,omitempty"`
	PendingSince        string `toml:"pending_since,omitempty"`
	DeployMode          string `toml:"deploy_mode,omitempty"` // "awgm" default supported path, "legacy_ssh" for break-glass recovery
	AWGMURL             string `toml:"awgm_url,omitempty"`    // public AWG Manager base URL, usually KeenDNS web-app URL
	AWGMAuth            string `toml:"awgm_auth,omitempty"`   // credential source label only; password lives in SecretStore
	// ExpectedMAC pins the physical identity of the router so a fresh
	// install-agent against a wrong host (operator forgot to switch SSTP,
	// or another router took 192.168.31.1 on the same LAN) bails BEFORE
	// touching /opt. Captured automatically on first successful install,
	// verified before every subsequent SSH-write op. Lowercase, no colons.
	ExpectedMAC string `toml:"expected_mac,omitempty"`
	// PreferredIface caches the network interface name (as reported by
	// net.Interface.Name — e.g. "Ethernet 2" on Windows, "tun0" on linux)
	// that successfully reached this router on the previous deploy. Layer-1
	// path discovery tries it first on subsequent runs; on failure it falls
	// back to full enumeration and overwrites the cache. Empty = no cache,
	// do full enumeration.
	PreferredIface string `toml:"preferred_iface,omitempty"`
}

// LoadState reads wizard.toml from path. Missing file → returns default state, no error.
// Invalid schema_version → returns error.
func LoadState(path string) (*State, error) {
	s := &State{SchemaVersion: CurrentSchemaVersion}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.SchemaVersion > CurrentSchemaVersion {
		return nil, fmt.Errorf("wizard.toml schema_version=%d not supported (max=%d). Update wg-monitor-deploy",
			s.SchemaVersion, CurrentSchemaVersion)
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = CurrentSchemaVersion
	}
	return s, nil
}

func SaveState(path string, s *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := toml.NewEncoder(f)
	if err := enc.Encode(s); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}

// DefaultStatePath returns the OS-appropriate config location.
// Priority used by callers:
//  1. --config flag (handled in main)
//  2. ./wizard.toml if exists in cwd (handled in main)
//  3. this default
func DefaultStatePath() string {
	if runtime.GOOS == "windows" {
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			appdata = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return filepath.Join(appdata, "wg-monitor-deploy", "wizard.toml")
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		cfg = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(cfg, "wg-monitor-deploy", "wizard.toml")
}

// ProfileStatePath returns an isolated wizard.toml path for a named rollout
// profile. The default profile intentionally keeps the historic location.
func ProfileStatePath(profile string) string {
	profile = sanitizeProfileName(profile)
	if profile == "" || profile == "default" {
		return DefaultStatePath()
	}
	if runtime.GOOS == "windows" {
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			appdata = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return filepath.Join(appdata, "wg-monitor-deploy", "profiles", profile, "wizard.toml")
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		cfg = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(cfg, "wg-monitor-deploy", "profiles", profile, "wizard.toml")
}

func sanitizeProfileName(profile string) string {
	out := ""
	for _, r := range profile {
		switch {
		case r >= 'a' && r <= 'z':
			out += string(r)
		case r >= 'A' && r <= 'Z':
			out += string(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			out += string(r)
		case r == '-' || r == '_':
			out += string(r)
		}
	}
	return out
}

// FindAgent returns a pointer to the agent with the given nickname, or nil.
func (s *State) FindAgent(nickname string) *AgentState {
	for i := range s.Agents {
		if s.Agents[i].Nickname == nickname {
			return &s.Agents[i]
		}
	}
	return nil
}
