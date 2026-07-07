package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type RestoreBackup struct {
	ArchivePath     string
	TempDir         string
	StateDBPath     string
	BackendYAMLPath string
	ManifestPath    string
	AgentsPath      string
	Manifest        map[string]string
	Agents          []RestoreAgent
}

type RestoreAgent struct {
	Nickname            string
	Kind                string
	LastSeenAt          string
	LastDeployedVersion string
}

type RestoreBackupOptions struct {
	ArchivePath string
	Mode        string
	DryRun      bool
}

type restoreBackendConfig struct {
	DBPath   string `yaml:"db_path"`
	Telegram struct {
		BotTokenFile string `yaml:"bot_token_file"`
		ChatID       int64  `yaml:"chat_id"`
		AdminUserID  int64  `yaml:"admin_user_id"`
	} `yaml:"telegram"`
	Wizard struct {
		TokenFile         string `yaml:"token_file"`
		BackendUpdateFile string `yaml:"backend_update_file"`
	} `yaml:"wizard"`
}

const (
	restoreRemoteDBPath            = "/var/lib/wg-monitor/state.db"
	restoreRemoteBotTokenPath      = "/etc/wg-monitor/bot-token.txt"    // #nosec G101 -- filesystem path for restored token file, not token material.
	restoreRemoteWizardTokenPath   = "/etc/wg-monitor/wizard-token.txt" // #nosec G101 -- filesystem path for restored token file, not token material.
	restoreRemoteBackendUpdatePath = "/var/lib/wg-monitor/backend-update.json"
)

func InspectRestoreBackup(archivePath string) (*RestoreBackup, func(), error) {
	return inspectRestoreBackup(archivePath, validateRestoreBackendYAML)
}

func InspectRestoreBackupForImport(archivePath string) (*RestoreBackup, func(), error) {
	return inspectRestoreBackup(archivePath, validateRestoreBackendYAMLForImport)
}

func inspectRestoreBackup(archivePath string, validateBackendYAML func(string) error) (*RestoreBackup, func(), error) {
	tmpDir, err := os.MkdirTemp("", "wg-monitor-restore-*")
	if err != nil {
		return nil, nil, fmt.Errorf("temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	in, err := os.Open(archivePath)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("open backup archive: %w", err)
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("gzip backup archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	seen := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("tar backup archive: %w", err)
		}
		//lint:ignore SA1019 accept both old (TypeRegA/NUL) and new (TypeReg/'0') regular-file
		// markers — backup archives may be produced by non-Go tar implementations that still
		// emit the legacy flag; TypeReg alone would silently skip those members.
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		name := filepath.ToSlash(hdr.Name)
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			cleanup()
			return nil, nil, fmt.Errorf("backup archive contains suspect path %q", hdr.Name)
		}
		base := filepath.Base(name)
		switch name {
		case "state.db", "backend.yaml", "manifest.txt", "agents.csv":
			if seen[name] != "" {
				cleanup()
				return nil, nil, fmt.Errorf("backup archive contains duplicate restore member %q", hdr.Name)
			}
			dst := filepath.Join(tmpDir, name)
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("create extracted %s: %w", name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				cleanup()
				return nil, nil, fmt.Errorf("extract %s: %w", name, err)
			}
			if err := f.Close(); err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("close extracted %s: %w", name, err)
			}
			seen[name] = dst
		default:
			switch base {
			case "state.db", "backend.yaml", "manifest.txt", "agents.csv":
				cleanup()
				return nil, nil, fmt.Errorf("backup archive contains unexpected restore member path %q", hdr.Name)
			}
			// Ignore future archive members.
		}
	}

	for _, required := range []string{"state.db", "backend.yaml", "manifest.txt"} {
		if seen[required] == "" {
			cleanup()
			return nil, nil, fmt.Errorf("backup archive missing %s", required)
		}
	}
	if validateBackendYAML != nil {
		if err := validateBackendYAML(seen["backend.yaml"]); err != nil {
			cleanup()
			return nil, nil, err
		}
	}

	manifest, err := readRestoreManifest(seen["manifest.txt"])
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	agents, err := readRestoreAgents(seen["agents.csv"])
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	return &RestoreBackup{
		ArchivePath:     archivePath,
		TempDir:         tmpDir,
		StateDBPath:     seen["state.db"],
		BackendYAMLPath: seen["backend.yaml"],
		ManifestPath:    seen["manifest.txt"],
		AgentsPath:      seen["agents.csv"],
		Manifest:        manifest,
		Agents:          agents,
	}, cleanup, nil
}

