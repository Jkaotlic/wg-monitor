package backend

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen    string          `yaml:"listen"`
	LogLevel  string          `yaml:"log_level"`
	DBPath    string          `yaml:"db_path"`
	Telegram  TelegramConfig  `yaml:"telegram"`
	Heartbeat HeartbeatConfig `yaml:"heartbeat"`
	State     StateConfig     `yaml:"state"`
}

type TelegramConfig struct {
	BotTokenFile string `yaml:"bot_token_file"`
	BotToken     string `yaml:"-"`
	ChatID       int64  `yaml:"chat_id"`
	AdminUserID  int64  `yaml:"admin_user_id"`
}

type HeartbeatConfig struct {
	StaleAfterSec   int `yaml:"stale_after_sec"`
	ScanIntervalSec int `yaml:"scan_interval_sec"`
}

type StateConfig struct {
	FailThreshold     int `yaml:"fail_threshold"`
	RecoveryThreshold int `yaml:"recovery_threshold"`
	RealertEverySec   int `yaml:"realert_every_sec"`
	RealertTickSec    int `yaml:"realert_tick_sec"`
	MuteCutoffHour    int `yaml:"mute_cutoff_hour"`
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
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("db_path is required")
	}
	if cfg.Telegram.BotTokenFile == "" {
		return nil, fmt.Errorf("telegram.bot_token_file is required")
	}
	tokBytes, err := os.ReadFile(cfg.Telegram.BotTokenFile)
	if err != nil {
		return nil, fmt.Errorf("read bot_token_file: %w", err)
	}
	cfg.Telegram.BotToken = strings.TrimSpace(string(tokBytes))
	if cfg.Telegram.BotToken == "" {
		return nil, fmt.Errorf("bot_token_file is empty")
	}
	if cfg.Telegram.ChatID == 0 {
		return nil, fmt.Errorf("telegram.chat_id is required")
	}
	if cfg.Telegram.AdminUserID == 0 {
		return nil, fmt.Errorf("telegram.admin_user_id is required")
	}
	if cfg.Heartbeat.StaleAfterSec == 0 {
		cfg.Heartbeat.StaleAfterSec = 300
	}
	if cfg.Heartbeat.ScanIntervalSec == 0 {
		cfg.Heartbeat.ScanIntervalSec = 30
	}
	if cfg.State.FailThreshold == 0 {
		cfg.State.FailThreshold = 3
	}
	if cfg.State.RecoveryThreshold == 0 {
		cfg.State.RecoveryThreshold = 2
	}
	if cfg.State.RealertEverySec == 0 {
		cfg.State.RealertEverySec = 6 * 3600
	}
	if cfg.State.RealertTickSec == 0 {
		cfg.State.RealertTickSec = 300
	}
	if cfg.State.MuteCutoffHour == 0 {
		cfg.State.MuteCutoffHour = 9
	}
	return &cfg, nil
}
