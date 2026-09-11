package agent

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnswatch"
)

var nicknameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

type Config struct {
	Backend       BackendConfig       `yaml:"backend"`
	Agent         AgentConfig         `yaml:"agent"`
	AwgManager    AwgManagerConfig    `yaml:"awg_manager"`
	Checks        ChecksConfig        `yaml:"checks"`
	State         StateConfig         `yaml:"state"`
	ExternalReach ExternalReachConfig `yaml:"external_reach"`
	Maintenance   MaintenanceConfig   `yaml:"maintenance"`
	Logging       LoggingConfig       `yaml:"logging"`
	DNSWatchdog   DNSWatchdogConfig   `yaml:"dns_watchdog"`
}

// DNSWatchdogConfig: the agent-side DNS watchdog (internal/agent/dnswatch).
// When the router's own DoH resolver (Endpoint) stops answering, the watchdog
// switches dns-proxy — in the live config only, never saved — to a split
// fallback set (RU zones → Yandex, the rest → up to MaxForeign live foreign
// resolvers, PinnedZones → PinnedCandidate) and switches back when the own
// resolver recovers. Opt-in per router: an absent block means off.
//
// Defaults are applied by LoadConfig only to an enabled block. An enabled
// block that is unusable (no https:// endpoint with a host, bad bootstrap_ip)
// is switched off with ConfigError set, instead of failing the whole load:
// update_agent_config writes YAML without LoadConfig, and an agent that exits
// on start would cut the router off from the bot.
//
// The remote edit (update_agent_config) writes only enabled, endpoint,
// canary_domain and bootstrap_ip; the lists live in the file.
type DNSWatchdogConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Endpoint      string `yaml:"endpoint"`       // https://host/<secret>, required when enabled
	CanaryDomain  string `yaml:"canary_domain"`  // default "example.com" (foreign + own-resolver probes)
	RUCanary      string `yaml:"ru_canary"`      // default "ya.ru" (RU candidate probes)
	BootstrapIP   string `yaml:"bootstrap_ip"`   // endpoint host address; empty → plain DNS to 77.88.8.8:53
	IntervalSec   int    `yaml:"interval_sec"`   // default 60
	FailThreshold int    `yaml:"fail_threshold"` // default 2
	OKThreshold   int    `yaml:"ok_threshold"`   // default 2
	CooldownSec   int    `yaml:"cooldown_sec"`   // default 300
	MaxForeign    int    `yaml:"max_foreign"`    // default 3

	RUZones           []string `yaml:"ru_zones"`           // default dnswatch.DefaultRUZones
	RUCandidates      []string `yaml:"ru_candidates"`      // default dnswatch.DefaultRUCandidates, in order
	ForeignCandidates []string `yaml:"foreign_candidates"` // default dnswatch.DefaultForeignCandidates, in order
	PinnedZones       []string `yaml:"pinned_zones"`       // default dnswatch.DefaultPinnedZones
	PinnedCandidate   string   `yaml:"pinned_candidate"`   // default dnswatch.DefaultPinnedCandidate

	// ConfigError is set by LoadConfig when an enabled block was switched off
	// as unusable. It never contains the endpoint path (a credential).
	ConfigError string `yaml:"-"`
}

// applyDNSWatchdogDefaults validates an enabled block and fills its defaults.
// A disabled block is left untouched.
func applyDNSWatchdogDefaults(w *DNSWatchdogConfig) {
	if !w.Enabled {
		return
	}
	if msg := dnsWatchdogConfigProblem(*w); msg != "" {
		w.Enabled = false
		w.ConfigError = msg
		return
	}
	if w.CanaryDomain == "" {
		w.CanaryDomain = "example.com"
	}
	if w.RUCanary == "" {
		w.RUCanary = "ya.ru"
	}
	if w.IntervalSec <= 0 {
		w.IntervalSec = 60
	}
	if w.FailThreshold <= 0 {
		w.FailThreshold = 2
	}
	if w.OKThreshold <= 0 {
		w.OKThreshold = 2
	}
	if w.CooldownSec <= 0 {
		w.CooldownSec = 300
	}
	if w.MaxForeign <= 0 {
		w.MaxForeign = 3
	}
	if len(w.RUZones) == 0 {
		w.RUZones = append([]string(nil), dnswatch.DefaultRUZones...)
	}
	if len(w.RUCandidates) == 0 {
		w.RUCandidates = append([]string(nil), dnswatch.DefaultRUCandidates...)
	}
	if len(w.ForeignCandidates) == 0 {
		w.ForeignCandidates = append([]string(nil), dnswatch.DefaultForeignCandidates...)
	}
	if len(w.PinnedZones) == 0 {
		w.PinnedZones = append([]string(nil), dnswatch.DefaultPinnedZones...)
	}
	if w.PinnedCandidate == "" {
		w.PinnedCandidate = dnswatch.DefaultPinnedCandidate
	}
}

