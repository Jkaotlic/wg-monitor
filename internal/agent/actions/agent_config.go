package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Agent-side config editing. Lets the operator change a SAFE whitelist of
// config.yaml settings on the router and restart the agent to apply them —
// without SSH. backend.url / backend.token are deliberately NOT here: re-pointing
// the backend is fleet-takeover blast radius and stays on the wizard/CLI path
// (update_backend_url). See feedback_dashboard_security_boundary.

// agentConfigField maps a flat command arg to a config.yaml section/key. Only
// keys listed here can be changed via update_agent_config.
type agentConfigField struct {
	Arg     string
	Section string
	Key     string
	Kind    string // "int" | "bool" | "string"
}

var agentConfigWhitelist = []agentConfigField{
	{"interval_sec", "agent", "interval_sec", "int"},
	{"awgm_base_url", "awg_manager", "base_url", "string"},
	{"awgm_login", "awg_manager", "login", "string"},
	{"external_reach_enabled", "external_reach", "enabled", "bool"},
	{"external_reach_fail_threshold", "external_reach", "fail_threshold", "int"},
	{"allow_router_reboot", "maintenance", "allow_router_reboot", "bool"},
	{"allow_firmware_install", "maintenance", "allow_firmware_install", "bool"},
}

// agentConfigFile is the minimal subset of config.yaml we read back. yaml.v3
// silently ignores the keys we don't list, so this parses any real config.
type agentConfigFile struct {
	Agent struct {
		IntervalSec int `yaml:"interval_sec"`
	} `yaml:"agent"`
	AwgManager struct {
		BaseURL string `yaml:"base_url"`
		Login   string `yaml:"login"`
	} `yaml:"awg_manager"`
	ExternalReach struct {
		Enabled       bool `yaml:"enabled"`
		FailThreshold int  `yaml:"fail_threshold"`
	} `yaml:"external_reach"`
	Maintenance struct {
		AllowRouterReboot    bool `yaml:"allow_router_reboot"`
		AllowFirmwareInstall bool `yaml:"allow_firmware_install"`
	} `yaml:"maintenance"`
}

// AgentConfigView is the JSON the agent returns for agent_config_get. ConfigKind
// is a discriminator so the dashboard can recognise and prefill from it.
type AgentConfigView struct {
	ConfigKind                 string `json:"config_kind"`
	IntervalSec                int    `json:"interval_sec"`
	AWGMBaseURL                string `json:"awgm_base_url"`
	AWGMLogin                  string `json:"awgm_login"`
	ExternalReachEnabled       bool   `json:"external_reach_enabled"`
	ExternalReachFailThreshold int    `json:"external_reach_fail_threshold"`
	AllowRouterReboot          bool   `json:"allow_router_reboot"`
	AllowFirmwareInstall       bool   `json:"allow_firmware_install"`
	ConfigPath                 string `json:"config_path"`
}

// GetAgentConfig returns the safe config subset as JSON for the dashboard to
// prefill its editor.
func GetAgentConfig(configPath string) (string, error) {
	if configPath == "" {
		return "", fmt.Errorf("agent_config_get: config path not set on runner")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("agent_config_get: read config: %w", err)
	}
	var f agentConfigFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("agent_config_get: parse config: %w", err)
	}
	view := AgentConfigView{
		ConfigKind:                 "agent",
		IntervalSec:                f.Agent.IntervalSec,
		AWGMBaseURL:                f.AwgManager.BaseURL,
		AWGMLogin:                  f.AwgManager.Login,
		ExternalReachEnabled:       f.ExternalReach.Enabled,
		ExternalReachFailThreshold: f.ExternalReach.FailThreshold,
		AllowRouterReboot:          f.Maintenance.AllowRouterReboot,
		AllowFirmwareInstall:       f.Maintenance.AllowFirmwareInstall,
		ConfigPath:                 configPath,
	}
	b, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("agent_config_get: encode: %w", err)
	}
	return string(b), nil
}

type agentConfigChange struct {
	section, key, value, tag string
}

