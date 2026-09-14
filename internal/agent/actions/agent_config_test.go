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
	}, path, "")
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
	}, path, "")
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
			if _, err := UpdateAgentConfig(context.Background(), args, path, ""); err == nil {
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

// DNS-сторож включается удалённо только на роутерах оператора, поэтому его
// блок правится той же узкой дорогой, что и остальные безопасные ключи.
func TestUpdateAgentConfigSetsDNSWatchdog(t *testing.T) {
	path := writeSampleConfig(t)
	restarted := stubRestart(t)

	_, err := UpdateAgentConfig(context.Background(), map[string]any{
		"dns_watchdog_enabled":       true,
		"dns_watchdog_endpoint":      "https://dns.example.com/secret-path/dns-query",
		"dns_watchdog_canary_domain": "example.org",
		"dns_watchdog_bootstrap_ip":  "198.51.100.7",
	}, path, "")
	if err != nil {
		t.Fatal(err)
	}
	if !*restarted {
		t.Fatal("agent restart was not scheduled")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var full struct {
		Backend struct {
			URL string `yaml:"url"`
		} `yaml:"backend"`
		DNSWatchdog struct {
			Enabled      bool   `yaml:"enabled"`
			Endpoint     string `yaml:"endpoint"`
			CanaryDomain string `yaml:"canary_domain"`
			BootstrapIP  string `yaml:"bootstrap_ip"`
		} `yaml:"dns_watchdog"`
	}
	if err := yaml.Unmarshal(raw, &full); err != nil {
		t.Fatalf("result did not parse: %v\n%s", err, raw)
	}
	w := full.DNSWatchdog
	if !w.Enabled || w.Endpoint != "https://dns.example.com/secret-path/dns-query" ||
		w.CanaryDomain != "example.org" || w.BootstrapIP != "198.51.100.7" {
		t.Fatalf("dns_watchdog block=%+v\n%s", w, raw)
	}
	if full.Backend.URL != "https://wgmonitor.example.com" {
		t.Fatalf("backend block changed: %+v", full.Backend)
	}
}

// Путь эндпоинта -- секрет: по нему любой получит доступ к своему DNS-серверу.
// Ответ get_agent_config уходит на дашборд и в журнал команд, поэтому путь
// в нём не печатается никогда.
func TestGetAgentConfigMasksWatchdogEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := sampleAgentConfig + `
dns_watchdog:
  enabled: true
  endpoint: https://dns.example.com/very-secret-path/dns-query
  canary_domain: example.org
  bootstrap_ip: 198.51.100.7
`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := GetAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "very-secret-path") || strings.Contains(out, "dns-query") {
		t.Fatalf("endpoint path leaked into view: %s", out)
	}
	var v AgentConfigView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.DNSWatchdogEndpoint != "https://dns.example.com/***" {
		t.Fatalf("endpoint=%q, want masked https://dns.example.com/***", v.DNSWatchdogEndpoint)
	}
	if !v.DNSWatchdogEnabled || v.DNSWatchdogCanaryDomain != "example.org" || v.DNSWatchdogBootstrapIP != "198.51.100.7" {
		t.Fatalf("view=%+v", v)
	}
}

func TestGetAgentConfigWatchdogAbsentIsEmpty(t *testing.T) {
	out, err := GetAgentConfig(writeSampleConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	var v AgentConfigView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.DNSWatchdogEnabled || v.DNSWatchdogEndpoint != "" {
		t.Fatalf("absent block must read as off and empty: %+v", v)
	}
}

func TestUpdateAgentConfigRejectsBadWatchdogValues(t *testing.T) {
	cases := map[string]map[string]any{
		"http endpoint":         {"dns_watchdog_endpoint": "http://dns.example.com/secret"},
		"endpoint without host": {"dns_watchdog_endpoint": "https:///secret"},
		"empty endpoint":        {"dns_watchdog_endpoint": ""},
		"endpoint too long":     {"dns_watchdog_endpoint": "https://dns.example.com/" + strings.Repeat("a", 500)},
		// Маска из get_agent_config, отправленная обратно, стёрла бы секрет.
		"masked endpoint echoed": {"dns_watchdog_endpoint": "https://dns.example.com/***"},
		"bad ip":                 {"dns_watchdog_bootstrap_ip": "198.51.100.300"},
		"ipv6 bootstrap":         {"dns_watchdog_bootstrap_ip": "2001:db8::1"},
		"hostname as ip":         {"dns_watchdog_bootstrap_ip": "dns.example.com"},
		"bad domain":             {"dns_watchdog_canary_domain": "exa mple.com"},
		"domain with path":       {"dns_watchdog_canary_domain": "example.com/x"},
		"url as domain":          {"dns_watchdog_canary_domain": "https://example.com"},
		// Однословное имя (localhost, intranet) не находится никогда: сторож
		// считал бы свой DNS-сервер мёртвым вечно.
		"single-label domain": {"dns_watchdog_canary_domain": "localhost"},
		"single-label word":   {"dns_watchdog_canary_domain": "intranet"},
		"enabled wrong type":  {"dns_watchdog_enabled": "yes"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeSampleConfig(t)
			_ = stubRestart(t)
			before, _ := os.ReadFile(path)
			if _, err := UpdateAgentConfig(context.Background(), args, path, ""); err == nil {
				t.Fatal("expected validation error")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("config must not change on validation failure")
			}
		})
	}
}

