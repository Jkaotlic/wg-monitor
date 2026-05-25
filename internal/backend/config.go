package backend

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen            string                   `yaml:"listen"`
	LogLevel          string                   `yaml:"log_level"`
	DBPath            string                   `yaml:"db_path"`
	Telegram          TelegramConfig           `yaml:"telegram"`
	Wizard            WizardConfig             `yaml:"wizard"`
	Heartbeat         HeartbeatConfig          `yaml:"heartbeat"`
	State             StateConfig              `yaml:"state"`
	UI                UIConfig                 `yaml:"ui"`
	Upstream          UpstreamConfig           `yaml:"upstream"`
	Retention         RetentionConfig          `yaml:"retention"`
	RateLimit         RateLimitConfig          `yaml:"rate_limit"`
	Amnezia           AmneziaConfig            `yaml:"amnezia_premium"`
	SelfHostedAmnezia selfhostedamnezia.Config `yaml:"amnezia_selfhosted"`
	HideMy            HideMyConfig             `yaml:"hidemyname"`
}

// AmneziaConfig wires the optional Amnezia Premium cabinet helper.
// SecretsPath intentionally lives outside state.db so Telegram DB backups do
// not carry vpn:// subscription keys.
type AmneziaConfig struct {
	BaseURL     string `yaml:"base_url"`
	SecretsPath string `yaml:"secrets_path"`
}

// HideMyConfig wires the optional HideMy.name access-code helper.
// SecretsPath intentionally lives outside state.db so Telegram DB backups do
// not carry provider access codes.
type HideMyConfig struct {
	BaseURL     string `yaml:"base_url"`
	SecretsPath string `yaml:"secrets_path"`
}