// UpdateAgentConfig applies the whitelisted changes present in args to
// config.yaml (preserving comments and untouched keys via the yaml.Node API),
// then schedules an agent restart so the new values take effect. Only keys the
// operator actually set are touched — a partial patch, never a full rewrite.
func UpdateAgentConfig(_ context.Context, args map[string]any, configPath string) (string, error) {
	if configPath == "" {
		return "", fmt.Errorf("update_agent_config: config path not set on runner")
	}
	changes, err := normalizeAgentConfigChanges(args)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", fmt.Errorf("update_agent_config: no recognized settings to change")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("update_agent_config: read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("update_agent_config: parse config: %w", err)
	}
	applied := make([]string, 0, len(changes))
	for _, ch := range changes {
		if err := setConfigValue(&doc, ch.section, ch.key, ch.value, ch.tag); err != nil {
			return "", fmt.Errorf("update_agent_config: %w", err)
		}
		applied = append(applied, ch.section+"."+ch.key+"="+ch.value)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return "", fmt.Errorf("update_agent_config: marshal config: %w", err)
	}
	// Re-parse the result so we never write a config the agent can't load.
	var check agentConfigFile
	if err := yaml.Unmarshal(out, &check); err != nil {
		return "", fmt.Errorf("update_agent_config: result would not parse: %w", err)
	}
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return "", fmt.Errorf("update_agent_config: write temp: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("update_agent_config: rename: %w", err)
	}
	scheduleURLUpdateRestart()
	return "config updated (" + strings.Join(applied, ", ") + "); restarting agent", nil
}

func normalizeAgentConfigChanges(args map[string]any) ([]agentConfigChange, error) {
	out := make([]agentConfigChange, 0, len(agentConfigWhitelist))
	for _, f := range agentConfigWhitelist {
		v, ok := args[f.Arg]
		if !ok {
			continue
		}
		switch f.Kind {
		case "int":
			n, err := agentConfigInt(v)
			if err != nil {
				return nil, fmt.Errorf("update_agent_config: %s: %v", f.Arg, err)
			}
			if err := validateAgentConfigInt(f.Arg, n); err != nil {
				return nil, err
			}
			out = append(out, agentConfigChange{f.Section, f.Key, strconv.Itoa(n), "!!int"})
		case "bool":
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("update_agent_config: %s must be true or false", f.Arg)
			}
			out = append(out, agentConfigChange{f.Section, f.Key, strconv.FormatBool(b), "!!bool"})
		case "string":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("update_agent_config: %s must be a string", f.Arg)
			}
			s = strings.TrimSpace(s)
			if err := validateAgentConfigString(f.Arg, s); err != nil {
				return nil, err
			}
			out = append(out, agentConfigChange{f.Section, f.Key, s, "!!str"})
		}
	}
	return out, nil
}

func agentConfigInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		return int(i), err
	case string:
		return strconv.Atoi(strings.TrimSpace(n))
	default:
		return 0, fmt.Errorf("must be an integer")
	}
}

func validateAgentConfigInt(arg string, n int) error {
	switch arg {
	case "interval_sec":
		if n < 10 || n > 86400 {
			return fmt.Errorf("update_agent_config: interval_sec must be 10..86400 seconds")
		}
	case "external_reach_fail_threshold":
		if n < 1 || n > 20 {
			return fmt.Errorf("update_agent_config: external_reach_fail_threshold must be 1..20")
		}
	}
	return nil
}

func validateAgentConfigString(arg, s string) error {
	switch arg {
	case "awgm_base_url":
		if s == "" {
			return nil
		}
		u, err := url.Parse(s)
		if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("update_agent_config: awgm_base_url must be an absolute http(s) URL")
		}
	case "awgm_login":
		if len(s) > 64 {
			return fmt.Errorf("update_agent_config: awgm_login too long (max 64)")
		}
	}
	return nil
}

// setConfigValue sets section.key = value (with the given yaml tag) in a parsed
// document, creating the section or key if absent. Comments and other keys are
// preserved because we mutate the node tree in place.
func setConfigValue(doc *yaml.Node, section, key, value, tag string) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("config is empty")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config top level is not a mapping")
	}
	secNode := mappingValue(root, section)
	if secNode == nil {
		secNode = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: section},
			secNode)
	}
	if secNode.Kind != yaml.MappingNode {
		return fmt.Errorf("config section %q is not a mapping", section)
	}
	if valNode := mappingValue(secNode, key); valNode != nil {
		valNode.Kind = yaml.ScalarNode
		valNode.Tag = tag
		valNode.Value = value
		valNode.Style = 0
		valNode.Content = nil
		return nil
	}
	secNode.Content = append(secNode.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
	return nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
