package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

func TestImportEncryptedFullBackupImportsOperatorVault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WG_NO_SECRET_CACHE", "")
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local"))
	t.Setenv("APPDATA", filepath.Join(dir, "roaming"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("HOME", dir)

	operatorPlain := makeTGZForTest(t, map[string]string{
		"secrets.env": "WIZARD_TOKEN=abc\nWG_AGENT_TOKEN_TESTKEEN=raw\n",
		"wizard.toml": "schema_version = 1\n",
	})
	operatorEnc, err := backup.Encrypt(operatorPlain, []byte("pass"), backup.TestParams())
	if err != nil {
		t.Fatal(err)
	}
	fullPlain := makeTGZForTest(t, map[string]string{
		"state.db":                 "sqlite bytes",
		"backend.yaml":             validRestoreBackendYAML(),
		"agents.csv":               "nickname,kind\n",
		"manifest.txt":             "name=wg-monitor-full-backup\nformat=encrypted-full-v1\n",
		"operator-secrets.tgz.enc": string(operatorEnc),
	})
	fullEnc, err := backup.Encrypt(fullPlain, []byte("pass"), backup.TestParams())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "full.tgz.enc")
	if err := os.WriteFile(path, fullEnc, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ImportEncryptedFullBackup(path, "pass", true); err != nil {
		t.Fatal(err)
	}
	secBody, err := os.ReadFile(secretsCachePath())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(secBody); got != "WIZARD_TOKEN=abc\nWG_AGENT_TOKEN_TESTKEEN=raw\n" {
		t.Fatalf("secrets mismatch: %q", got)
	}
	if _, err := os.Stat(DefaultStatePath()); err != nil {
		t.Fatalf("state not imported: %v", err)
	}
}

func TestImportEncryptedFullBackupRejectsWrongPassword(t *testing.T) {
	dir := t.TempDir()
	fullEnc, err := backup.Encrypt(makeTGZForTest(t, map[string]string{
		"state.db": "sqlite bytes", "backend.yaml": "x", "agents.csv": "x", "manifest.txt": "x",
	}), []byte("right"), backup.TestParams())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "full.tgz.enc")
	if err := os.WriteFile(path, fullEnc, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ImportEncryptedFullBackup(path, "wrong", true); err == nil {
		t.Fatal("wrong password imported")
	}
}

func makeTGZForTest(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range members {
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
	return buf.Bytes()
}
