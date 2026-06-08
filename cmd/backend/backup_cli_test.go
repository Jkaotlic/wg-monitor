package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
	_ "modernc.org/sqlite"
)

func TestRunBackupCommandWritesEncryptedFullBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	initBackupTestDB(t, dbPath)
	botPath := filepath.Join(dir, "bot-token.txt")
	wizPath := filepath.Join(dir, "wizard-token.txt")
	passPath := filepath.Join(dir, "backup-passphrase.txt")
	cfgPath := filepath.Join(dir, "backend.yaml")
	outDir := filepath.Join(dir, "backups")

	mustWrite(t, botPath, "bot-secret-token\n")
	mustWrite(t, wizPath, "wizard-secret-token\n")
	mustWrite(t, passPath, "backup password\n")
	mustWrite(t, cfgPath, "listen: 127.0.0.1:8080\n"+
		"db_path: "+slash(dbPath)+"\n"+
		"telegram:\n"+
		"  bot_token_file: "+slash(botPath)+"\n"+
		"  chat_id: -1001\n"+
		"  admin_user_id: 42\n"+
		"wizard:\n"+
		"  token_file: "+slash(wizPath)+"\n")

	if err := runBackupCommand([]string{
		"--config", cfgPath,
		"--passphrase-file", passPath,
		"--out-dir", outDir,
		"--test-kdf",
	}); err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob(filepath.Join(outDir, "wg-monitor-full-backup-*.tgz.enc"))
	if err != nil || len(files) != 1 {
		t.Fatalf("backup files=%v err=%v", files, err)
	}
	blob, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"bot-secret-token", "wizard-secret-token", "backup password"} {
		if bytes.Contains(blob, []byte(secret)) {
			t.Fatalf("encrypted blob contains plaintext %q", secret)
		}
	}
	plain, err := backup.Decrypt(blob, []byte("backup password"))
	if err != nil {
		t.Fatal(err)
	}
	members := tarMembers(t, plain)
	for _, want := range []string{"state.db", "backend.yaml", "bot-token.txt", "wizard-token.txt", "agents.csv", "manifest.txt"} {
		if !members[want] {
			t.Fatalf("backup missing %s; members=%v", want, members)
		}
	}
	if !strings.Contains(string(tarMember(t, plain, "agents.csv")), "testkeen") {
		t.Fatalf("agents.csv missing user: %s", string(tarMember(t, plain, "agents.csv")))
	}
}

func TestRunBackupCommandResolvesDockerLayoutRoot(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "wg-monitor")
	for _, sub := range []string{"data", "config", "secrets"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(root, "data", "state.db")
	initBackupTestDB(t, dbPath)
	mustWrite(t, filepath.Join(root, "secrets", "bot-token.txt"), "bot-secret-token\n")
	mustWrite(t, filepath.Join(root, "secrets", "wizard-token.txt"), "wizard-secret-token\n")
	mustWrite(t, filepath.Join(root, "secrets", "backup-passphrase.txt"), "backup password\n")
	cfgPath := filepath.Join(root, "config", "backend.yaml")
	mustWrite(t, cfgPath, "listen: 0.0.0.0:8080\n"+
		"db_path: /data/state.db\n"+
		"telegram:\n"+
		"  bot_token_file: /secrets/bot-token.txt\n"+
		"  chat_id: -1001\n"+
		"  admin_user_id: 42\n"+
		"wizard:\n"+
		"  token_file: /secrets/wizard-token.txt\n")

	if err := runBackupCommand([]string{
		"--config", cfgPath,
		"--passphrase-file", filepath.Join(root, "secrets", "backup-passphrase.txt"),
		"--layout-root", root,
		"--out-dir", filepath.Join(root, "data", "backups"),
		"--test-kdf",
	}); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "data", "backups", "wg-monitor-full-backup-*.tgz.enc"))
	if err != nil || len(files) != 1 {
		t.Fatalf("backup files=%v err=%v", files, err)
	}
}

func TestRedactTelegramBotTokenFromTransportError(t *testing.T) {
	token := "123456:ABC-very-secret"
	errText := `Post "https://api.telegram.org/bot123456:ABC-very-secret/sendDocument": dial tcp: timeout`

	got := redactTelegramBotToken(errText, token)

	if strings.Contains(got, token) {
		t.Fatalf("redacted error still contains bot token: %q", got)
	}
	if !strings.Contains(got, "bot<redacted>/sendDocument") {
		t.Fatalf("redacted error should preserve endpoint shape, got %q", got)
	}
}

func initBackupTestDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE users(
		nickname TEXT,
		kind TEXT,
		last_seen_at TEXT,
		last_deployed_version TEXT
	);
	INSERT INTO users(nickname, kind, last_seen_at, last_deployed_version)
	VALUES ('testkeen', 'static', '2026-06-05T01:02:03Z', 'v0.13.0-rc76');`)
	if err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func slash(path string) string {
	return filepath.ToSlash(path)
}

func tarMembers(t *testing.T, gzBody []byte) map[string]bool {
	t.Helper()
	members := map[string]bool{}
	gr, err := gzip.NewReader(bytes.NewReader(gzBody))
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		members[h.Name] = true
	}
	return members
}

func tarMember(t *testing.T, gzBody []byte, name string) []byte {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(gzBody))
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			t.Fatalf("%s not found", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name != name {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
}
