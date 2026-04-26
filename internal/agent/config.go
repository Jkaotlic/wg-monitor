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
}

type BackendConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

type AgentConfig struct {
	Nickname       string `yaml:"nickname"`
	IntervalSec    int    `yaml:"interval_sec"`
	AwgIface       string `yaml:"awg_iface"`
	ExpectedExitIP string `yaml:"expected_exit_ip"`
}

func (a AgentConfig) Interval() time.Duration {
	if a.IntervalSec <= 0 {
		return 60 * time.Second
	}
	return time.Duration(a.IntervalSec) * time.Second
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
	if cfg.Agent.AwgIface == "" {
		return nil, fmt.Errorf("agent.awg_iface is required (no default — per-user, see spec Q4)")
	}
	if cfg.Agent.ExpectedExitIP == "" {
		return nil, fmt.Errorf("agent.expected_exit_ip is required (no default — per-user, see spec Q4)")
	}
	return &cfg, nil
}
