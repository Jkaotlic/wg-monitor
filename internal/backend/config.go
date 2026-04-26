package backend

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

var nicknameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

type Config struct {
	Listen   string        `yaml:"listen"`
	LogLevel string        `yaml:"log_level"`
	Agents   []AgentConfig `yaml:"agents"`
}

type AgentConfig struct {
	Nickname string `yaml:"nickname"`
	Token    string `yaml:"token"`
}

func LoadConfig(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8080"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	seen := make(map[string]struct{}, len(cfg.Agents))
	for i, a := range cfg.Agents {
		if !nicknameRegexp.MatchString(a.Nickname) {
			return nil, fmt.Errorf("agents[%d]: nickname %q must match %s", i, a.Nickname, nicknameRegexp)
		}
		if len(a.Token) < 32 {
			return nil, fmt.Errorf("agents[%d] %s: token must be at least 32 chars", i, a.Nickname)
		}
		if _, dup := seen[a.Nickname]; dup {
			return nil, fmt.Errorf("agents[%d]: duplicate nickname %q", i, a.Nickname)
		}
		seen[a.Nickname] = struct{}{}
	}
	return &cfg, nil
}