// dnsWatchdogConfigProblem explains why an enabled block is unusable, or
// returns "". The endpoint path is a credential: only the host may appear.
func dnsWatchdogConfigProblem(w DNSWatchdogConfig) string {
	ep := strings.TrimSpace(w.Endpoint)
	if ep == "" {
		return "dns_watchdog.endpoint is empty"
	}
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "dns_watchdog.endpoint must be an https:// URL with a host"
	}
	if w.BootstrapIP != "" && net.ParseIP(w.BootstrapIP) == nil {
		return fmt.Sprintf("dns_watchdog.bootstrap_ip %q is not an IP address (endpoint host %s)", w.BootstrapIP, u.Hostname())
	}
	// A single-label canary (localhost, intranet) never resolves on a public
	// resolver: the watchdog would take the own resolver for dead forever.
	if w.CanaryDomain != "" && !canaryDomainOK(w.CanaryDomain) {
		return fmt.Sprintf("dns_watchdog.canary_domain %q must be a plain domain name with a dot, e.g. example.com (endpoint host %s)", w.CanaryDomain, u.Hostname())
	}
	return ""
}

// canaryDomainOK: a plain ASCII domain name of at least two labels — the same
// shape update_agent_config accepts for dns_watchdog_canary_domain.
func canaryDomainOK(s string) bool {
	if len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// LoggingConfig controls the agent's log destination. On Entware the S99 init
// sends stderr to /dev/null, so the agent additionally tees slog to a rotating
// file. File defaults to defaultAgentLogFile when unset; set it to "off"/"none"
// to disable the file sink (stderr only). MaxBytes 0 => defaultLogMaxBytes.
type LoggingConfig struct {
	File     string `yaml:"file"`
	MaxBytes int64  `yaml:"max_bytes"`
}

func (c LoggingConfig) ResolveFile() string {
	f := strings.TrimSpace(c.File)
	switch f {
	case "":
		return defaultAgentLogFile
	case "off", "none", "disabled":
		return ""
	default:
		return f
	}
}

// ExternalReachConfig: probes blocked-in-RU services through the
// defaultRoute=true WG tunnel. When Enabled and Targets is empty, defaults
// to YouTube/Telegram/Instagram. BindToDefault binds the HTTP client to
// the linux iface backing the default-route tunnel; without it, probes go
// through the system default route (often outside any WG tunnel).
type ExternalReachConfig struct {
	Enabled       bool                  `yaml:"enabled"`
	FailThreshold int                   `yaml:"fail_threshold"`
	Targets       []ExternalReachTarget `yaml:"targets"`
	BindToDefault bool                  `yaml:"bind_to_default"`
}

type ExternalReachTarget struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// MaintenanceConfig gates destructive ops the bot can trigger remotely.
// Both default to false — wizard's `add-router` step opts in by setting them
// to true, so the operator must explicitly accept the destructive surface
// (or hand-edit the file).
type MaintenanceConfig struct {
	AllowRouterReboot    bool `yaml:"allow_router_reboot"`
	AllowFirmwareInstall bool `yaml:"allow_firmware_install"`
}

var defaultExternalReachTargets = []ExternalReachTarget{
	{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	{Name: "Telegram", URL: "https://web.telegram.org/"},
	{Name: "Instagram", URL: "https://www.instagram.com/favicon.ico"},
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
	BaseURL      string `yaml:"base_url"`
	URLAlias     string `yaml:"url"`
	Login        string `yaml:"login"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
}

func (a AwgManagerConfig) URL() string {
	if a.BaseURL != "" {
		return a.BaseURL
	}
	if a.URLAlias != "" {
		return a.URLAlias
	}
	return "http://127.0.0.1:2222"
}

func (a AwgManagerConfig) PasswordValue() (string, error) {
	if a.Password != "" {
		return a.Password, nil
	}
	if a.PasswordFile == "" {
		return "", nil
	}
	body, err := os.ReadFile(a.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("read awg_manager.password_file: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
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

func (s StateConfig) CommandResultPath() string {
	return filepath.Join(filepath.Dir(s.ResolvedPath()), "cmd-results.json")
}

// DNSWatchdogStatePath: where the DNS watchdog keeps its cooldown stamp and
// the own-resolver lines it removed (next to the reporter state).
func (s StateConfig) DNSWatchdogStatePath() string {
	return filepath.Join(filepath.Dir(s.ResolvedPath()), "dns-watchdog-state.json")
}

type ChecksConfig struct {
	AWG AWGCheckConfig `yaml:"awg"`
	DNS DNSCheckConfig `yaml:"dns"`
}

// AWGCheckConfig: legacy `interface`, `expected_exit_ip`, `marker_url`,
// `routing_probe_url` fields were dropped in the rc20 audit cleanup —
// gopkg.in/yaml.v3's Unmarshal already silently ignores unknown keys, so
// legacy config.yaml files keep parsing without those fields in the struct.
// Only HandshakeMaxAgeSec still has effect (post awg-manager pivot, 2026-04-29).
type AWGCheckConfig struct {
	HandshakeMaxAgeSec int `yaml:"handshake_max_age_sec,omitempty"`
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
	if cfg.ExternalReach.Enabled {
		if len(cfg.ExternalReach.Targets) == 0 {
			cfg.ExternalReach.Targets = append([]ExternalReachTarget(nil), defaultExternalReachTargets...)
		}
		if cfg.ExternalReach.FailThreshold <= 0 {
			n := len(cfg.ExternalReach.Targets)
			cfg.ExternalReach.FailThreshold = (n*2 + 2) / 3
			if cfg.ExternalReach.FailThreshold < 1 {
				cfg.ExternalReach.FailThreshold = 1
			}
		}
	}
	applyDNSWatchdogDefaults(&cfg.DNSWatchdog)
	return &cfg, nil
}
