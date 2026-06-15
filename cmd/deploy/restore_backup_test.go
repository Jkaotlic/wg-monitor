package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectRestoreBackupReadsManifestAndAgents(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"state.db":     "sqlite bytes",
		"backend.yaml": validRestoreBackendYAML(),
		"manifest.txt": "name=wg-monitor-backup\ncreated_utc=20260522T055443Z\nbackend_version=v0.13.0-rc15\nhost=old-vps\n",
		"agents.csv":   "nickname,kind,last_seen_at,last_deployed_version\nalyaba,static,2026-05-22T05:00:00Z,v0.13.0-rc15\n",
	})

	backup, cleanup, err := InspectRestoreBackup(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if got := backup.Manifest["created_utc"]; got != "20260522T055443Z" {
		t.Fatalf("created_utc=%q", got)
	}
	if len(backup.Agents) != 1 || backup.Agents[0].Nickname != "alyaba" {
		t.Fatalf("agents not parsed: %#v", backup.Agents)
	}
	if _, err := os.Stat(backup.StateDBPath); err != nil {
		t.Fatalf("state.db not extracted: %v", err)
	}
	preview := RenderRestoreBackupPreview(backup)
	for _, want := range []string{"20260522T055443Z", "old-vps", "v0.13.0-rc15", "agents: 1"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview missing %q:\n%s", want, preview)
		}
	}
}

func TestInspectRestoreBackupRejectsTraversal(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"state.db":      "sqlite bytes",
		"backend.yaml":  "telegram:\n",
		"manifest.txt":  "name=wg-monitor-backup\n",
		"../escape.txt": "nope",
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "suspect path") {
		t.Fatalf("want traversal error, got %v", err)
	}
}

func TestInspectRestoreBackupRejectsNestedRestoreMembers(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"nested/state.db": "sqlite bytes",
		"backend.yaml":    validRestoreBackendYAML(),
		"manifest.txt":    "name=wg-monitor-backup\n",
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "unexpected restore member") {
		t.Fatalf("want nested member error, got %v", err)
	}
}

func TestInspectRestoreBackupRejectsDuplicateRestoreMembers(t *testing.T) {
	archive := writeRestoreBackupArchiveEntries(t, []restoreArchiveEntry{
		{name: "state.db", body: "sqlite bytes"},
		{name: "backend.yaml", body: validRestoreBackendYAML()},
		{name: "manifest.txt", body: "name=wg-monitor-backup\n"},
		{name: "state.db", body: "replacement bytes"},
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "duplicate restore member") {
		t.Fatalf("want duplicate member error, got %v", err)
	}
}

func TestInspectRestoreBackupRejectsInvalidBackendYAML(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"state.db":     "sqlite bytes",
		"backend.yaml": "telegram:\n  chat_id: [broken\n",
		"manifest.txt": "name=wg-monitor-backup\n",
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "backend.yaml") {
		t.Fatalf("want backend.yaml validation error, got %v", err)
	}
}

func TestInspectRestoreBackupRejectsEmptyBackendYAML(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"state.db":     "sqlite bytes",
		"backend.yaml": "\n# empty config\n",
		"manifest.txt": "name=wg-monitor-backup\n",
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "backend.yaml") {
		t.Fatalf("want backend.yaml validation error, got %v", err)
	}
}

func TestInspectRestoreBackupRejectsIncompleteBackendYAML(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"state.db":     "sqlite bytes",
		"backend.yaml": "telegram:\n  admin_user_id: 42\n",
		"manifest.txt": "name=wg-monitor-backup\n",
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "db_path") {
		t.Fatalf("want backend.yaml required-field error, got %v", err)
	}
}

func TestInspectRestoreBackupRejectsUnsupportedDBPath(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"state.db": "sqlite bytes",
		"backend.yaml": `db_path: /custom/state.db
telegram:
  bot_token_file: /etc/wg-monitor/bot-token.txt
  chat_id: -100123
  admin_user_id: 42
`,
		"manifest.txt": "name=wg-monitor-backup\n",
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "db_path") {
		t.Fatalf("want db_path validation error, got %v", err)
	}
}

