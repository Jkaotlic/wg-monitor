package backend

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen    string          `yaml:"listen"`
	LogLevel  string          `yaml:"log_level"`
	DBPath    string          `yaml:"db_path"`
	Telegram  TelegramConfig  `yaml:"telegram"`
	Heartbeat HeartbeatConfig `yaml:"heartbeat"`
	State     StateConfig     `yaml:"state"`
	UI        UIConfig        `yaml:"ui"`
	Upstream  UpstreamConfig  `yaml:"upstream"`
	Retention RetentionConfig `yaml:"retention"`
}

// UpstreamConfig points the upstream version cache at the right GitHub repos.
// Both repo fields are optional — empty strings mean "skip that source"
// (the panel and smart-reply just won't surface that comparison).
// CacheTTL defaults to 12h.
type UpstreamConfig struct {
	AwgmgrRepo string        `yaml:"awgmgr_repo"`
	HrneoRepo  string        `yaml:"hrneo_repo"`
	CacheTTL   time.Duration `yaml:"cache_ttl"`
}

// RetentionConfig governs periodic DB maintenance: pruning old events,
// VACUUM to reclaim space, and WAL checkpointing. Each field 0 disables
// that operation independently — useful for tests or short-lived
// deployments.
//
// Defaults applied in LoadConfig:
//
//	EventsDays: 30
//	VacuumInterval: 168h (weekly)
//	WALCheckpointEvery: 1h
type RetentionConfig struct {
	EventsDays         int           `yaml:"events_days"`
	VacuumInterval     time.Duration `yaml:"vacuum_interval"`
	WALCheckpointEvery time.Duration `yaml:"wal_checkpoint_every"`
}

type TelegramConfig struct {
	BotTokenFile string `yaml:"bot_token_file"`
	BotToken     string `yaml:"-"`
	ChatID       int64  `yaml:"chat_id"`
	AdminUserID  int64  `yaml:"admin_user_id"`
}

type HeartbeatConfig struct {
	StaleAfterSec       int `yaml:"stale_after_sec"`         // legacy, applied to static if mobile_sec absent
	StaleAfterStaticSec int `yaml:"stale_after_static_sec"`  // override for static (home/office) routers
	StaleAfterMobileSec int `yaml:"stale_after_mobile_sec"`  // override for mobile (4G in-vehicle) routers
	ResumeGraceSec      int `yaml:"resume_grace_sec"`        // suppress OFFLINE this long after Report.Resumed=true
	ScanIntervalSec     int `yaml:"scan_interval_sec"`
}

type StateConfig struct {
	FailThreshold     int `yaml:"fail_threshold"`
	RecoveryThreshold int `yaml:"recovery_threshold"`
	RealertEverySec   int `yaml:"realert_every_sec"`
	RealertTickSec    int `yaml:"realert_tick_sec"`
	MuteCutoffHour    int `yaml:"mute_cutoff_hour"`
}

// UIConfig controls v0.6.0 ReplyKeyboard / smart-reply behaviour (spec §8).
type UIConfig struct {
	// DeleteUserCommandMessages — bot deletes the operator's
	// "📊 Что происходит?" message after the smart-reply is composed.
	// Disable (set to `false` in YAML) if the bot lacks `can_delete_messages`
	// admin right. Pointer type so omitted YAML defaults to true while
	// explicit `false` is honoured.
	DeleteUserCommandMessages *bool `yaml:"delete_user_command_messages"`
	// SmartReplyWithKeyboard — re-attach the topic-appropriate ReplyKeyboard
	// to every smart-reply message (mitigation for desktop-client bug
	// where ReplyKeyboard intermittently disappears). Pointer type so
	// explicit `false` is honoured.
	SmartReplyWithKeyboard *bool `yaml:"smart_reply_with_keyboard"`
	// DiagMaxChars — soft cap for code-fenced diag output before pagination
	// kicks in. TG raw limit is 4096; 3500 leaves room for fence and prefix.
	DiagMaxChars int `yaml:"diag_max_chars"`
	// CompatInlineKeyboard — replace the persistent ReplyKeyboardMarkup with
	// equivalent inline buttons attached to every bot reply. Workaround for
	// TG Desktop dropping the bottom-keyboard panel in forum topics; also
	// useful for users who never installed the mobile client. Default false
	// (mobile users get the proper bottom panel as before).
	CompatInlineKeyboard bool `yaml:"compat_inline_keyboard"`
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
	if cfg.Heartbeat.StaleAfterStaticSec == 0 {
		cfg.Heartbeat.StaleAfterStaticSec = cfg.Heartbeat.StaleAfterSec
	}
	if cfg.Heartbeat.StaleAfterMobileSec == 0 {
		cfg.Heartbeat.StaleAfterMobileSec = 60 * 60
	}
	if cfg.Heartbeat.ResumeGraceSec == 0 {
		cfg.Heartbeat.ResumeGraceSec = 90
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
	// UI defaults: pointer types let us distinguish "field omitted" (nil → apply default)
	// from "field explicitly set to false" (honour the user's choice).
	if cfg.UI.DeleteUserCommandMessages == nil {
		v := true
		cfg.UI.DeleteUserCommandMessages = &v
	}
	if cfg.UI.SmartReplyWithKeyboard == nil {
		v := true
		cfg.UI.SmartReplyWithKeyboard = &v
	}
	if cfg.UI.DiagMaxChars == 0 {
		cfg.UI.DiagMaxChars = 3500
	}
	if cfg.Upstream.CacheTTL == 0 {
		cfg.Upstream.CacheTTL = 12 * time.Hour
	}
	if cfg.Retention.EventsDays == 0 {
		cfg.Retention.EventsDays = 30
	}
	if cfg.Retention.VacuumInterval == 0 {
		cfg.Retention.VacuumInterval = 168 * time.Hour
	}
	if cfg.Retention.WALCheckpointEvery == 0 {
		cfg.Retention.WALCheckpointEvery = 1 * time.Hour
	}
	return &cfg, nil
}