func readRestoreBackendYAML(path string) (*restoreBackendConfig, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read backend.yaml: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("validate backend.yaml: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || len(doc.Content[0].Content) == 0 {
		return nil, fmt.Errorf("validate backend.yaml: expected a non-empty YAML mapping")
	}
	var cfg restoreBackendConfig
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("validate backend.yaml fields: %w", err)
	}
	return &cfg, nil
}

func validateRestoreBackendYAML(path string) error {
	cfg, err := readRestoreBackendYAML(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.DBPath) == "" {
		return fmt.Errorf("validate backend.yaml: db_path is required")
	}
	if strings.TrimSpace(cfg.DBPath) != restoreRemoteDBPath {
		return fmt.Errorf("validate backend.yaml: db_path must be %s for this restore flow, got %q", restoreRemoteDBPath, strings.TrimSpace(cfg.DBPath))
	}
	if strings.TrimSpace(cfg.Telegram.BotTokenFile) == "" {
		return fmt.Errorf("validate backend.yaml: telegram.bot_token_file is required")
	}
	if strings.TrimSpace(cfg.Telegram.BotTokenFile) != restoreRemoteBotTokenPath {
		return fmt.Errorf("validate backend.yaml: telegram.bot_token_file must be %s for this restore flow, got %q", restoreRemoteBotTokenPath, strings.TrimSpace(cfg.Telegram.BotTokenFile))
	}
	if cfg.Telegram.ChatID == 0 {
		return fmt.Errorf("validate backend.yaml: telegram.chat_id is required")
	}
	if cfg.Telegram.AdminUserID == 0 {
		return fmt.Errorf("validate backend.yaml: telegram.admin_user_id is required")
	}
	if strings.TrimSpace(cfg.Wizard.TokenFile) == "" {
		return fmt.Errorf("validate backend.yaml: wizard.token_file is required")
	}
	if strings.TrimSpace(cfg.Wizard.TokenFile) != restoreRemoteWizardTokenPath {
		return fmt.Errorf("validate backend.yaml: wizard.token_file must be %s for this restore flow, got %q", restoreRemoteWizardTokenPath, strings.TrimSpace(cfg.Wizard.TokenFile))
	}
	if updatePath := strings.TrimSpace(cfg.Wizard.BackendUpdateFile); updatePath != "" && updatePath != restoreRemoteBackendUpdatePath {
		return fmt.Errorf("validate backend.yaml: wizard.backend_update_file must be empty or %s for this restore flow, got %q", restoreRemoteBackendUpdatePath, updatePath)
	}
	return nil
}

func validateRestoreBackendYAMLForImport(path string) error {
	cfg, err := readRestoreBackendYAML(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.DBPath) == "" {
		return fmt.Errorf("validate backend.yaml: db_path is required")
	}
	if strings.TrimSpace(cfg.Telegram.BotTokenFile) == "" {
		return fmt.Errorf("validate backend.yaml: telegram.bot_token_file is required")
	}
	if cfg.Telegram.ChatID == 0 && cfg.Telegram.AdminUserID == 0 {
		return fmt.Errorf("validate backend.yaml: telegram.chat_id or telegram.admin_user_id is required")
	}
	return nil
}