func TestInspectRestoreBackupRejectsUnsupportedBotTokenPath(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"state.db": "sqlite bytes",
		"backend.yaml": `db_path: /var/lib/wg-monitor/state.db
telegram:
  bot_token_file: /custom/bot-token.txt
  chat_id: -100123
  admin_user_id: 42
wizard:
  token_file: /etc/wg-monitor/wizard-token.txt
`,
		"manifest.txt": "name=wg-monitor-backup\n",
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "telegram.bot_token_file") {
		t.Fatalf("want bot token path validation error, got %v", err)
	}
}

func TestInspectRestoreBackupRejectsMissingWizardTokenPath(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"state.db": "sqlite bytes",
		"backend.yaml": `db_path: /var/lib/wg-monitor/state.db
telegram:
  bot_token_file: /etc/wg-monitor/bot-token.txt
  chat_id: -100123
  admin_user_id: 42
`,
		"manifest.txt": "name=wg-monitor-backup\n",
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "wizard.token_file") {
		t.Fatalf("want wizard token path validation error, got %v", err)
	}
}

func TestInspectRestoreBackupRejectsUnsupportedBackendUpdatePath(t *testing.T) {
	archive := writeRestoreBackupArchive(t, map[string]string{
		"state.db": "sqlite bytes",
		"backend.yaml": `db_path: /var/lib/wg-monitor/state.db
telegram:
  bot_token_file: /etc/wg-monitor/bot-token.txt
  chat_id: -100123
  admin_user_id: 42
wizard:
  token_file: /etc/wg-monitor/wizard-token.txt
  backend_update_file: /custom/backend-update.json
`,
		"manifest.txt": "name=wg-monitor-backup\n",
	})

	_, cleanup, err := InspectRestoreBackup(archive)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "wizard.backend_update_file") {
		t.Fatalf("want backend update path validation error, got %v", err)
	}
}

func TestBuildRestoreRemoteScriptSafetySteps(t *testing.T) {
	script := buildRestoreRemoteScript("20260522T055443Z")
	for _, want := range []string{
		"systemctl stop wg-monitor-backend",
		"PRAGMA integrity_check",
		"cp -p /var/lib/wg-monitor/state.db /var/lib/wg-monitor/state.db.bak.20260522T055443Z",
		"install -m 640 -o root -g wgmonitor /tmp/wg-monitor-restore/backend.yaml /etc/wg-monitor/backend.yaml",
		"install -m 600 -o wgmonitor -g wgmonitor /tmp/wg-monitor-restore/state.db /var/lib/wg-monitor/state.db",
		"systemctl start wg-monitor-backend",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("restore script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildRestoreRemoteScriptValidatesTokensBeforeStop(t *testing.T) {
	script := buildRestoreRemoteScript("20260522T055443Z")
	stop := strings.Index(script, "systemctl stop wg-monitor-backend")
	bot := strings.Index(script, "test -s /etc/wg-monitor/bot-token.txt")
	wizard := strings.Index(script, "test -s /etc/wg-monitor/wizard-token.txt")
	if stop < 0 || bot < 0 || wizard < 0 {
		t.Fatalf("restore script missing expected steps:\n%s", script)
	}
	if bot > stop || wizard > stop {
		t.Fatalf("token preflight must run before systemctl stop:\n%s", script)
	}
}

func writeRestoreBackupArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	entries := make([]restoreArchiveEntry, 0, len(files))
	for name, body := range files {
		entries = append(entries, restoreArchiveEntry{name: name, body: body})
	}
	return writeRestoreBackupArchiveEntries(t, entries)
}

type restoreArchiveEntry struct {
	name string
	body string
}

func writeRestoreBackupArchiveEntries(t *testing.T, entries []restoreArchiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.tgz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func validRestoreBackendYAML() string {
	return `db_path: /var/lib/wg-monitor/state.db
telegram:
  bot_token_file: /etc/wg-monitor/bot-token.txt
  chat_id: -100123
  admin_user_id: 42
wizard:
  token_file: /etc/wg-monitor/wizard-token.txt
  backend_update_file: /var/lib/wg-monitor/backend-update.json
`
}
