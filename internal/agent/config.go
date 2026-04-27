package agent

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var nicknameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

type Config struct {
	Backend BackendConfig `yaml:"backend"`
	Agent   AgentConfig   `yaml:"agent"`
	Checks  ChecksConfig  `yaml:"checks"`
}

type BackendConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

type AgentConfig struct {
	Nickname    string `yaml:"nickname"`
	IntervalSec int    `yaml:"interval_sec"`
}

func (a AgentConfig) Interval() time.Duration {
	if a.IntervalSec <= 0 {
		return 60 * time.Second
	}
	return time.Duration(a.IntervalSec) * time.Second
}

type ChecksConfig struct {
	AWG AWGCheckConfig `yaml:"awg"`
	DNS DNSCheckConfig `yaml:"dns"`
}

type AWGCheckConfig struct {
	Interface          string `yaml:"interface"`
	HandshakeMaxAgeSec int    `yaml:"handshake_max_age_sec"`
	ExpectedExitIP     string `yaml:"expected_exit_ip"`
	MarkerURL          string `yaml:"marker_url"`
	RoutingProbeURL    string `yaml:"routing_probe_url"` // default https://api.ipify.org
}

func (a AWGCheckConfig) HandshakeMaxAge() time.Duration {
	if a.HandshakeMaxAgeSec <= 0 {
		return 180 * time.Second
	}
	return time.Duration(a.HandshakeMaxAgeSec) * time.Second
}

func (a AWGCheckConfig) RoutingURL() string {
	if a.RoutingProbeURL != "" {
		return a.RoutingProbeURL
	}
	return "https://1.1.1.1/cdn-cgi/trace"
}

type DNSProviderConfig struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
}

type DNSCheckConfig struct {
	Providers     []DNSProviderConfig `yaml:"providers"`
	TestDomain    string              `yaml:"test_domain"`
	FailThreshold int                 `yaml:"fail_threshold"`
}

type LoadOption func(*loadOpts)

type loadOpts struct {
	allowHTTP bool
}

// WithAllowHTTP permits http:// backend URLs (dev/smoke only — production must use https://).
func WithAllowHTTP() LoadOption {
	return func(o *loadOpts) { o.allowHTTP = true }
}

func LoadConfig(path string, opts ...LoadOption) (*Config, error) {
	o := loadOpts{}
	for _, op := range opts {
		op(&o)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if o.allowHTTP {
		if !strings.HasPrefix(cfg.Backend.URL, "http://") && !strings.HasPrefix(cfg.Backend.URL, "https://") {
			return nil, fmt.Errorf("backend.url must start with http:// or https://, got %q", cfg.Backend.URL)
		}
	} else {
		if !strings.HasPrefix(cfg.Backend.URL, "https://") {
			return nil, fmt.Errorf("backend.url must start with https://, got %q", cfg.Backend.URL)
		}
	}
	if len(cfg.Backend.Token) < 32 {
		return nil, fmt.Errorf("backend.token must be at least 32 chars")
	}
	if !nicknameRegexp.MatchString(cfg.Agent.Nickname) {
		return nil, fmt.Errorf("agent.nickname %q must match %s", cfg.Agent.Nickname, nicknameRegexp)
	}
	if cfg.Checks.AWG.Interface == "" {
		return nil, fmt.Errorf("checks.awg.interface is required (no default — per-user, see spec Q4)")
	}
	if cfg.Checks.AWG.ExpectedExitIP == "" {
		return nil, fmt.Errorf("checks.awg.expected_exit_ip is required (no default — per-user, see spec Q4)")
	}
	if cfg.Checks.AWG.MarkerURL == "" {
		return nil, fmt.Errorf("checks.awg.marker_url is required")
	}
	if cfg.Checks.DNS.TestDomain == "" {
		cfg.Checks.DNS.TestDomain = "example.com"
	}
	if cfg.Checks.DNS.FailThreshold <= 0 {
		cfg.Checks.DNS.FailThreshold = 2
	}
	if len(cfg.Checks.DNS.Providers) == 0 {
		cfg.Checks.DNS.Providers = []DNSProviderConfig{
			{Name: "cloudflare", Host: "1.1.1.1"},
			{Name: "google", Host: "8.8.8.8"},
			{Name: "quad9", Host: "9.9.9.9"},
		}
	}
	return &cfg, nil
}
