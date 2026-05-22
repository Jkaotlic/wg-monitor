package main

import (
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
		"WG_BOT_TOKEN":            false,
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
