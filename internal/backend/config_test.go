package backend

import (
	"os"
	"path/filepath"
	"testing"
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
	if cfg.Heartbeat.StaleAfterSec != 300 {
		t.Fatalf("hb default: %d", cfg.Heartbeat.StaleAfterSec)
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
	if cfg.State.MuteCutoffHour != 9 {
		t.Errorf("MuteCutoffHour default: got %d, want 9", cfg.State.MuteCutoffHour)
	}
	if cfg.State.RealertTickSec != 300 {
		t.Errorf("RealertTickSec default: got %d, want 300", cfg.State.RealertTickSec)
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
	if !c.UI.DeleteUserCommandMessages {
		t.Errorf("DeleteUserCommandMessages should default true")
	}
	if !c.UI.SmartReplyWithKeyboard {
		t.Errorf("SmartReplyWithKeyboard should default true")
	}
	if c.UI.DiagMaxChars != 3500 {
		t.Errorf("DiagMaxChars default = %d, want 3500", c.UI.DiagMaxChars)
	}
}
