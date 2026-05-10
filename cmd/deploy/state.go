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
	LastDeploy          string `toml:"last_deploy"`
	LastDeployedVersion string `toml:"last_deployed_version"`
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

// FindAgent returns a pointer to the agent with the given nickname, or nil.
func (s *State) FindAgent(nickname string) *AgentState {
	for i := range s.Agents {
		if s.Agents[i].Nickname == nickname {
			return &s.Agents[i]
		}
	}
	return nil
}
