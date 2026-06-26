package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/anex/wg-monitor/internal/installtmpl"
)

func TestRenderBackendYAML(t *testing.T) {
	got, err := RenderBackendYAML(BackendParams{
		PublicBaseURL: "https://wg.example.test",
		ChatID:        -1001,
		ExtraChatIDs:  []int64{-1002, -1003},
		AdminUserID:   42,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	var parsed map[string]any
	if err := yaml.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("rendered yaml must parse: %v\n%s", err, s)
	}
	for _, want := range []string{
		`bot_token_file: /etc/wg-monitor/bot-token.txt`,
		`public_base_url: "https://wg.example.test"`,
		`chat_id: -1001`,
		`extra_chat_ids:`,
		`- -1002`,
		`- -1003`,
		`admin_user_id: 42`,
		`dashboard:`,
		`enabled: false`,
		`token_file: /etc/wg-monitor/dashboard-token.txt`,
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

func TestEnsureYAMLTelegramExtraChatID(t *testing.T) {
	raw := []byte("listen: 127.0.0.1:8080\ntelegram:\n  chat_id: -1001\n  extra_chat_ids: []\n  admin_user_id: 42\n")
	updated, changed, err := ensureYAMLTelegramExtraChatID(raw, -1002)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected yaml to change")
	}
	s := string(updated)
	for _, want := range []string{
		"telegram:",
		"chat_id: -1001",
		"extra_chat_ids:",
		"- -1002",
		"admin_user_id: 42",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("patched yaml missing %q\n%s", want, s)
		}
	}
	again, changed, err := ensureYAMLTelegramExtraChatID(updated, -1002)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("second patch should be idempotent:\n%s", string(again))
	}
}

func TestExtractThreadIDFromEnsureTopicsOutput(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want int
	}{
		{out: "+ created bronya - chat_id=-1002 topic id=777\n", want: 777},
		{out: "= skip bronya - already has topic id=888 (use --force to rebuild)\n", want: 888},
		{out: "Done: 0 created, 0 skipped, 0 failed\n", want: 0},
	} {
		if got := extractThreadIDFromEnsureTopicsOutput(tc.out); got != tc.want {
			t.Fatalf("extractThreadIDFromEnsureTopicsOutput(%q)=%d, want %d", tc.out, got, tc.want)
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
		"external_reach:",
		"enabled: true",
		"bind_to_default: true",
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
	for _, name := range []string{
		"wg-monitor-backend.service",
		"wg-monitor-backup.service",
		"wg-monitor-backup.timer",
	} {
		got, err := ReadStaticTemplate(name)
		if err != nil {
			t.Errorf("ReadStaticTemplate(%q): %v", name, err)
		}
		if len(got) == 0 {
			t.Errorf("empty %s", name)
		}
	}
}

func TestInitScriptAvailableViaInstalltmpl(t *testing.T) {
	s := installtmpl.InitScript()
	if len(s) == 0 {
		t.Fatal("installtmpl.InitScript() is empty")
	}
	if !strings.Contains(s, "S99wg-monitor") {
		t.Fatal("init script missing S99wg-monitor reference")
	}
}

func TestBackupTimerRunsDailyAtFiveMoscow(t *testing.T) {
	got, err := ReadStaticTemplate("wg-monitor-backup.timer")
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		"OnCalendar=*-*-* 05:00:00 Europe/Moscow",
		"Persistent=true",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("backup timer missing %q\nfull:\n%s", want, s)
		}
	}
}

func TestBackupServiceSendsTelegramBundle(t *testing.T) {
	service, err := ReadStaticTemplate("wg-monitor-backup.service")
	if err != nil {
		t.Fatal(err)
	}
	svc := string(service)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/wg-monitor-backend backup",
		"--passphrase-file /etc/wg-monitor/backup-passphrase.txt",
		"--operator-vault /var/lib/wg-monitor/operator-secrets.tgz.enc",
		"--send-telegram",
		"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("backup service missing %q\nfull:\n%s", want, svc)
		}
	}
}

func TestRenderBackupServiceSupportsDockerLayout(t *testing.T) {
	got, err := RenderBackupService(BackupServiceParams{
		User:            "anex",
		Group:           "anex",
		BinaryPath:      "/home/anex/wg-monitor/bin/wg-monitor-backend",
		ConfigPath:      "/home/anex/wg-monitor/config/backend.yaml",
		PassphrasePath:  "/home/anex/wg-monitor/secrets/backup-passphrase.txt",
		OperatorVault:   "/home/anex/wg-monitor/secrets/operator-secrets.tgz.enc",
		OutDir:          "/home/anex/wg-monitor/data/backups",
		LayoutRoot:      "/home/anex/wg-monitor",
		ReadWritePath:   "/home/anex/wg-monitor",
		SendTelegram:    true,
		ProtectHomeMode: "read-only",
		OmitUserGroup:   true,
		OmitHardening:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := string(got)
	for _, want := range []string{
		"ExecStart=/home/anex/wg-monitor/bin/wg-monitor-backend backup",
		"--layout-root /home/anex/wg-monitor",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("docker backup service missing %q\nfull:\n%s", want, svc)
		}
	}
	for _, bad := range []string{"User=anex", "Group=anex", "CapabilityBoundingSet=", "ProtectSystem=strict", "ReadWritePaths=", "ProtectHome="} {
		if strings.Contains(svc, bad) {
			t.Errorf("docker user backup service must omit %q\nfull:\n%s", bad, svc)
		}
	}
}