// RateLimitConfig governs per-userID throttling on /v1/report (API-06).
// Zero values disable rate limiting altogether (default in tests; production
// LoadConfig fills sane defaults).
type RateLimitConfig struct {
	ReportPerSec float64 `yaml:"report_per_sec"`
	ReportBurst  int     `yaml:"report_burst"`
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

// WizardConfig wires the optional /v1/wizard/* endpoints. When TokenFile
// is empty OR points to a missing/empty file, the endpoints are NOT
// registered (fail-closed). To enable: put a 64-hex token (any opaque
// secret really) into the file, mode 0600 root:wgmonitor.
type WizardConfig struct {
	TokenFile string `yaml:"token_file"`
	// Token is loaded from TokenFile at config-load time. Empty → feature off.
	Token string `yaml:"-"`
}

type HeartbeatConfig struct {
	StaleAfterSec       int   `yaml:"stale_after_sec"`        // legacy, applied to static if mobile_sec absent
	StaleAfterStaticSec int   `yaml:"stale_after_static_sec"` // override for static (home/office) routers
	StaleAfterMobileSec int   `yaml:"stale_after_mobile_sec"` // legacy: applied only if mobile_lifecycle=false
	ResumeGraceSec      int   `yaml:"resume_grace_sec"`       // suppress OFFLINE this long after Report.Resumed=true
	ScanIntervalSec     int   `yaml:"scan_interval_sec"`
	MobileLifecycle     *bool `yaml:"mobile_lifecycle"`       // NEW: default true. Wake-card on Resumed=true + one-shot sleep-info instead of HARD-OFFLINE.
	MobileSleepAfterSec int   `yaml:"mobile_sleep_after_sec"` // default 1800. After this many seconds of mobile silence, send one sleep info-card.
}

type StateConfig struct {
	FailThreshold          int `yaml:"fail_threshold"`
	RecoveryThreshold      int `yaml:"recovery_threshold"`
	MobileFailThreshold    int `yaml:"mobile_fail_threshold"`
	NoisyFailThreshold     int `yaml:"noisy_fail_threshold"`
	NoisyRecoveryThreshold int `yaml:"noisy_recovery_threshold"`
	RealertEverySec        int `yaml:"realert_every_sec"`
	MobileRealertEverySec  int `yaml:"mobile_realert_every_sec"`
	RealertTickSec         int `yaml:"realert_tick_sec"`
	MuteCutoffHour         int `yaml:"mute_cutoff_hour"`
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
	// TG Desktop dropping the bottom-keyboard panel in forum topics. Pointer
	// type so omitted YAML defaults to true while explicit false is honoured.
	CompatInlineKeyboard *bool `yaml:"compat_inline_keyboard"`
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
	// Wizard token is optional — empty file/path means the /v1/wizard/* feature
	// is disabled. Missing file is NOT an error (the wizard pre-rc1 didn't write
	// one; backend upgrades without immediate wizard upgrade should still boot).
	if cfg.Wizard.TokenFile != "" {
		if b, err := os.ReadFile(cfg.Wizard.TokenFile); err == nil {
			cfg.Wizard.Token = strings.TrimSpace(string(b))
		}
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
	if cfg.Heartbeat.MobileLifecycle == nil {
		t := true
		cfg.Heartbeat.MobileLifecycle = &t
	}
	if cfg.Heartbeat.MobileSleepAfterSec == 0 {
		cfg.Heartbeat.MobileSleepAfterSec = 1800
	}
	if cfg.Heartbeat.MobileLifecycle != nil && *cfg.Heartbeat.MobileLifecycle && cfg.Heartbeat.MobileSleepAfterSec < 1800 {
		cfg.Heartbeat.MobileSleepAfterSec = 1800
	}
	if cfg.State.FailThreshold == 0 {
		cfg.State.FailThreshold = 3
	}
	if cfg.State.RecoveryThreshold == 0 {
		cfg.State.RecoveryThreshold = 2
	}
	if cfg.State.MobileFailThreshold == 0 {
		cfg.State.MobileFailThreshold = 6
	}
	if cfg.State.NoisyFailThreshold == 0 {
		cfg.State.NoisyFailThreshold = 6
	}
	if cfg.State.NoisyRecoveryThreshold == 0 {
		cfg.State.NoisyRecoveryThreshold = 3
	}
	if cfg.State.RealertEverySec == 0 {
		cfg.State.RealertEverySec = 6 * 3600
	}
	if cfg.State.MobileRealertEverySec == 0 {
		cfg.State.MobileRealertEverySec = 6 * 3600
	}
	if cfg.State.RealertTickSec == 0 {
		cfg.State.RealertTickSec = 300
	}
	if cfg.State.MuteCutoffHour == 0 {
		cfg.State.MuteCutoffHour = 9
	}
	// Rate-limit defaults: 0.2 r/s × burst 5 = "1 every 5s sustained, burst
	// of 5". Reporter interval is 60s so legitimate traffic uses ~1/60 r/s
	// per agent — room for transient ForceResumed spikes without limiting.
	if cfg.RateLimit.ReportPerSec == 0 {
		cfg.RateLimit.ReportPerSec = 0.2
	}
	if cfg.RateLimit.ReportBurst == 0 {
		cfg.RateLimit.ReportBurst = 5
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
	if cfg.UI.CompatInlineKeyboard == nil {
		v := true
		cfg.UI.CompatInlineKeyboard = &v
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
	if cfg.Amnezia.SecretsPath == "" {
		cfg.Amnezia.SecretsPath = "/var/lib/wg-monitor/amnezia-premium.json"
	}
	if cfg.SelfHostedAmnezia.StorePath == "" {
		cfg.SelfHostedAmnezia.StorePath = selfhostedamnezia.DefaultStorePath
	}
	if cfg.HideMy.BaseURL == "" {
		cfg.HideMy.BaseURL = "https://hide-my-name.cloud"
	}
	if cfg.HideMy.SecretsPath == "" {
		cfg.HideMy.SecretsPath = "/var/lib/wg-monitor/hidemyname.json"
	}
	return &cfg, nil
}
