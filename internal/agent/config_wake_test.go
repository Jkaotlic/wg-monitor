package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_WakeHooksOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "backend:\n  url: https://wgmonitor.example.com\n  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe\nagent:\n  nickname: router-a\n  wake_hooks_off: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agent.WakeHooksOff {
		t.Fatal("wake_hooks_off не прочитан")
	}
}