func readRestoreManifest(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	out := map[string]string{}
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

func readRestoreAgents(path string) ([]RestoreAgent, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open agents.csv: %w", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse agents.csv: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	idx := map[string]int{}
	for i, col := range rows[0] {
		idx[col] = i
	}
	var out []RestoreAgent
	for _, row := range rows[1:] {
		get := func(name string) string {
			i, ok := idx[name]
			if !ok || i >= len(row) {
				return ""
			}
			return row[i]
		}
		if nick := strings.TrimSpace(get("nickname")); nick != "" {
			out = append(out, RestoreAgent{
				Nickname:            nick,
				Kind:                get("kind"),
				LastSeenAt:          get("last_seen_at"),
				LastDeployedVersion: get("last_deployed_version"),
			})
		}
	}
	return out, nil
}

func RenderRestoreBackupPreview(b *RestoreBackup) string {
	info, _ := os.Stat(b.StateDBPath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	return fmt.Sprintf(
		"backup: %s\ncreated: %s\nhost: %s\nbackend: %s\nstate.db: %d bytes\nagents: %d",
		b.ArchivePath,
		emptyDash(b.Manifest["created_utc"]),
		emptyDash(b.Manifest["host"]),
		emptyDash(b.Manifest["backend_version"]),
		size,
		len(b.Agents),
	)
}

func actionRestoreBackup(state *State, secrets *SecretStore, dl *Downloader, opts RestoreBackupOptions) error {
	path := strings.TrimSpace(opts.ArchivePath)
	if path == "" {
		path = strings.TrimSpace(Ask("Path to wg-monitor backup .tgz", ""))
	}
	if path == "" {
		return fmt.Errorf("backup archive path is required")
	}

	backup, cleanup, err := InspectRestoreBackup(path)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Println(RenderRestoreBackupPreview(backup))

	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if opts.DryRun || mode == "dry-run" || mode == "inspect" {
		PrintOK("dry-run: backup archive is readable")
		return nil
	}
	if mode == "" {
		fmt.Println("  [1] Restore to current VPS")
		fmt.Println("  [2] Restore to NEW VPS")
		fmt.Println("  [3] Dry-run only")
		switch strings.TrimSpace(Ask("restore mode", "3")) {
		case "1":
			mode = "current"
		case "2":
			mode = "new"
		default:
			PrintOK("dry-run: no restore performed")
			return nil
		}
	}

	switch mode {
	case "current", "current-vps":
		return restoreBackupToCurrentVPS(state, secrets, backup)
	case "new", "new-vps":
		return restoreBackupToNewVPS(state, secrets, dl, backup)
	default:
		return fmt.Errorf("unknown restore mode %q", opts.Mode)
	}
}

func restoreBackupToCurrentVPS(state *State, secrets *SecretStore, backup *RestoreBackup) error {
	if state.Backend.Host == "" {
		return fmt.Errorf("backend VPS is not configured in wizard.toml")
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	PrintStep(1, 4, "SSH to current VPS")
	s, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return err
	}
	defer s.Close()

	PrintStep(2, 4, "Upload backup files")
	if err := uploadRestoreBackupFiles(s, backup); err != nil {
		return err
	}
	PrintStep(3, 4, "Apply backend state")
	if err := applyRestoreBackupOnRemote(s); err != nil {
		return err
	}
	PrintStep(4, 4, "Verify backend")
	return verifyRestoredBackend(s, state.Backend.Domain)
}

func restoreBackupToNewVPS(state *State, secrets *SecretStore, dl *Downloader, backup *RestoreBackup) error {
	rel, err := dl.GetLatestRelease()
	if err != nil {
		return err
	}
	PrintOK("latest release: " + rel.TagName)

	state.Backend.Host = Ask("NEW VPS host or IP", state.Backend.Host)
	state.Backend.Port = parseIntOr(Ask("SSH port", strOrDefault(state.Backend.Port, "22")), 22)
	state.Backend.User = orDefault(Ask("SSH user", strOrDefaultS(state.Backend.User, "root")), "root")
	configureBackendSSHAuth(state)
	state.Backend.Domain = cleanPromptDefaultLeak(Ask("New backend domain", state.Backend.Domain))
	caddyEmail := cleanPromptDefaultLeak(Ask("Email for Let's Encrypt", "admin@"+state.Backend.Domain))

	chatID, adminID := parseTelegramMetaFromYAMLFile(backup.BackendYAMLPath)
	if state.Telegram.ChatID == 0 {
		state.Telegram.ChatID = chatID
	}
	if state.Telegram.AdminUserID == 0 {
		state.Telegram.AdminUserID = adminID
	}
	if state.Telegram.ChatID == 0 {
		state.Telegram.ChatID = parseInt64Or(Ask("Telegram chat_id", ""), 0)
	}
	if state.Telegram.AdminUserID == 0 {
		state.Telegram.AdminUserID = parseInt64Or(Ask("Telegram admin user_id", ""), 0)
	}

	botToken, _ := secrets.Get("WG_BOT_TOKEN", "Telegram bot token (1234:ABC...)", nil)
	if botToken == "" || state.Backend.Host == "" || state.Backend.Domain == "" {
		return fmt.Errorf("missing required new VPS/domain/bot token fields")
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	PrintStep(1, 12, "SSH to NEW VPS")
	s, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return err
	}
	defer s.Close()

	PrintStep(2, 12, "Base user and directories")
	if err := stepEnsureUser(s, "wgmonitor"); err != nil {
		return err
	}
	stepEnsureDir(s, "/etc/wg-monitor", "")
	stepEnsureDir(s, "/var/lib/wg-monitor", "wgmonitor:wgmonitor")

	PrintStep(3, 12, "Restore backend.yaml")
	yamlBytes, err := os.ReadFile(backup.BackendYAMLPath)
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/wg-monitor/backend.yaml", yamlBytes, "640"); err != nil {
		return err
	}
	if _, err := s.MustRun("chown root:wgmonitor /etc/wg-monitor/backend.yaml"); err != nil {
		return err
	}

	PrintStep(4, 12, "bot-token.txt + wizard token")
	if err := stepUploadFile(s, "/etc/wg-monitor/bot-token.txt", []byte(strings.TrimSpace(botToken)+"\n"), "640"); err != nil {
		return err
	}
	if _, err := s.MustRun("chown root:wgmonitor /etc/wg-monitor/bot-token.txt"); err != nil {
		return err
	}
	if err := stepEnsureWizardSetup(s, secrets); err != nil {
		return err
	}

	PrintStep(5, 12, "systemd unit")
	unit, err := ReadStaticTemplate("wg-monitor-backend.service")
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/systemd/system/wg-monitor-backend.service", unit, "644"); err != nil {
		return err
	}
	if _, err := s.MustRun("systemctl daemon-reload && systemctl enable wg-monitor-backend"); err != nil {
		return err
	}

	PrintStep(6, 12, "encrypted nightly backup")
	if _, _, err := ensureBackupPassphrase(secrets, false); err != nil {
		return err
	}
	if err := installBackupOnBackend(state, secrets, s, backupLayoutForState(state)); err != nil {
		return err
	}
	if err := pushBackupSecretsToBackend(state, secrets, s, backupLayoutForState(state), ""); err != nil {
		PrintWarn("operator secrets vault not uploaded: " + err.Error())
	}

	PrintStep(7, 12, "Caddy")
	if err := stepInstallCaddy(s); err != nil {
		return err
	}
	cf, err := RenderCaddyfile(CaddyParams{Domain: state.Backend.Domain, Email: caddyEmail})
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/caddy/Caddyfile", cf, "644"); err != nil {
		return err
	}
	if _, err := s.MustRun("systemctl enable --now caddy && systemctl reload caddy"); err != nil {
		PrintWarn("caddy reload failed: " + err.Error())
	}

	PrintStep(8, 12, "Upload backend binary")
	localPath, err := stepDownloadBackendAsset(s, dl, rel)
	if err != nil {
		return err
	}
	if err := stepUploadAndSwap(s, localPath, "/usr/local/bin/wg-monitor-backend", backendInstallSwapService(existingInstallDetected(s))); err != nil {
		return err
	}

	PrintStep(9, 12, "Upload backup files")
	if err := uploadRestoreBackupFiles(s, backup); err != nil {
		return err
	}
	PrintStep(10, 12, "Apply restored DB/config")
	if err := applyRestoreBackupOnRemote(s); err != nil {
		return err
	}
	PrintStep(11, 12, "Verify backend")
	if err := verifyRestoredBackend(s, state.Backend.Domain); err != nil {
		return err
	}
	PrintStep(12, 12, "Agent next steps")
	PrintInfo("If the backend domain changed, run [4] Move to new VPS to rewrite agent backend URLs through AWG Manager.")

	state.Backend.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	state.Backend.LastDeployedVersion = rel.TagName
	return nil
}

