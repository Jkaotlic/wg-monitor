package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "secret-bot-token-xyz")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/state.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -1003651873378
  admin_user_id: 136513775
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.BotToken != "secret-bot-token-xyz" {
		t.Fatalf("token: %q", cfg.Telegram.BotToken)
	}
	if cfg.State.FailThreshold != 3 || cfg.State.RecoveryThreshold != 2 {
		t.Fatalf("state defaults: %+v", cfg.State)
	}
	if cfg.State.NoisyFailThreshold != 6 || cfg.State.NoisyRecoveryThreshold != 3 {
		t.Fatalf("noisy state defaults: %+v", cfg.State)
	}
	if cfg.Heartbeat.StaleAfterSec != 300 {
		t.Fatalf("hb default: %d", cfg.Heartbeat.StaleAfterSec)
	}
	if cfg.Amnezia.SecretsPath != "/var/lib/wg-monitor/amnezia-premium.json" {
		t.Fatalf("amnezia secrets default: %q", cfg.Amnezia.SecretsPath)
	}
	if cfg.SelfHostedAmnezia.StorePath != "/var/lib/wg-monitor/amnezia-selfhosted.json" {
		t.Fatalf("self-hosted amnezia store default: %q", cfg.SelfHostedAmnezia.StorePath)
	}
	if cfg.HideMy.BaseURL != "https://hide-my-name.cloud" {
		t.Fatalf("hidemy base default: %q", cfg.HideMy.BaseURL)
	}
	if cfg.HideMy.SecretsPath != "/var/lib/wg-monitor/hidemyname.json" {
		t.Fatalf("hidemy secrets default: %q", cfg.HideMy.SecretsPath)
	}
}

func TestLoadConfigDashboardDefaultsDisabled(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "bot-token", "secret-bot-token-xyz")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/state.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -1003651873378
  admin_user_id: 136513775
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dashboard.Enabled {
		t.Fatal("dashboard must be disabled by default")
	}
	if cfg.Dashboard.Token != "" {
		t.Fatalf("dashboard token must stay empty when disabled, got %q", cfg.Dashboard.Token)
	}
}

func TestLoadConfigDashboardLoadsTokenWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	botPath := writeFile(t, dir, "bot-token", "secret-bot-token-xyz")
	dashboardPath := writeFile(t, dir, "dashboard-token", "dashboard-secret\n")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/state.db
telegram:
  bot_token_file: `+botPath+`
  chat_id: -1003651873378
  admin_user_id: 136513775
dashboard:
  enabled: true
  token_file: `+dashboardPath+`
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Dashboard.Enabled {
		t.Fatal("dashboard should be enabled")
	}
	if cfg.Dashboard.Token != "dashboard-secret" {
		t.Fatalf("dashboard token=%q", cfg.Dashboard.Token)
	}
}

func TestLoadConfigDashboardFailsClosedWhenEnabledWithoutToken(t *testing.T) {
	dir := t.TempDir()
	botPath := writeFile(t, dir, "bot-token", "secret-bot-token-xyz")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/state.db
telegram:
  bot_token_file: `+botPath+`
  chat_id: -1003651873378
  admin_user_id: 136513775
dashboard:
  enabled: true
  token_file: `+filepath.Join(dir, "missing-dashboard-token")+`
`)
	if _, err := LoadConfig(cfgPath); err == nil || !strings.Contains(err.Error(), "dashboard.token_file") {
		t.Fatalf("expected dashboard token_file error, got %v", err)
	}
}

func TestLoadConfigTrimsPublicBaseURL(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "secret-bot-token-xyz")
	cfgPath := writeFile(t, dir, "c.yaml", `
public_base_url: " https://wg.example.test/ "
db_path: /tmp/state.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -1003651873378
  admin_user_id: 136513775
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicBaseURL != "https://wg.example.test" {
		t.Fatalf("PublicBaseURL = %q, want trimmed URL", cfg.PublicBaseURL)
	}
}

func TestLoadConfigPublicIPValidAndInvalid(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "x")
	base := func(publicIP string) string {
		return `
db_path: /tmp/state.db
public_ip: ` + publicIP + `
telegram:
  bot_token_file: ` + tokPath + `
  chat_id: -100
  admin_user_id: 1
`
	}

	// Valid IPv4 loads and is trimmed.
	cfg, err := LoadConfig(writeFile(t, dir, "ok.yaml", base(`" 128.0.142.207 "`)))
	if err != nil {
		t.Fatalf("valid public_ip rejected: %v", err)
	}
	if cfg.PublicIP != "128.0.142.207" {
		t.Fatalf("PublicIP = %q, want trimmed IPv4", cfg.PublicIP)
	}

	// A non-IP string is rejected (fail fast, not a silent broken --resolve).
	if _, err := LoadConfig(writeFile(t, dir, "bad.yaml", base("not-an-ip"))); err == nil {
		t.Fatal("expected error for non-IP public_ip")
	}

	// IPv6 is rejected — the feature is IPv4-only by design.
	if _, err := LoadConfig(writeFile(t, dir, "v6.yaml", base(`"2001:db8::1"`))); err == nil {
		t.Fatal("expected error for IPv6 public_ip (IPv4-only)")
	}

	// Empty/omitted stays empty (unchanged behaviour, no --resolve).
	cfg, err = LoadConfig(writeFile(t, dir, "empty.yaml", base(`""`)))
	if err != nil {
		t.Fatalf("empty public_ip should be allowed: %v", err)
	}
	if cfg.PublicIP != "" {
		t.Fatalf("PublicIP = %q, want empty", cfg.PublicIP)
	}
}

