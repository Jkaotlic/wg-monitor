package main

import (
	"strings"
	"testing"
)

func TestRenderBackendYAML(t *testing.T) {
	got, err := RenderBackendYAML(BackendParams{
		BotToken:    "1234:ABCD",
		ChatID:      -1001,
		AdminUserID: 42,
		Agents: []AgentEntry{
			{Nickname: "testkeen", Token: "deadbeef", ThreadID: 7},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		`bot_token: "1234:ABCD"`,
		`chat_id: -1001`,
		`admin_user_id: 42`,
		`nickname: testkeen`,
		`token: "deadbeef"`,
		`thread_id: 7`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered yaml missing %q\nfull:\n%s", want, s)
		}
	}
}

func TestRenderAgentYAML(t *testing.T) {
	got, err := RenderAgentYAML(AgentParams{
		BackendURL:     "https://example.com",
		Token:          "feedface",
		Nickname:       "router1",
		AWGIface:       "awg0",
		ExpectedExitIP: "1.2.3.4",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		"url: https://example.com",
		`token: "feedface"`,
		"nickname: router1",
		"awg_iface: awg0",
		"expected_exit_ip: 1.2.3.4",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered yaml missing %q", want)
		}
	}
}

func TestRenderCaddyfile(t *testing.T) {
	got, err := RenderCaddyfile(CaddyParams{
		Domain: "wgmon.example.com",
		Email:  "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, "wgmon.example.com {") {
		t.Errorf("Caddyfile missing domain block:\n%s", s)
	}
	if !strings.Contains(s, "email admin@example.com") {
		t.Errorf("Caddyfile missing email")
	}
}

func TestStaticTemplates(t *testing.T) {
	for _, name := range []string{"S99wg-monitor", "wg-monitor-backend.service"} {
		got, err := ReadStaticTemplate(name)
		if err != nil {
			t.Errorf("ReadStaticTemplate(%q): %v", name, err)
		}
		if len(got) == 0 {
			t.Errorf("empty %s", name)
		}
	}
}