func uploadRestoreBackupFiles(s *SSH, backup *RestoreBackup) error {
	if _, err := s.MustRun("rm -rf /tmp/wg-monitor-restore && mkdir -p /tmp/wg-monitor-restore"); err != nil {
		return err
	}
	db, err := os.ReadFile(backup.StateDBPath)
	if err != nil {
		return fmt.Errorf("read extracted state.db: %w", err)
	}
	if err := stepUploadFile(s, "/tmp/wg-monitor-restore/state.db", db, "600"); err != nil {
		return err
	}
	yamlBytes, err := os.ReadFile(backup.BackendYAMLPath)
	if err != nil {
		return fmt.Errorf("read extracted backend.yaml: %w", err)
	}
	return stepUploadFile(s, "/tmp/wg-monitor-restore/backend.yaml", yamlBytes, "600")
}

func applyRestoreBackupOnRemote(s *SSH) error {
	if err := ensureRemoteSQLite3(s); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	if _, err := s.MustRun(buildRestoreRemoteScript(stamp)); err != nil {
		return err
	}
	return nil
}

func buildRestoreRemoteScript(stamp string) string {
	return fmt.Sprintf(`set -eu
test "$(sqlite3 /tmp/wg-monitor-restore/state.db 'PRAGMA integrity_check;')" = "ok"
test -s /etc/wg-monitor/bot-token.txt
test -s /etc/wg-monitor/wizard-token.txt
rollback() {
	if [ -f /var/lib/wg-monitor/state.db.bak.%[1]s ]; then
		cp -p /var/lib/wg-monitor/state.db.bak.%[1]s /var/lib/wg-monitor/state.db || true
	fi
	if [ -f /etc/wg-monitor/backend.yaml.bak.%[1]s ]; then
		cp -p /etc/wg-monitor/backend.yaml.bak.%[1]s /etc/wg-monitor/backend.yaml || true
	fi
	systemctl start wg-monitor-backend 2>/dev/null || true
}
systemctl stop wg-monitor-backend 2>/dev/null || true
trap 'rc=$?; if [ "$rc" != 0 ]; then rollback; fi; exit $rc' EXIT
if [ -f /var/lib/wg-monitor/state.db ]; then cp -p /var/lib/wg-monitor/state.db /var/lib/wg-monitor/state.db.bak.%[1]s; fi
if [ -f /etc/wg-monitor/backend.yaml ]; then cp -p /etc/wg-monitor/backend.yaml /etc/wg-monitor/backend.yaml.bak.%[1]s; fi
install -m 600 -o wgmonitor -g wgmonitor /tmp/wg-monitor-restore/state.db /var/lib/wg-monitor/state.db
install -m 640 -o root -g wgmonitor /tmp/wg-monitor-restore/backend.yaml /etc/wg-monitor/backend.yaml
systemctl start wg-monitor-backend
trap - EXIT
`, stamp)
}

