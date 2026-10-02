package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Jkaotlic/wg-monitor/internal/installtmpl"
)

func TestRenderBackendYAML(t *testing.T) {
	got, err := RenderBackendYAML(BackendParams{
		PublicBaseURL: "https://wg.example.test",
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
	// Группы больше нет (цикл 5): шаблон не должен заводить её заново -- ни
	// chat_id, ни списка групп, ни настроек клавиатур бота.
	for _, dont := range []string{"agents:", `bot_token:`, "chat_id:", "extra_chat_ids:", "compat_inline_keyboard"} {
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
		"wg-monitor-backup-verify.service",
		"wg-monitor-backup-verify.timer",
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

// TestEmbeddedTemplatesContainNoCarriageReturns guards against a Windows
// checkout (core.autocrlf=true) reintroducing CRLF into a *.tmpl/*.timer
// file that isn't pinned to eol=lf in .gitattributes: go:embed captures
// whatever is on disk at build time, and a stray \r breaks `set -eu` under
// dash on the router.
func TestEmbeddedTemplatesContainNoCarriageReturns(t *testing.T) {
	entries, err := templatesFS.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded templates FS is empty")
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		raw, err := templatesFS.ReadFile("templates/" + name)
		if err != nil {
			t.Fatalf("read embedded template %s: %v", name, err)
		}
		if strings.ContainsRune(string(raw), '\r') {
			t.Errorf("embedded template %s contains a carriage return (CRLF checkout corruption) - pin eol=lf in .gitattributes", name)
		}
	}
}

// TestRenderedAWGMBootstrapScriptContainsNoCarriageReturns renders the
// bootstrap script the way production code does and checks the fully
// rendered output (template + interpolated agent.yaml + init script), not
// just the raw .tmpl file on disk.
func TestRenderedAWGMBootstrapScriptContainsNoCarriageReturns(t *testing.T) {
	script, err := RenderAWGMBootstrapScript(AWGMBootstrapParams{
		Nickname:     "crlfcheck",
		BackendURL:   "https://wg.example.test",
		RawToken:     strings.Repeat("a", 64),
		Version:      "v0.13.0-rc1",
		DownloadURL:  "https://example.test/wg-monitor-linux-arm64",
		ChecksumName: "wg-monitor-linux-arm64",
		ExpectedSHA:  strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("RenderAWGMBootstrapScript: %v", err)
	}
	if strings.ContainsRune(script, '\r') {
		t.Fatalf("rendered awgm-bootstrap script contains a carriage return; dash on the router fails on `set -eu\\r`:\n%q", script)
	}
}

func TestRenderBackupServiceSupportsDockerLayout(t *testing.T) {
	got, err := RenderBackupService(BackupServiceParams{
		User:            "user",
		Group:           "user",
		BinaryPath:      "/home/user/wg-monitor/bin/wg-monitor-backend",
		ConfigPath:      "/home/user/wg-monitor/config/backend.yaml",
		PassphrasePath:  "/home/user/wg-monitor/secrets/backup-passphrase.txt",
		OperatorVault:   "/home/user/wg-monitor/secrets/operator-secrets.tgz.enc",
		OutDir:          "/home/user/wg-monitor/data/backups",
		LayoutRoot:      "/home/user/wg-monitor",
		ReadWritePath:   "/home/user/wg-monitor",
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
		"ExecStart=/home/user/wg-monitor/bin/wg-monitor-backend backup",
		"--layout-root /home/user/wg-monitor",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("docker backup service missing %q\nfull:\n%s", want, svc)
		}
	}
	for _, bad := range []string{"User=user", "Group=user", "CapabilityBoundingSet=", "ProtectSystem=strict", "ReadWritePaths=", "ProtectHome="} {
		if strings.Contains(svc, bad) {
			t.Errorf("docker user backup service must omit %q\nfull:\n%s", bad, svc)
		}
	}
}

// v0.53: служба бэкапа делает оба архива одним запуском и чистит старые.
func TestBackupServiceRunsBothKindsWithRetention(t *testing.T) {
	static, err := ReadStaticTemplate("wg-monitor-backup.service")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderBackupService(BackupServiceParams{SendTelegram: true})
	if err != nil {
		t.Fatal(err)
	}
	const wantExec = "ExecStart=/usr/local/bin/wg-monitor-backend backup --config /etc/wg-monitor/backend.yaml " +
		"--passphrase-file /etc/wg-monitor/backup-passphrase.txt --operator-vault /var/lib/wg-monitor/operator-secrets.tgz.enc " +
		"--out-dir /var/lib/wg-monitor/backups --kind both " +
		"--small-keep-daily 7 --small-keep-weekly 4 --full-keep-daily 3 --full-keep-weekly 0 --send-telegram\n"
	for name, svc := range map[string]string{"static": string(static), "rendered": string(rendered)} {
		if !strings.Contains(svc, wantExec) {
			t.Errorf("%s: ExecStart не тот, ждали\n%s\nполучили:\n%s", name, wantExec, svc)
		}
		if strings.Count(svc, "ExecStart=") != 1 {
			t.Errorf("%s: ExecStart должен быть один", name)
		}
		if strings.Contains(svc, "--offsite") {
			t.Errorf("%s: внешняя цель не настроена, а флаг есть", name)
		}
	}
}

func TestRenderBackupServiceOffsiteTarget(t *testing.T) {
	got, err := RenderBackupService(BackupServiceParams{
		SendTelegram: true,
		OffsiteSCP:   "backup@198.51.100.20:/srv/wg-monitor/",
		OffsiteKey:   "/etc/wg-monitor/offsite_ed25519",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := " --full-keep-weekly 0 --offsite-scp backup@198.51.100.20:/srv/wg-monitor/ --offsite-key /etc/wg-monitor/offsite_ed25519 --send-telegram\n"; !strings.Contains(string(got), want) {
		t.Fatalf("нет флагов внешней цели %q:\n%s", want, got)
	}
	for name, p := range map[string]BackupServiceParams{
		"цель без ключа":    {OffsiteSCP: "backup@198.51.100.20:/srv/"},
		"ключ без цели":     {OffsiteKey: "/etc/wg-monitor/k"},
		"пробел в цели":     {OffsiteSCP: "backup@198.51.100.20:/srv/a b", OffsiteKey: "/k"},
		"перевод строки":    {OffsiteSCP: "backup@198.51.100.20:/srv/\nExecStartPre=/bin/x", OffsiteKey: "/k"},
		"не user@host:path": {OffsiteSCP: "198.51.100.20", OffsiteKey: "/k"},
		"ключ с пробелом":   {OffsiteSCP: "backup@198.51.100.20:/srv/", OffsiteKey: "/etc/my key"},
		"цель с дефиса":     {OffsiteSCP: "-oProxyCommand=x@y:/z", OffsiteKey: "/k"},
	} {
		if _, err := RenderBackupService(p); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
}

func TestBackupVerifyUnits(t *testing.T) {
	timer, err := ReadStaticTemplate("wg-monitor-backup-verify.timer")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OnCalendar=Sun *-*-* 06:30:00 Europe/Moscow", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(string(timer), want) {
			t.Errorf("в таймере проверки нет %q:\n%s", want, timer)
		}
	}
	static, err := ReadStaticTemplate("wg-monitor-backup-verify.service")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderBackupVerifyService(BackupServiceParams{})
	if err != nil {
		t.Fatal(err)
	}
	const wantExec = "ExecStart=/usr/local/bin/wg-monitor-backend backup verify --config /etc/wg-monitor/backend.yaml " +
		"--passphrase-file /etc/wg-monitor/backup-passphrase.txt --out-dir /var/lib/wg-monitor/backups\n"
	for name, svc := range map[string]string{"static": string(static), "rendered": string(rendered)} {
		for _, want := range []string{wantExec, "Type=oneshot", "User=wgmonitor", "ReadWritePaths=/var/lib/wg-monitor", "ProtectSystem=strict"} {
			if !strings.Contains(svc, want) {
				t.Errorf("%s: в службе проверки нет %q:\n%s", name, want, svc)
			}
		}
		if strings.Contains(svc, "--send-telegram") || strings.Contains(svc, "--operator-vault") {
			t.Errorf("%s: лишние флаги в службе проверки:\n%s", name, svc)
		}
	}
	docker, err := RenderBackupVerifyService(BackupServiceParams{
		BinaryPath:     "/home/user/wg-monitor/bin/wg-monitor-backend",
		ConfigPath:     "/home/user/wg-monitor/config/backend.yaml",
		PassphrasePath: "/home/user/wg-monitor/secrets/backup-passphrase.txt",
		OutDir:         "/home/user/wg-monitor/data/backups",
		LayoutRoot:     "/home/user/wg-monitor",
		OmitUserGroup:  true,
		OmitHardening:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "ExecStart=/home/user/wg-monitor/bin/wg-monitor-backend backup verify --config /home/user/wg-monitor/config/backend.yaml " +
		"--passphrase-file /home/user/wg-monitor/secrets/backup-passphrase.txt --out-dir /home/user/wg-monitor/data/backups --layout-root /home/user/wg-monitor\n"; !strings.Contains(string(docker), want) {
		t.Fatalf("докер-раскладка:\n%s", docker)
	}
	for _, bad := range []string{"User=", "ProtectSystem="} {
		if strings.Contains(string(docker), bad) {
			t.Errorf("пользовательская служба не должна содержать %q", bad)
		}
	}
}

func TestBackupLayoutCarriesOffsiteFromState(t *testing.T) {
	st := &State{}
	st.Backend.BackupOffsiteSCP = "backup@198.51.100.20:/srv/wg-monitor/"
	st.Backend.BackupOffsiteKey = "/etc/wg-monitor/offsite_ed25519"
	layout := backupLayoutForState(st)
	if layout.OffsiteSCP != st.Backend.BackupOffsiteSCP || layout.OffsiteKey != st.Backend.BackupOffsiteKey {
		t.Fatalf("раскладка без внешней цели: %+v", layout)
	}
	if l := backupLayoutForState(&State{}); l.OffsiteSCP != "" || l.OffsiteKey != "" {
		t.Fatalf("внешняя цель из ниоткуда: %+v", l)
	}
}
