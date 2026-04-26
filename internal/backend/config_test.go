package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.yaml")
	body := `
listen: 127.0.0.1:8080
log_level: info
agents:
  - nickname: testkeen
    token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8080" {
		t.Errorf("listen: %q", cfg.Listen)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Nickname != "testkeen" {
		t.Errorf("agents: %+v", cfg.Agents)
	}
	if cfg.Agents[0].Token != "deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe" {
		t.Errorf("token mismatch")
	}
}

func TestLoadConfig_RejectsBadNickname(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.yaml")
	body := `
listen: 127.0.0.1:8080
agents:
  - nickname: "Bad Name!"
    token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on bad nickname, got nil")
	}
}

func TestLoadConfig_RejectsShortToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.yaml")
	body := `
listen: 127.0.0.1:8080
agents:
  - nickname: testkeen
    token: short
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on short token, got nil")
	}
}

func TestLoadConfig_RejectsDuplicateNickname(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.yaml")
	body := `
listen: 127.0.0.1:8080
agents:
  - nickname: dup
    token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
  - nickname: dup
    token: cafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeef
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on duplicate nickname, got nil")
	}
}