func verifyRestoredBackend(s *SSH, domain string) error {
	out, err := s.MustRun("systemctl is-active wg-monitor-backend")
	var healthErr error
	if strings.TrimSpace(domain) != "" {
		healthErr = stepVerifyBackendHealth(s, domain)
	}
	if err := restoredBackendVerificationResult(out, err, domain, healthErr); err != nil {
		return err
	}
	PrintOK("restored backend is active")
	return nil
}

func restoredBackendVerificationResult(activeOut string, activeErr error, domain string, healthErr error) error {
	if activeErr != nil {
		return activeErr
	}
	if strings.TrimSpace(activeOut) != "active" {
		return fmt.Errorf("wg-monitor-backend is %q", strings.TrimSpace(activeOut))
	}
	if strings.TrimSpace(domain) != "" && healthErr != nil {
		return fmt.Errorf("health check failed: %w", healthErr)
	}
	return nil
}

func parseTelegramMetaFromYAMLFile(path string) (chatID, adminUserID int64) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if rest, ok := strings.CutPrefix(line, "chat_id:"); ok {
			chatID = parseInt64Or(strings.TrimSpace(rest), 0)
		}
		if rest, ok := strings.CutPrefix(line, "admin_user_id:"); ok {
			adminUserID = parseInt64Or(strings.TrimSpace(rest), 0)
		}
	}
	return chatID, adminUserID
}
