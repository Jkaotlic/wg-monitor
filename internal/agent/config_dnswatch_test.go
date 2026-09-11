package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnswatch"
)

const dnsWatchdogBaseYAML = `
backend:
  url: https://wgmonitor.example.com
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
agent:
  nickname: testkeen
`

func loadDNSWatchdogConfig(t *testing.T, extra string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(dnsWatchdogBaseYAML+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

func TestLoadConfig_DNSWatchdogOffByDefault(t *testing.T) {
	cfg := loadDNSWatchdogConfig(t, "")
	w := cfg.DNSWatchdog
	if w.Enabled {
		t.Fatal("no dns_watchdog block must mean the watchdog is off")
	}
	if w.ConfigError != "" {
		t.Errorf("absent block is not a config error, got %q", w.ConfigError)
	}
	// Defaults are applied only to an enabled block: an off block stays empty,
	// so nothing about the watchdog leaks into a router that never opted in.
	if w.IntervalSec != 0 || len(w.ForeignCandidates) != 0 {
		t.Errorf("disabled block must keep zero values, got %+v", w)
	}
}

// TestLoadConfig_DNSWatchdogDefaults loads exactly the minimal block the
// dashboard's update_agent_config writes (dns_watchdog_enabled +
// dns_watchdog_endpoint → dns_watchdog.enabled / .endpoint): it must stay
// enabled, with every other field filled from defaults.
func TestLoadConfig_DNSWatchdogDefaults(t *testing.T) {
	cfg := loadDNSWatchdogConfig(t, `
dns_watchdog:
  enabled: true
  endpoint: https://dns.example.com/secret-path
`)
	w := cfg.DNSWatchdog
	if !w.Enabled || w.ConfigError != "" {
		t.Fatalf("valid block must stay enabled, got enabled=%v err=%q", w.Enabled, w.ConfigError)
	}
	if w.Endpoint != "https://dns.example.com/secret-path" {
		t.Errorf("endpoint = %q", w.Endpoint)
	}
	if w.IntervalSec != 60 || w.FailThreshold != 2 || w.OKThreshold != 2 || w.CooldownSec != 300 || w.MaxForeign != 3 {
		t.Errorf("numeric defaults = interval %d fail %d ok %d cooldown %d max_foreign %d, want 60/2/2/300/3",
			w.IntervalSec, w.FailThreshold, w.OKThreshold, w.CooldownSec, w.MaxForeign)
	}
	if w.CanaryDomain != "example.com" || w.RUCanary != "ya.ru" {
		t.Errorf("canaries = %q / %q, want example.com / ya.ru", w.CanaryDomain, w.RUCanary)
	}
	if w.BootstrapIP != "" {
		t.Errorf("bootstrap_ip must default to empty (ask plain DNS), got %q", w.BootstrapIP)
	}
	if !reflect.DeepEqual(w.RUZones, dnswatch.DefaultRUZones) ||
		!reflect.DeepEqual(w.RUCandidates, dnswatch.DefaultRUCandidates) ||
		!reflect.DeepEqual(w.ForeignCandidates, dnswatch.DefaultForeignCandidates) ||
		!reflect.DeepEqual(w.PinnedZones, dnswatch.DefaultPinnedZones) ||
		w.PinnedCandidate != dnswatch.DefaultPinnedCandidate {
		t.Errorf("list defaults differ from dnswatch defaults: %+v", w)
	}
	// The config must hold copies: editing it must not rewrite the defaults.
	w.RUZones[0] = "changed"
	if dnswatch.DefaultRUZones[0] != "ru" {
		t.Fatal("config shares the default slice; a later edit rewrote dnswatch.DefaultRUZones")
	}
}

func TestLoadConfig_DNSWatchdogKeepsExplicitValues(t *testing.T) {
	cfg := loadDNSWatchdogConfig(t, `
dns_watchdog:
  enabled: true
  endpoint: https://dns.example.com/secret-path
  canary_domain: probe.example.com
  bootstrap_ip: 203.0.113.10
  interval_sec: 30
  fail_threshold: 3
  ok_threshold: 4
  cooldown_sec: 600
  max_foreign: 2
  ru_zones: [ru]
  foreign_candidates:
    - "tls upstream 198.51.100.53 sni dns.example.com"
`)
	w := cfg.DNSWatchdog
	if !w.Enabled || w.ConfigError != "" {
		t.Fatalf("enabled=%v err=%q", w.Enabled, w.ConfigError)
	}
	if w.CanaryDomain != "probe.example.com" || w.BootstrapIP != "203.0.113.10" ||
		w.IntervalSec != 30 || w.FailThreshold != 3 || w.OKThreshold != 4 || w.CooldownSec != 600 || w.MaxForeign != 2 {
		t.Errorf("explicit values overwritten: %+v", w)
	}
	if !reflect.DeepEqual(w.RUZones, []string{"ru"}) ||
		!reflect.DeepEqual(w.ForeignCandidates, []string{"tls upstream 198.51.100.53 sni dns.example.com"}) {
		t.Errorf("explicit lists overwritten: zones %v foreign %v", w.RUZones, w.ForeignCandidates)
	}
	// Unset lists still get their defaults.
	if !reflect.DeepEqual(w.RUCandidates, dnswatch.DefaultRUCandidates) {
		t.Errorf("unset ru_candidates = %v, want defaults", w.RUCandidates)
	}
}

// TestLoadConfig_DNSWatchdogRequiresHTTPSEndpoint: an enabled watchdog needs an
// https:// endpoint with a host (and a valid bootstrap_ip, if set). A bad block
// switches the WATCHDOG off with a loud config error — it never fails the whole
// load: update_agent_config writes YAML without LoadConfig, and an agent that
// exits on start would cut the router off from the bot, the one channel left
// to fix the config.
func TestLoadConfig_DNSWatchdogRequiresHTTPSEndpoint(t *testing.T) {
	cases := map[string]string{
		"missing":       "",
		"http":          "endpoint: http://dns.example.com/secret-path",
		"no scheme":     "endpoint: dns.example.com/secret-path",
		"no host":       "endpoint: https:///secret-path",
		"bad bootstrap": "endpoint: https://dns.example.com/secret-path\n  bootstrap_ip: dns.example.com",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := loadDNSWatchdogConfig(t, "\ndns_watchdog:\n  enabled: true\n  "+line+"\n")
			w := cfg.DNSWatchdog
			if w.Enabled {
				t.Fatalf("invalid block must switch the watchdog off, got enabled (%+v)", w)
			}
			if w.ConfigError == "" {
				t.Fatal("invalid block must carry a config error to log")
			}
			if strings.Contains(w.ConfigError, "secret-path") {
				t.Errorf("config error must not echo the secret endpoint path: %q", w.ConfigError)
			}
		})
	}
}

func TestStateConfig_DNSWatchdogStatePath(t *testing.T) {
	if got := (StateConfig{}).DNSWatchdogStatePath(); got != "/opt/var/wg-monitor/dns-watchdog-state.json" {
		t.Errorf("default = %q", got)
	}
	if got := (StateConfig{Path: "/tmp/x/reporter.json"}).DNSWatchdogStatePath(); got != "/tmp/x/dns-watchdog-state.json" {
		t.Errorf("custom = %q", got)
	}
}