// Включённый сторож без эндпоинта не заработает: LoadConfig агента выключит
// такой блок и только запишет ConfigError в журнал, так что после перезапуска
// сторож молча остался бы выключенным. Такой конфиг не пишется вовсе -- отказ
// виден на дашборде сразу.
func TestUpdateAgentConfigRefusesWatchdogWithoutEndpoint(t *testing.T) {
	path := writeSampleConfig(t)
	restarted := stubRestart(t)
	before, _ := os.ReadFile(path)
	_, err := UpdateAgentConfig(context.Background(), map[string]any{"dns_watchdog_enabled": true}, path, "")
	if err == nil || !strings.Contains(err.Error(), "dns_watchdog_endpoint") {
		t.Fatalf("err=%v, want refusal naming dns_watchdog_endpoint", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("config must not change when the watchdog would be enabled without an endpoint")
	}
	if *restarted {
		t.Fatal("agent must not restart on a refused change")
	}
}

// Пустой bootstrap_ip -- законное значение: «спросить адрес эндпоинта у
// 77.88.8.8». Его можно выставить, чтобы убрать устаревший адрес.
func TestUpdateAgentConfigClearsWatchdogBootstrapIP(t *testing.T) {
	path := writeSampleConfig(t)
	_ = stubRestart(t)
	if _, err := UpdateAgentConfig(context.Background(), map[string]any{"dns_watchdog_bootstrap_ip": ""}, path, ""); err != nil {
		t.Fatal(err)
	}
}

// Ответ update_agent_config показывается на дашборде в панели результата
// команды. Путь эндпоинта -- секрет, и печатать его там нельзя так же, как
// в get_agent_config.
func TestUpdateAgentConfigResultMasksWatchdogEndpoint(t *testing.T) {
	path := writeSampleConfig(t)
	_ = stubRestart(t)
	msg, err := UpdateAgentConfig(context.Background(), map[string]any{
		"dns_watchdog_endpoint": "https://dns.example.com/secret-path/dns-query",
	}, path, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "secret-path") || strings.Contains(msg, "dns-query") {
		t.Fatalf("endpoint path leaked into command result: %q", msg)
	}
	if !strings.Contains(msg, "dns_watchdog.endpoint=https://dns.example.com/***") {
		t.Fatalf("result should name the change with the masked endpoint: %q", msg)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "https://dns.example.com/secret-path/dns-query") {
		t.Fatalf("the real endpoint must still be written to config:\n%s", raw)
	}
}

const watchdogOnBlock = `
dns_watchdog:
  enabled: true
  endpoint: https://dns.example.com/secret-path/dns-query
`

func writeWatchdogOnConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(sampleAgentConfig+watchdogOnBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeWatchdogState(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dns-watchdog-state.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Сторож держит роутер на запасных: переключение живёт только в текущей
// конфигурации, и выключенный сторож оставил бы роутер там до перезагрузки,
// а смена endpoint сделала бы свою строку неузнаваемой. Агент отказывает,
// конфиг не трогает и не перезапускается.
func TestUpdateAgentConfigRefusesToDropAHoldingWatchdog(t *testing.T) {
	for _, state := range []string{`{"mode":"fallback"}`, `{"mode":"primary","pending":"return"}`, `{"mode":"primary","leftover":["x"]}`} {
		for name, args := range map[string]map[string]any{
			"выключение":     {"dns_watchdog_enabled": false},
			"смена endpoint": {"dns_watchdog_endpoint": "https://dns2.example.com/other-path"},
		} {
			path := writeWatchdogOnConfig(t)
			before, _ := os.ReadFile(path)
			restarted := stubRestart(t)
			_, err := UpdateAgentConfig(context.Background(), args, path, writeWatchdogState(t, state))
			if err == nil || !strings.Contains(err.Error(), "запасных") {
				t.Errorf("%s при %s: want отказ со словом «запасных», got %v", name, state, err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) || *restarted {
				t.Errorf("%s при %s: конфиг изменён или агент перезапущен", name, state)
			}
		}
	}
}

// Чистая запись или её отсутствие -- выключать можно. Включение и смена
// canary при грязной записи не запрещаются: они ничего не подвешивают.
func TestUpdateAgentConfigAllowsSafeWatchdogEdits(t *testing.T) {
	stubRestart(t)
	clean := writeWatchdogState(t, `{"mode":"primary"}`)
	if _, err := UpdateAgentConfig(context.Background(), map[string]any{"dns_watchdog_enabled": false}, writeWatchdogOnConfig(t), clean); err != nil {
		t.Errorf("чистая запись: %v", err)
	}
	if _, err := UpdateAgentConfig(context.Background(), map[string]any{"dns_watchdog_enabled": false}, writeWatchdogOnConfig(t), ""); err != nil {
		t.Errorf("без записи: %v", err)
	}
	dirty := writeWatchdogState(t, `{"mode":"fallback"}`)
	if _, err := UpdateAgentConfig(context.Background(), map[string]any{"dns_watchdog_canary_domain": "example.org"}, writeWatchdogOnConfig(t), dirty); err != nil {
		t.Errorf("canary при fallback: %v", err)
	}
}

// Запись есть, но не читается (права, каталог вместо файла) -- при сомнении
// запрещаем.
func TestUpdateAgentConfigRefusesWhenWatchdogStateUnreadable(t *testing.T) {
	stubRestart(t)
	if _, err := UpdateAgentConfig(context.Background(), map[string]any{"dns_watchdog_enabled": false}, writeWatchdogOnConfig(t), t.TempDir()); err == nil {
		t.Error("нечитаемая запись: want отказ")
	}
}

// writeWatchdogBrokenConfig: enabled: true, но endpoint -- маска из
// agent_config_get, а не настоящий адрес. LoadConfig такой блок не запустит
// (dnsWatchdogConfigProblem), значит "was on" по одному только файловому
// enabled -- ложь: сторож не бежит, некому чистить грязную запись.
func writeWatchdogBrokenConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	block := "\ndns_watchdog:\n  enabled: true\n  endpoint: https://dns.example.com/***\n"
	if err := os.WriteFile(path, []byte(sampleAgentConfig+block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Файл говорит enabled: true, но эндпойнт -- маска: LoadConfig такой блок
// глушит сам, сторож не бежит. "Выключить" отражает реальность и не должно
// упираться в отказ про "запасные" -- там некому было их держать.
func TestUpdateAgentConfigAllowsDisablingWatchdogThatConfigLoaderWouldReject(t *testing.T) {
	stubRestart(t)
	fallback := writeWatchdogState(t, `{"mode":"fallback"}`)
	if _, err := UpdateAgentConfig(context.Background(), map[string]any{"dns_watchdog_enabled": false}, writeWatchdogBrokenConfig(t), fallback); err != nil {
		t.Errorf("сломанный конфиг, выключение: want успех, got %v", err)
	}
}

// Та же сломанная запись -- операторский путь наружу это не выключение, а
// починка эндпойнта. Она тоже не должна упираться в отказ про "запасные".
func TestUpdateAgentConfigAllowsFixingEndpointOfWatchdogThatConfigLoaderWouldReject(t *testing.T) {
	stubRestart(t)
	fallback := writeWatchdogState(t, `{"mode":"fallback"}`)
	args := map[string]any{"dns_watchdog_endpoint": "https://dns2.example.com/new-secret-path"}
	if _, err := UpdateAgentConfig(context.Background(), args, writeWatchdogBrokenConfig(t), fallback); err != nil {
		t.Errorf("сломанный конфиг, починка endpoint: want успех, got %v", err)
	}
}

// Настоящий endpoint в файле хранится с YAML-кавычками и пробелами по краям
// ("  https://...  ") -- LoadConfig их обрезает перед проверкой, значит и
// пост-проверка, и "was on" обязаны сравнивать по обрезанному значению, иначе
// рабочий сторож ошибочно сочтут нерабочим или наоборот.
func TestUpdateAgentConfigTrimsWatchdogEndpointBeforeChecks(t *testing.T) {
	stubRestart(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	block := "\ndns_watchdog:\n  enabled: true\n  endpoint: \"  https://dns.example.com/secret-path/dns-query  \"\n"
	if err := os.WriteFile(path, []byte(sampleAgentConfig+block), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateAgentConfig(context.Background(), map[string]any{"interval_sec": 90}, path, ""); err != nil {
		t.Errorf("несвязанная правка при обрезаемом endpoint: want успех, got %v", err)
	}
}
