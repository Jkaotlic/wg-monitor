package actions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
	"gopkg.in/yaml.v3"
)

const sampleAgentConfig = `backend:
  url: https://wgmonitor.example.com
  token: "secret-token-123"

agent:
  nickname: testkeen
  interval_sec: 60

awg_manager:
  base_url: http://127.0.0.1:2222
  # login: admin

external_reach:
  enabled: true
  bind_to_default: true
  fail_threshold: 2

maintenance:
  allow_router_reboot: true
  allow_firmware_install: true
`

func writeSampleConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(sampleAgentConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func stubRestart(t *testing.T) *bool {
	t.Helper()
	called := false
	old := scheduleURLUpdateRestart
	scheduleURLUpdateRestart = func() { called = true }
	t.Cleanup(func() { scheduleURLUpdateRestart = old })
	return &called
}

func TestGetAgentConfigReadsSafeSubset(t *testing.T) {
	path := writeSampleConfig(t)
	out, err := GetAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var v AgentConfigView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.ConfigKind != "agent" || v.IntervalSec != 60 || v.AWGMBaseURL != "http://127.0.0.1:2222" ||
		!v.ExternalReachEnabled || v.ExternalReachFailThreshold != 2 ||
		!v.AllowRouterReboot || !v.AllowFirmwareInstall {
		t.Fatalf("view=%+v", v)
	}
}

func TestUpdateAgentConfigPatchesAndPreservesRest(t *testing.T) {
	path := writeSampleConfig(t)
	restarted := stubRestart(t)

	msg, err := UpdateAgentConfig(context.Background(), map[string]any{
		"interval_sec":        float64(90),
		"allow_router_reboot": false,
		"awgm_login":          "admin", // key present only as a comment → must be added
	}, path)
	if err != nil {
		t.Fatal(err)
	}
	if !*restarted {
		t.Fatal("agent restart was not scheduled")
	}
	if !strings.Contains(msg, "restarting agent") {
		t.Fatalf("message=%q", msg)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var full struct {
		Backend struct {
			URL   string `yaml:"url"`
			Token string `yaml:"token"`
		} `yaml:"backend"`
		Agent struct {
			Nickname    string `yaml:"nickname"`
			IntervalSec int    `yaml:"interval_sec"`
		} `yaml:"agent"`
		AwgManager struct {
			BaseURL string `yaml:"base_url"`
			Login   string `yaml:"login"`
		} `yaml:"awg_manager"`
		ExternalReach struct {
			Enabled       bool `yaml:"enabled"`
			BindToDefault bool `yaml:"bind_to_default"`
		} `yaml:"external_reach"`
		Maintenance struct {
			AllowRouterReboot    bool `yaml:"allow_router_reboot"`
			AllowFirmwareInstall bool `yaml:"allow_firmware_install"`
		} `yaml:"maintenance"`
	}
	if err := yaml.Unmarshal(raw, &full); err != nil {
		t.Fatalf("result did not parse: %v\n%s", err, raw)
	}
	// Applied changes.
	if full.Agent.IntervalSec != 90 {
		t.Fatalf("interval_sec not updated: %d", full.Agent.IntervalSec)
	}
	if full.Maintenance.AllowRouterReboot {
		t.Fatal("allow_router_reboot not set to false")
	}
	if full.AwgManager.Login != "admin" {
		t.Fatalf("awgm login not added: %q", full.AwgManager.Login)
	}
	// Untouched keys preserved.
	if full.Backend.URL != "https://wgmonitor.example.com" || full.Backend.Token != "secret-token-123" {
		t.Fatalf("backend block changed: %+v", full.Backend)
	}
	if full.Agent.Nickname != "testkeen" || !full.ExternalReach.BindToDefault || !full.Maintenance.AllowFirmwareInstall {
		t.Fatalf("untouched keys changed: %+v", full)
	}
	// Comments survive the round-trip.
	if !strings.Contains(string(raw), "#") {
		t.Fatalf("comments were stripped:\n%s", raw)
	}
}

func TestUpdateAgentConfigIgnoresNonWhitelistedKeys(t *testing.T) {
	path := writeSampleConfig(t)
	_ = stubRestart(t)

	// "url"/"token"/"nickname" are NOT whitelisted — must be ignored, and since
	// no whitelisted key is present, this is a no-op error rather than a change.
	_, err := UpdateAgentConfig(context.Background(), map[string]any{
		"url":      "https://evil.example",
		"token":    "stolen",
		"nickname": "hacked",
	}, path)
	if err == nil || !strings.Contains(err.Error(), "no recognized settings") {
		t.Fatalf("err=%v, want no-recognized-settings", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "https://wgmonitor.example.com") ||
		strings.Contains(string(raw), "evil.example") || strings.Contains(string(raw), "hacked") {
		t.Fatalf("non-whitelisted keys leaked into config:\n%s", raw)
	}
}

func TestUpdateAgentConfigRejectsBadValues(t *testing.T) {
	cases := map[string]map[string]any{
		"interval too small": {"interval_sec": float64(2)},
		"interval too big":   {"interval_sec": float64(999999)},
		"bad awgm url":       {"awgm_base_url": "not a url"},
		"bool wrong type":    {"allow_router_reboot": "yes"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeSampleConfig(t)
			_ = stubRestart(t)
			before, _ := os.ReadFile(path)
			if _, err := UpdateAgentConfig(context.Background(), args, path); err == nil {
				t.Fatal("expected validation error")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("config must not change on validation failure")
			}
		})
	}
}

func TestRunnerAgentConfigGetDispatches(t *testing.T) {
	path := writeSampleConfig(t)
	r := Runner{Now: mockNow(), ConfigPath: path}
	res := r.Execute(context.Background(), wire.Command{ID: "cfg-get", Action: "agent_config_get"})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, `"config_kind":"agent"`) {
		t.Fatalf("output missing discriminator: %s", res.Output)
	}
}
