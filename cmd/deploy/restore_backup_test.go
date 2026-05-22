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
		"backend.yaml": "telegram:\n  admin_user_id: 42\n",
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

func writeRestoreBackupArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.tgz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
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
