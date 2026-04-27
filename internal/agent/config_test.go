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
  url: https://wgmonitor.example.com
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe

agent:
  nickname: testkeen
  interval_sec: 60
checks:
  awg:
    interface: awg0
    expected_exit_ip: 198.51.100.21
    marker_url: https://www.youtube.com/-/manifest
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Backend.URL != "https://wgmonitor.example.com" {
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
  url: https://wgmonitor.example.com
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
  url: https://wgmonitor.example.com
  token: 0123456789abcdef0123456789abcdef0123456789abcdef
agent:
  nickname: testkeen
  interval_sec: 30
checks:
  awg:
    interface: awg0
    handshake_max_age_sec: 180
    expected_exit_ip: 198.51.100.21
    marker_url: https://www.youtube.com/-/manifest
  dns:
    auto_discover: true
    test_domain: example.com
    fail_threshold: 2
    endpoints:
      - { type: plain, host: 1.1.1.1, port: 53 }
      - { type: plain, host: 8.8.8.8, port: 53 }
      - { type: plain, host: 9.9.9.9, port: 53 }
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Checks.AWG.Interface != "awg0" || cfg.Checks.AWG.ExpectedExitIP != "198.51.100.21" {
		t.Fatalf("awg parse: %+v", cfg.Checks.AWG)
	}
	if len(cfg.Checks.DNS.Endpoints) != 3 || cfg.Checks.DNS.FailThreshold != 2 {
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

func TestLoadConfigDNSDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
backend:
  url: https://wgmonitor.example.com
  token: 0123456789abcdef0123456789abcdef0123456789abcdef
agent:
  nickname: testkeen
checks:
  awg:
    interface: awg0
    expected_exit_ip: 198.51.100.21
    marker_url: https://www.youtube.com/-/manifest
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Checks.DNS.TestDomain != "example.com" {
		t.Fatalf("TestDomain default not applied: got %q", cfg.Checks.DNS.TestDomain)
	}
	if cfg.Checks.DNS.FailThreshold != 1 {
		t.Fatalf("FailThreshold default not applied: got %d, want 1", cfg.Checks.DNS.FailThreshold)
	}
	// Endpoints can be empty; AutoDiscover has no default
	if len(cfg.Checks.DNS.Endpoints) != 0 {
		t.Fatalf("Endpoints should be empty when not specified: got %d", len(cfg.Checks.DNS.Endpoints))
	}
	if cfg.Checks.DNS.AutoDiscover {
		t.Fatalf("AutoDiscover should default to false")
	}
}

func TestLoadConfig_MarkerURLDefaultsWhenOmitted(t *testing.T) {
	body := `backend:
  url: https://wgmonitor.example.org
  token: 0123456789abcdef0123456789abcdef0123456789abcdef
agent:
  nickname: testkeen
  interval_sec: 60
checks:
  awg:
    interface: nwg0
    expected_exit_ip: 198.51.100.21
`
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("expected load to succeed without marker_url: %v", err)
	}
	const want = "http://www.gstatic.com/generate_204"
	if got := cfg.Checks.AWG.ResolvedMarkerURL(); got != want {
		t.Fatalf("ResolvedMarkerURL() default: got %q want %q", got, want)
	}
}

const minimalNewSchema = `
backend:
  url: https://wgmon.example.org
  token: abcdefghijklmnopqrstuvwxyz0123456789ABCD
agent:
  nickname: testkeen
  interval_sec: 60
checks:
  awg:
    interface: nwg0
    expected_exit_ip: 198.51.100.21
  dns:
    auto_discover: true
    test_domain: example.com
    fail_threshold: 1
    endpoints:
      - { type: doh, url: "https://my.example/dns-query" }
      - { type: plain, host: 1.1.1.1, port: 53, ndms_name: Wireguard1 }
`

func TestLoadConfig_NewDNSSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(minimalNewSchema), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Checks.DNS.AutoDiscover {
		t.Errorf("AutoDiscover not parsed")
	}
	if len(cfg.Checks.DNS.Endpoints) != 2 {
		t.Fatalf("Endpoints: %+v", cfg.Checks.DNS.Endpoints)
	}
	doh := cfg.Checks.DNS.Endpoints[0]
	if doh.Type != "doh" || doh.URL != "https://my.example/dns-query" {
		t.Errorf("doh: %+v", doh)
	}
	plain := cfg.Checks.DNS.Endpoints[1]
	if plain.Type != "plain" || plain.Host != "1.1.1.1" || plain.Port != 53 || plain.NDMSName != "Wireguard1" {
		t.Errorf("plain: %+v", plain)
	}
}
