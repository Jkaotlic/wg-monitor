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
	Backend    BackendConfig    `yaml:"backend"`
	Agent      AgentConfig      `yaml:"agent"`
	AwgManager AwgManagerConfig `yaml:"awg_manager"`
	Checks     ChecksConfig     `yaml:"checks"`
	State      StateConfig      `yaml:"state"`
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

// AwgManagerConfig points the agent at the local awg-manager daemon.
// Default base URL is http://127.0.0.1:2222 — that's where hoaxisr/awg-manager
// listens by default on Keenetic. Users with a non-default port should
// override base_url; otherwise the field can be omitted entirely.
type AwgManagerConfig struct {
	BaseURL string `yaml:"base_url"`
}

func (a AwgManagerConfig) URL() string {
	if a.BaseURL != "" {
		return a.BaseURL
	}
	return "http://127.0.0.1:2222"
}

// StateConfig: where the reporter persists last_report_at across restarts.
// Empty path → in-memory only (Resumed flag won't survive process restart).
type StateConfig struct {
	Path string `yaml:"path"`
}

func (s StateConfig) ResolvedPath() string {
	if s.Path != "" {
		return s.Path
	}
	return "/opt/var/wg-monitor/reporter-state.json"
}

type ChecksConfig struct {
	AWG AWGCheckConfig `yaml:"awg"`
	DNS DNSCheckConfig `yaml:"dns"`
}

// AWGCheckConfig: most former fields (interface, expected_exit_ip, marker_url,
// routing_probe_url) are no longer used — those checks were replaced with
// awg-manager API wrappers in the 2026-04-29 pivot. The struct is kept so
// existing config.yaml files don't fail to parse; legacy fields are silently
// ignored. Only HandshakeMaxAgeSec still has effect.
type AWGCheckConfig struct {
	Interface          string `yaml:"interface,omitempty"`           // deprecated (legacy parse only)
	ExpectedExitIP     string `yaml:"expected_exit_ip,omitempty"`    // deprecated
	MarkerURL          string `yaml:"marker_url,omitempty"`          // deprecated
	RoutingProbeURL    string `yaml:"routing_probe_url,omitempty"`   // deprecated
	HandshakeMaxAgeSec int    `yaml:"handshake_max_age_sec,omitempty"`
}

func (a AWGCheckConfig) HandshakeMaxAge() time.Duration {
	if a.HandshakeMaxAgeSec <= 0 {
		return 180 * time.Second
	}
	return time.Duration(a.HandshakeMaxAgeSec) * time.Second
}

// DNSEndpointConfig: explicit DNS endpoint to probe in addition to whatever
// auto-discovery finds via NDMC.
type DNSEndpointConfig struct {
	Type     string `yaml:"type"`
	Host     string `yaml:"host,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	URL      string `yaml:"url,omitempty"`
	NDMSName string `yaml:"ndms_name,omitempty"`
}

type DNSCheckConfig struct {
	AutoDiscover   bool                `yaml:"auto_discover"`
	Endpoints      []DNSEndpointConfig `yaml:"endpoints"`
	TestDomain     string              `yaml:"test_domain"`
	FailThreshold  int                 `yaml:"fail_threshold"`
	RKNTestDomains []string            `yaml:"rkn_test_domains"`
}

type LoadOption func(*loadOpts)

type loadOpts struct {
	allowHTTP bool
}

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
	if cfg.Checks.DNS.TestDomain == "" {
		cfg.Checks.DNS.TestDomain = "example.com"
	}
	if cfg.Checks.DNS.FailThreshold <= 0 {
		cfg.Checks.DNS.FailThreshold = 1
	}
	// If auto-discovery is on and the user didn't override RKN-test domains,
	// supply the defaults so production agents get RKN-awareness without
	// extra config plumbing.
	if cfg.Checks.DNS.AutoDiscover && len(cfg.Checks.DNS.RKNTestDomains) == 0 {
		cfg.Checks.DNS.RKNTestDomains = []string{"rutracker.org", "lostfilm.tv", "linkedin.com"}
	}
	return &cfg, nil
}