func TestLoadConfigRejectsMissingChatID(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "x")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/state.db
telegram:
  bot_token_file: `+tokPath+`
  admin_user_id: 1
`)
	if _, err := LoadConfig(cfgPath); err == nil {
		t.Fatal("expected chat_id required")
	}
}

func TestLoadConfigDefaultsForStage2(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "test-token")
	cfgPath := writeFile(t, dir, "cfg.yaml", `
db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -100
  admin_user_id: 12345
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.State.MuteCutoffHour == nil || *cfg.State.MuteCutoffHour != 9 {
		t.Errorf("MuteCutoffHour default: got %v, want 9", cfg.State.MuteCutoffHour)
	}
	if cfg.State.RealertTickSec != 300 {
		t.Errorf("RealertTickSec default: got %d, want 300", cfg.State.RealertTickSec)
	}
}

// TestLoadConfig_MuteCutoffHourZeroHonored pins C14: MuteCutoffHour==0 must be
// distinguishable from "not configured" so an operator can set literal
// midnight. A pointer type lets nil mean "apply the default (9)" while an
// explicit 0 in YAML is honoured as-is.
func TestLoadConfig_MuteCutoffHourZeroHonored(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "test-token")
	cfgPath := writeFile(t, dir, "cfg.yaml", `
db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -100
  admin_user_id: 12345
state:
  mute_cutoff_hour: 0
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.State.MuteCutoffHour == nil {
		t.Fatal("MuteCutoffHour: explicit 0 was overwritten to nil")
	}
	if *cfg.State.MuteCutoffHour != 0 {
		t.Errorf("MuteCutoffHour: got %d, want 0 (honoured, not defaulted to 9)", *cfg.State.MuteCutoffHour)
	}
}

func TestConfigUIDefaults(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "abc")
	cfgPath := writeFile(t, dir, "c.yaml", `
listen: ":8080"
db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -100
  admin_user_id: 1
`)
	c, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.DeleteUserCommandMessages == nil || !*c.UI.DeleteUserCommandMessages {
		t.Errorf("DeleteUserCommandMessages should default true")
	}
	if c.UI.SmartReplyWithKeyboard == nil || !*c.UI.SmartReplyWithKeyboard {
		t.Errorf("SmartReplyWithKeyboard should default true")
	}
	if c.UI.DiagMaxChars != 3500 {
		t.Errorf("DiagMaxChars default = %d, want 3500", c.UI.DiagMaxChars)
	}
	if c.UI.CompatInlineKeyboard == nil || !*c.UI.CompatInlineKeyboard {
		t.Errorf("CompatInlineKeyboard should default true")
	}
}

// TestConfigUIRespectsExplicitFalse is a regression test for I-1 (T11 follow-up):
// the previous bool-default pattern silently overwrote `false` back to `true`,
// breaking the documented escape hatch when an operator wants to disable
// message deletion (no `can_delete_messages` admin right) or the keyboard
// re-attachment. With *bool we must honour explicit false.
func TestConfigUIRespectsExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "abc")
	cfgPath := writeFile(t, dir, "c.yaml", `
listen: ":8080"
db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -100
  admin_user_id: 1
ui:
  delete_user_command_messages: false
  smart_reply_with_keyboard: false
  compat_inline_keyboard: false
`)
	c, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.DeleteUserCommandMessages == nil || *c.UI.DeleteUserCommandMessages {
		t.Errorf("DeleteUserCommandMessages: explicit false should be honoured, got %v",
			c.UI.DeleteUserCommandMessages)
	}
	if c.UI.SmartReplyWithKeyboard == nil || *c.UI.SmartReplyWithKeyboard {
		t.Errorf("SmartReplyWithKeyboard: explicit false should be honoured, got %v",
			c.UI.SmartReplyWithKeyboard)
	}
	if c.UI.CompatInlineKeyboard == nil || *c.UI.CompatInlineKeyboard {
		t.Errorf("CompatInlineKeyboard: explicit false should be honoured, got %v",
			c.UI.CompatInlineKeyboard)
	}
}

func TestLoadConfig_UpstreamDefaults(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "test-token")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -100
  admin_user_id: 1
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Upstream.CacheTTL != 12*time.Hour {
		t.Errorf("CacheTTL default = %v, want 12h", cfg.Upstream.CacheTTL)
	}
	if cfg.Upstream.AwgmgrRepo != "" {
		t.Errorf("AwgmgrRepo default = %q, want empty", cfg.Upstream.AwgmgrRepo)
	}
	if cfg.Upstream.HrneoRepo != "" {
		t.Errorf("HrneoRepo default = %q, want empty", cfg.Upstream.HrneoRepo)
	}
}

func TestLoadConfig_RetentionDefaults(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "test-token")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -100
  admin_user_id: 1
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.EventsDays != 30 {
		t.Errorf("EventsDays default = %d, want 30", cfg.Retention.EventsDays)
	}
	if cfg.Retention.VacuumInterval != 168*time.Hour {
		t.Errorf("VacuumInterval default = %v, want 168h", cfg.Retention.VacuumInterval)
	}
	if cfg.Retention.WALCheckpointEvery != 1*time.Hour {
		t.Errorf("WALCheckpointEvery default = %v, want 1h", cfg.Retention.WALCheckpointEvery)
	}
}

func TestLoadConfig_RetentionParsed(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "test-token")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -100
  admin_user_id: 1
retention:
  events_days: 90
  vacuum_interval: 72h
  wal_checkpoint_every: 30m
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.EventsDays != 90 {
		t.Errorf("EventsDays = %d, want 90", cfg.Retention.EventsDays)
	}
	if cfg.Retention.VacuumInterval != 72*time.Hour {
		t.Errorf("VacuumInterval = %v, want 72h", cfg.Retention.VacuumInterval)
	}
	if cfg.Retention.WALCheckpointEvery != 30*time.Minute {
		t.Errorf("WALCheckpointEvery = %v, want 30m", cfg.Retention.WALCheckpointEvery)
	}
}

func TestLoadConfig_UpstreamParsed(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "test-token")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -100
  admin_user_id: 1
upstream:
  awgmgr_repo: owner/awg-manager
  hrneo_repo: owner/hr-neo
  cache_ttl: 6h
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Upstream.AwgmgrRepo != "owner/awg-manager" {
		t.Errorf("AwgmgrRepo = %q, want owner/awg-manager", cfg.Upstream.AwgmgrRepo)
	}
	if cfg.Upstream.HrneoRepo != "owner/hr-neo" {
		t.Errorf("HrneoRepo = %q, want owner/hr-neo", cfg.Upstream.HrneoRepo)
	}
	if cfg.Upstream.CacheTTL != 6*time.Hour {
		t.Errorf("CacheTTL = %v, want 6h", cfg.Upstream.CacheTTL)
	}
}

func TestLoadConfig_MobileLifecycleDefaults(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "TOKEN")
	cfgPath := writeFile(t, dir, "cfg.yaml", `db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: 1
  admin_user_id: 2
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Heartbeat.MobileLifecycle == nil || *cfg.Heartbeat.MobileLifecycle != true {
		t.Errorf("MobileLifecycle: want true, got %v", cfg.Heartbeat.MobileLifecycle)
	}
	if cfg.Heartbeat.MobileSleepAfterSec != 1800 {
		t.Errorf("MobileSleepAfterSec: want 1800, got %d", cfg.Heartbeat.MobileSleepAfterSec)
	}
	if cfg.State.MobileFailThreshold != 6 {
		t.Errorf("MobileFailThreshold: want 6, got %d", cfg.State.MobileFailThreshold)
	}
	if cfg.State.MobileRealertEverySec != 21600 {
		t.Errorf("MobileRealertEverySec: want 21600, got %d", cfg.State.MobileRealertEverySec)
	}
}

func TestLoadConfig_MobileLifecycleExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "TOKEN")
	cfgPath := writeFile(t, dir, "cfg.yaml", `db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: 1
  admin_user_id: 2
heartbeat:
  mobile_lifecycle: false
  mobile_sleep_after_sec: 600
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Heartbeat.MobileLifecycle == nil || *cfg.Heartbeat.MobileLifecycle != false {
		t.Errorf("explicit false: want false, got %v", cfg.Heartbeat.MobileLifecycle)
	}
	if cfg.Heartbeat.MobileSleepAfterSec != 600 {
		t.Errorf("MobileSleepAfterSec: want 600, got %d", cfg.Heartbeat.MobileSleepAfterSec)
	}
}

func TestLoadConfig_MobileLifecycleClampsAggressiveSleep(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "TOKEN")
	cfgPath := writeFile(t, dir, "cfg.yaml", `db_path: /tmp/x.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: 1
  admin_user_id: 2
heartbeat:
  mobile_lifecycle: true
  mobile_sleep_after_sec: 300
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Heartbeat.MobileSleepAfterSec != 1800 {
		t.Errorf("MobileSleepAfterSec: want clamp to 1800, got %d", cfg.Heartbeat.MobileSleepAfterSec)
	}
}
