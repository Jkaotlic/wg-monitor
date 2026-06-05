package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend"
	"github.com/anex/wg-monitor/internal/backup"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

type backupCommandOptions struct {
	ConfigPath     string
	PassphraseFile string
	OperatorVault  string
	OutDir         string
	LayoutRoot     string
	SendTelegram   bool
	LimitBytes     int64
	TestKDF        bool
}

func runBackupCommand(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := backupCommandOptions{}
	fs.StringVar(&opts.ConfigPath, "config", "/etc/wg-monitor/backend.yaml", "path to backend config yaml")
	fs.StringVar(&opts.PassphraseFile, "passphrase-file", "", "path to backup encryption passphrase")
	fs.StringVar(&opts.OperatorVault, "operator-vault", "", "optional encrypted operator secrets vault")
	fs.StringVar(&opts.OutDir, "out-dir", "/var/lib/wg-monitor/backups", "backup output directory")
	fs.StringVar(&opts.LayoutRoot, "layout-root", "", "host root for container-style /data and /secrets paths")
	fs.BoolVar(&opts.SendTelegram, "send-telegram", false, "send encrypted backup to Telegram")
	fs.Int64Var(&opts.LimitBytes, "limit-bytes", 47185920, "Telegram document size limit")
	fs.BoolVar(&opts.TestKDF, "test-kdf", false, "use lightweight KDF params for tests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runBackup(opts)
}

func runBackup(opts backupCommandOptions) error {
	if opts.PassphraseFile == "" {
		return fmt.Errorf("--passphrase-file is required")
	}
	if opts.OutDir == "" {
		return fmt.Errorf("--out-dir is required")
	}
	cfg, err := loadBackupConfig(opts.ConfigPath)
	if err != nil {
		return err
	}
	pass, err := readTrimmedFile(opts.PassphraseFile)
	if err != nil {
		return fmt.Errorf("read passphrase: %w", err)
	}
	if pass == "" {
		return fmt.Errorf("backup passphrase is empty")
	}
	dbPath := resolveLayoutPath(cfg.DBPath, opts.LayoutRoot)
	botTokenPath := resolveLayoutPath(cfg.Telegram.BotTokenFile, opts.LayoutRoot)
	wizardTokenPath := resolveLayoutPath(cfg.Wizard.TokenFile, opts.LayoutRoot)

	tmpDir, err := os.MkdirTemp(filepath.Dir(opts.OutDir), "wg-monitor-backup.")
	if err != nil {
		tmpDir, err = os.MkdirTemp("", "wg-monitor-backup.")
	}
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	dbCopy := filepath.Join(tmpDir, "state.db")
	if err := vacuumSQLite(dbPath, dbCopy); err != nil {
		return err
	}
	if err := copyFile(opts.ConfigPath, filepath.Join(tmpDir, "backend.yaml"), 0o600); err != nil {
		return err
	}
	if botTokenPath != "" {
		if err := copyFile(botTokenPath, filepath.Join(tmpDir, "bot-token.txt"), 0o600); err != nil {
			return err
		}
	}
	if wizardTokenPath != "" {
		if err := copyFile(wizardTokenPath, filepath.Join(tmpDir, "wizard-token.txt"), 0o600); err != nil {
			return err
		}
	}
	if opts.OperatorVault != "" {
		if _, err := os.Stat(opts.OperatorVault); err == nil {
			if err := copyFile(opts.OperatorVault, filepath.Join(tmpDir, "operator-secrets.tgz.enc"), 0o600); err != nil {
				return err
			}
		}
	}
	if err := writeAgentsCSV(dbCopy, filepath.Join(tmpDir, "agents.csv")); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	if err := writeManifest(filepath.Join(tmpDir, "manifest.txt"), stamp, cfg.DBPath, opts.ConfigPath); err != nil {
		return err
	}
	plainTGZ, err := makeTGZ(tmpDir, []string{
		"state.db",
		"backend.yaml",
		"bot-token.txt",
		"wizard-token.txt",
		"agents.csv",
		"manifest.txt",
		"operator-secrets.tgz.enc",
	})
	if err != nil {
		return err
	}
	params := backup.DefaultParams()
	if opts.TestKDF {
		params = backup.TestParams()
	}
	encrypted, err := backup.Encrypt(plainTGZ, []byte(pass), params)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return err
	}
	outPath := filepath.Join(opts.OutDir, "wg-monitor-full-backup-"+stamp+".tgz.enc")
	if err := os.WriteFile(outPath, encrypted, 0o600); err != nil {
		return err
	}
	if opts.SendTelegram {
		chatID := cfg.Telegram.AdminUserID
		if chatID == 0 {
			chatID = cfg.Telegram.ChatID
		}
		if chatID == 0 {
			return fmt.Errorf("telegram chat_id/admin_user_id is not configured")
		}
		if opts.LimitBytes > 0 && int64(len(encrypted)) > opts.LimitBytes {
			return fmt.Errorf("encrypted backup too large for Telegram: %d bytes", len(encrypted))
		}
		token, err := readTrimmedFile(botTokenPath)
		if err != nil {
			return fmt.Errorf("read bot token: %w", err)
		}
		if err := sendTelegramDocument(context.Background(), token, chatID, outPath, fmt.Sprintf("wg-monitor encrypted full backup %s (%d bytes)", stamp, len(encrypted))); err != nil {
			return err
		}
	}
	fmt.Println(outPath)
	return nil
}

