package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: https://wgmonitor.jkaotlic.duckdns.org
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe

agent:
  nickname: testkeen
  interval_sec: 60
checks:
  awg:
    interface: awg0
    expected_exit_ip: 89.125.101.122
    marker_url: https://www.youtube.com/-/manifest
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Backend.URL != "https://wgmonitor.jkaotlic.duckdns.org" {
		t.Errorf("url: %q", cfg.Backend.URL)
	}
	if cfg.Agent.Nickname != "testkeen" {
		t.Errorf("nickname: %q", cfg.Agent.Nickname)
	}
	if cfg.Agent.Interval() != 60*time.Second {
		t.Errorf("interval: %v", cfg.Agent.Interval())
	}
	if cfg.Checks.AWG.Interface != "awg0" {
		t.Errorf("awg_iface: %q", cfg.Checks.AWG.Interface)
	}
}

func TestLoadConfig_DefaultsInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: https://wgmonitor.jkaotlic.duckdns.org
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
agent:
  nickname: testkeen
checks:
  awg:
    interface: awg0
    expected_exit_ip: 1.2.3.4
    marker_url: https://www.youtube.com/-/manifest
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Interval() != 60*time.Second {
		t.Errorf("default interval: got %v want 60s", cfg.Agent.Interval())
	}
}

func TestLoadConfig_RejectsBadNickname(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: https://x
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
agent:
  nickname: "Bad Name"
checks:
  awg:
    interface: awg0
    expected_exit_ip: 1.2.3.4
    marker_url: https://www.youtube.com/-/manifest
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on bad nickname")
	}
}

func TestLoadConfig_RejectsHTTPURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: http://insecure.example
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
agent:
  nickname: testkeen
checks:
  awg:
    interface: awg0
    expected_exit_ip: 1.2.3.4
    marker_url: https://www.youtube.com/-/manifest
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on plaintext HTTP URL")
	}
}

func TestLoadConfig_AllowHTTPOption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: http://127.0.0.1:18080
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
agent:
  nickname: testkeen
checks:
  awg:
    interface: awg0
    expected_exit_ip: 1.2.3.4
    marker_url: https://www.youtube.com/-/manifest
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path, WithAllowHTTP()); err != nil {
		t.Fatalf("WithAllowHTTP should permit http: %v", err)
	}
}

func TestLoadConfigWithChecksSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
backend:
  url: https://wgmonitor.jkaotlic.duckdns.org
  token: 0123456789abcdef0123456789abcdef0123456789abcdef
agent:
  nickname: testkeen
  interval_sec: 30
checks:
  awg:
    interface: awg0
    handshake_max_age_sec: 180
    expected_exit_ip: 89.125.101.122
    marker_url: https://www.youtube.com/-/manifest
  dns:
    test_domain: example.com
    fail_threshold: 2
    providers:
      - { name: cloudflare, host: 1.1.1.1 }
      - { name: google,     host: 8.8.8.8 }
      - { name: quad9,      host: 9.9.9.9 }
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Checks.AWG.Interface != "awg0" || cfg.Checks.AWG.ExpectedExitIP != "89.125.101.122" {
		t.Fatalf("awg parse: %+v", cfg.Checks.AWG)
	}
	if len(cfg.Checks.DNS.Providers) != 3 || cfg.Checks.DNS.FailThreshold != 2 {
		t.Fatalf("dns parse: %+v", cfg.Checks.DNS)
	}
	if cfg.Checks.AWG.HandshakeMaxAge() != 180*time.Second {
		t.Fatalf("max age: %v", cfg.Checks.AWG.HandshakeMaxAge())
	}
}

func TestLoadConfigRejectsMissingChecksAWG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
backend: { url: https://x.example, token: 0123456789abcdef0123456789abcdef0123456789abcdef }
agent: { nickname: testkeen }
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatalf("expected error on missing checks.awg")
	}
}
