package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUsageTextMentionsMenuAndCLIParity(t *testing.T) {
	got := usageText()
	for _, want := range []string{
		"update-components",
		"sync-vps",
		"adopt-backend",
		"add-router",
		"repair-agent-token",
		"netfix",
		"doctor [--deep]",
		"backup status",
		"secrets status",
		"Without a command",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("usage missing %q:\n%s", want, got)
		}
	}
}

func TestSecretStatusRowsIncludeBackendAndAgents(t *testing.T) {
	rows := secretStatusRows(&State{
		Backend: BackendState{Host: "vps.example", Domain: "bot.example"},
		Agents:  []AgentState{{Nickname: "home"}, {Nickname: "office"}},
	})
	got := map[string]bool{}
	seen := map[string]bool{}
	for _, row := range rows {
		got[row.Name] = row.Required
		seen[row.Name] = true
	}
	for name, required := range map[string]bool{
		"WG_VPS_PASS":             true,
		"WIZARD_TOKEN":            true,
		"WG_AGENT_TOKEN_HOME":     true,
		"WG_AGENT_TOKEN_OFFICE":   true,
		"WG_BOT_TOKEN":            true,
		"WG_BACKUP_PASSPHRASE":    true,
		"WG_KEENETIC_PASS":        false,
		"WG_KEENETIC_PASS_HOME":   false,
		"WG_KEENETIC_PASS_OFFICE": false,
	} {
		if !seen[name] {
			t.Fatalf("%s missing; rows=%+v", name, rows)
		}
		if got[name] != required {
			t.Fatalf("%s required=%v, want %v; rows=%+v", name, got[name], required, rows)
		}
	}
}

func TestActionSecretsStatusReturnsErrorWhenRequiredSecretsMissing(t *testing.T) {
	t.Setenv("WG_NO_SECRET_CACHE", "1")
	for _, name := range []string{"WG_VPS_PASS", "WG_BOT_TOKEN", "WIZARD_TOKEN"} {
		t.Setenv(name, "")
	}

	state := &State{Backend: BackendState{Host: "vps.example", Domain: "bot.example"}}
	err := actionSecretsStatus(state, NewSecretStore())
	if err == nil {
		t.Fatal("secrets status should fail when required secrets are missing")
	}
	for _, want := range []string{"WG_VPS_PASS", "WG_BOT_TOKEN", "WIZARD_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should mention %s, got %v", want, err)
		}
	}
}

func TestSecretStatusRowsPreferAWGMAPIKey(t *testing.T) {
	rows := secretStatusRows(&State{
		Agents: []AgentState{{
			Nickname:   "home",
			DeployMode: "awgm",
			AWGMURL:    "https://awg.home.example",
		}},
	})
	got := map[string]bool{}
	seen := map[string]bool{}
	for _, row := range rows {
		got[row.Name] = row.Required
		seen[row.Name] = true
	}
	for name, required := range map[string]bool{
		"WG_AWGM_API_KEY_HOME": true,
		"WG_AWGM_LOGIN_HOME":   false,
		"WG_AWGM_PASS_HOME":    false,
	} {
		if !seen[name] {
			t.Fatalf("%s missing; rows=%+v", name, rows)
		}
		if got[name] != required {
			t.Fatalf("%s required=%v, want %v; rows=%+v", name, got[name], required, rows)
		}
	}
}

func TestParsePositivePortRejectsInvalidPort(t *testing.T) {
	if _, err := parsePositivePort("abc"); err == nil {
		t.Fatal("expected invalid port error")
	}
	if _, err := parsePositivePort("0"); err == nil {
		t.Fatal("expected zero port error")
	}
	if got, err := parsePositivePort("222"); err != nil || got != 222 {
		t.Fatalf("parsePositivePort valid got=%d err=%v", got, err)
	}
}

func TestParseUninstallAgentArgsRejectsUnknownAndMissingValues(t *testing.T) {
	tests := [][]string{
		{"uninstall-agent", "--bogus"},
		{"uninstall-agent", "--agent"},
		{"uninstall-agent", "--host", "--port", "222"},
		{"uninstall-agent", "--port", "abc"},
		{"uninstall-agent", "--user", "--host"},
	}
	for _, args := range tests {
		if _, err := parseUninstallAgentArgs(args); err == nil {
			t.Fatalf("parseUninstallAgentArgs(%v) succeeded unexpectedly", args)
		}
	}
}

func TestParseUninstallAgentArgsAcceptsAgentAndManualTarget(t *testing.T) {
	byAgent, err := parseUninstallAgentArgs([]string{"uninstall-agent", "--agent", "home"})
	if err != nil {
		t.Fatal(err)
	}
	if byAgent.Nickname != "home" || byAgent.Port != 222 || byAgent.User != "root" {
		t.Fatalf("byAgent=%+v", byAgent)
	}
	manual, err := parseUninstallAgentArgs([]string{"uninstall-agent", "--host", "192.0.2.10", "--port", "2202", "--user", "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if manual.Host != "192.0.2.10" || manual.Port != 2202 || manual.User != "admin" {
		t.Fatalf("manual=%+v", manual)
	}
}

func TestCheckWizardEndpointAcceptsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/wizard/agents" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if err := checkWizardEndpoint(srv.URL + "/v1/wizard/agents"); err != nil {
		t.Fatalf("checkWizardEndpoint returned %v", err)
	}
}

func TestCheckWizardEndpointRejectsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := checkWizardEndpoint(srv.URL + "/v1/wizard/agents"); err == nil {
		t.Fatal("checkWizardEndpoint should reject missing wizard route")
	}
}