func resolveLayoutPath(path, root string) string {
	if root == "" || path == "" {
		return path
	}
	switch {
	case path == "/data":
		return filepath.Join(root, "data")
	case strings.HasPrefix(path, "/data/"):
		return filepath.Join(root, "data", strings.TrimPrefix(path, "/data/"))
	case path == "/secrets":
		return filepath.Join(root, "secrets")
	case strings.HasPrefix(path, "/secrets/"):
		return filepath.Join(root, "secrets", strings.TrimPrefix(path, "/secrets/"))
	case path == "/config":
		return filepath.Join(root, "config")
	case strings.HasPrefix(path, "/config/"):
		return filepath.Join(root, "config", strings.TrimPrefix(path, "/config/"))
	default:
		return path
	}
}

func loadBackupConfig(path string) (*backend.Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg backend.Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("db_path is required")
	}
	if cfg.Telegram.BotTokenFile == "" {
		return nil, fmt.Errorf("telegram.bot_token_file is required")
	}
	if cfg.Telegram.ChatID == 0 && cfg.Telegram.AdminUserID == 0 {
		return nil, fmt.Errorf("telegram chat_id/admin_user_id is required")
	}
	return &cfg, nil
}

func vacuumSQLite(src, dst string) error {
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return err
	}
	defer db.Close()
	escaped := strings.ReplaceAll(dst, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
		return fmt.Errorf("vacuum sqlite: %w", err)
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func writeAgentsCSV(dbPath, dst string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT nickname, COALESCE(kind, ''), COALESCE(last_seen_at, ''), COALESCE(last_deployed_version, '') FROM users ORDER BY nickname`)
	if err != nil {
		return fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()
	var b strings.Builder
	b.WriteString("nickname,kind,last_seen_at,last_deployed_version\n")
	for rows.Next() {
		var nick, kind, seen, version string
		if err := rows.Scan(&nick, &kind, &seen, &version); err != nil {
			return err
		}
		b.WriteString(csvCell(nick) + "," + csvCell(kind) + "," + csvCell(seen) + "," + csvCell(version) + "\n")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(b.String()), 0o600)
}

func csvCell(s string) string {
	if !strings.ContainsAny(s, "\",\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func writeManifest(path, stamp, dbPath, configPath string) error {
	body := fmt.Sprintf("name=wg-monitor-full-backup\ncreated_utc=%s\nbackend_version=%s\ndb_path=%s\nconfig_path=%s\nhost=%s\nformat=encrypted-full-v1\n",
		stamp, Version, dbPath, configPath, hostname())
	return os.WriteFile(path, []byte(body), 0o600)
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func makeTGZ(dir string, names []string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			tw.Close()
			gz.Close()
			return nil, err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			tw.Close()
			gz.Close()
			return nil, err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
			tw.Close()
			gz.Close()
			return nil, err
		}
		if _, err := tw.Write(body); err != nil {
			tw.Close()
			gz.Close()
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func readTrimmedFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func sendTelegramDocument(ctx context.Context, token string, chatID int64, path, caption string) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if caption != "" {
		if err := mw.WriteField("caption", caption); err != nil {
			return err
		}
	}
	fw, err := mw.CreateFormFile("document", filepath.Base(path))
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendDocument", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("telegram sendDocument HTTP %d", resp.StatusCode)
	}
	return nil
}
