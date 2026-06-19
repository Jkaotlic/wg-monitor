package main

import (
	"strings"
	"testing"
)

const reviveSampleConfig = `backend:
  url: https://wgmonitor.anexaev.crazedns.ru
  token: "secret-token-123"

agent:
  nickname: snekhaev
  interval_sec: 60

awg_manager:
  base_url: http://127.0.0.1:2222
`

func TestReplaceAgentConfigBackendURLRewritesAndPreservesRest(t *testing.T) {
	out, changed, err := replaceAgentConfigBackendURL(reviveSampleConfig, "https://wg.snekhaev.example")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(out, "url: https://wg.snekhaev.example") {
		t.Fatalf("new url not written:\n%s", out)
	}
	// Token + other sections preserved; old URL gone.
	if strings.Contains(out, "anexaev.crazedns.ru") {
		t.Fatalf("old url still present:\n%s", out)
	}
	if !strings.Contains(out, `token: "secret-token-123"`) || !strings.Contains(out, "nickname: snekhaev") ||
		!strings.Contains(out, "base_url: http://127.0.0.1:2222") {
		t.Fatalf("preserved keys lost:\n%s", out)
	}
}

func TestReplaceAgentConfigBackendURLNoChangeWhenAlreadyCorrect(t *testing.T) {
	_, changed, err := replaceAgentConfigBackendURL(reviveSampleConfig, "https://wgmonitor.anexaev.crazedns.ru")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected changed=false when url already matches")
	}
}

func TestReplaceAgentConfigBackendURLOnlyTouchesBackendSection(t *testing.T) {
	// A `url:` key in another section (awg_manager) must NOT be rewritten.
	cfg := "backend:\n  url: https://old.example\n  token: t\n\nawg_manager:\n  url: http://127.0.0.1:2222\n"
	out, changed, err := replaceAgentConfigBackendURL(cfg, "https://new.example")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(out, "url: https://new.example") {
		t.Fatalf("backend url not updated:\n%s", out)
	}
	if !strings.Contains(out, "url: http://127.0.0.1:2222") {
		t.Fatalf("awg_manager url must be untouched:\n%s", out)
	}
}

func TestReplaceAgentConfigBackendURLErrorsWhenNoBackendURL(t *testing.T) {
	if _, _, err := replaceAgentConfigBackendURL("agent:\n  nickname: x\n", "https://new.example"); err == nil {
		t.Fatal("expected error when backend.url is absent")
	}
}
