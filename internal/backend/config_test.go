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
