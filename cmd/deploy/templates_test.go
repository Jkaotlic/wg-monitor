package main

import (
	"strings"
	"testing"
)

func TestRenderBackendYAML(t *testing.T) {
	got, err := RenderBackendYAML(BackendParams{
		ChatID:      -1001,
		AdminUserID: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		`bot_token_file: /etc/wg-monitor/bot-token.txt`,
		`chat_id: -1001`,
		`admin_user_id: 42`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered yaml missing %q\nfull:\n%s", want, s)
		}
	}
	// agents/users belong in the DB, not the yaml — make sure we didn't
	// accidentally re-introduce the legacy section that the running
	// backend's config loader silently ignores.
	for _, dont := range []string{"agents:", `bot_token:`} {
		if strings.Contains(s, dont) {
			t.Errorf("rendered yaml unexpectedly contains %q\nfull:\n%s", dont, s)
		}
	}
}

func TestRenderAgentYAML(t *testing.T) {
	got, err := RenderAgentYAML(AgentParams{
		BackendURL: "https://example.com",
		Token:      "feedface",
		Nickname:   "router1",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		"url: https://example.com",
		`token: "feedface"`,
		"nickname: router1",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered yaml missing %q", want)
		}
	}
	// awg_iface / expected_exit_ip — deprecated в агенте; шаблон не должен их рендерить.
	for _, dont := range []string{"awg_iface:", "expected_exit_ip:"} {
		if strings.Contains(s, dont) {
			t.Errorf("rendered yaml unexpectedly contains %q\nfull:\n%s", dont, s)
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
