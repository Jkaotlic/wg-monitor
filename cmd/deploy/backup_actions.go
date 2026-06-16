package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/anex/wg-monitor/internal/backup"
)

const backupPassphraseEnv = "WG_BACKUP_PASSPHRASE" // #nosec G101 -- environment variable name, not passphrase material.

type backupRemoteLayout struct {
	User            string
	Group           string
	BinaryPath      string
	ConfigPath      string
	PassphrasePath  string
	OperatorVault   string
	OutDir          string
	LayoutRoot      string
	ReadWritePath   string
	ProtectHomeMode string
	UnitDir         string
	UserSystemd     bool
	OmitUserGroup   bool
	OmitHardening   bool
}

func backupLayoutForState(state *State) backupRemoteLayout {
	user := "root"
	if state != nil {
		user = userOrDefault(state.Backend.User, "root")
	}
	if state != nil && strings.TrimSpace(state.Backend.SourceBind) != "" {
		base := backendDockerBase(user)
		home := "/home/" + user
		if user == "root" {
			home = "/root"
		}
		return backupRemoteLayout{
			User:            user,
			Group:           user,
			BinaryPath:      base + "/bin/wg-monitor-backend",
			ConfigPath:      base + "/config/backend.yaml",
			PassphrasePath:  base + "/secrets/backup-passphrase.txt",
			OperatorVault:   base + "/secrets/operator-secrets.tgz.enc",
			OutDir:          base + "/data/backups",
			LayoutRoot:      base,
			ReadWritePath:   base,
			ProtectHomeMode: "read-only",
			UnitDir:         home + "/.config/systemd/user",
			UserSystemd:     user != "root",
			OmitUserGroup:   user != "root",
			OmitHardening:   user != "root",
		}
	}
	return backupRemoteLayout{ // #nosec G101 -- layout contains filesystem paths for secret files, not secret values.
		User:            "wgmonitor",
		Group:           "wgmonitor",
		BinaryPath:      "/usr/local/bin/wg-monitor-backend",
		ConfigPath:      "/etc/wg-monitor/backend.yaml",
		PassphrasePath:  "/etc/wg-monitor/backup-passphrase.txt",
		OperatorVault:   "/var/lib/wg-monitor/operator-secrets.tgz.enc",
		OutDir:          "/var/lib/wg-monitor/backups",
		ReadWritePath:   "/var/lib/wg-monitor",
		ProtectHomeMode: "true",
		UnitDir:         "/etc/systemd/system",
	}
}

func actionBackupStatus(state *State, secrets *SecretStore) error {
	s, layout, err := connectBackupBackend(state, secrets)
	if err != nil {
		return err
	}
	defer s.Close()
	for _, probe := range []struct {
		label string
		cmd   string
	}{
		{"timer enabled", backupSystemctl(layout, "is-enabled wg-monitor-backup.timer 2>/dev/null || true")},
		{"timer active", backupSystemctl(layout, "is-active wg-monitor-backup.timer 2>/dev/null || true")},
		{"service last", backupSystemctl(layout, "show wg-monitor-backup.service -p Result -p ExecMainStatus --value 2>/dev/null || true")},
		{"passphrase", "test -s " + shellSingleQuote(layout.PassphrasePath) + " && echo present || echo missing"},
		{"operator vault", "test -s " + shellSingleQuote(layout.OperatorVault) + " && echo present || echo missing"},
		{"latest backup", "ls -1t " + shellSingleQuote(layout.OutDir) + "/wg-monitor-full-backup-*.tgz.enc 2>/dev/null | head -1 || true"},
	} {
		out, _, _, _ := s.Run(probe.cmd)
		PrintInfo(probe.label + ": " + strings.TrimSpace(out))
	}
	return nil
}

func actionBackupInstall(state *State, secrets *SecretStore) error {
	s, layout, err := connectBackupBackend(state, secrets)
	if err != nil {
		return err
	}
	defer s.Close()
	if _, _, err := ensureBackupPassphrase(secrets, false); err != nil {
		return err
	}
	if err := installBackupOnBackend(state, secrets, s, layout); err != nil {
		return err
	}
	if err := pushBackupSecretsToBackend(state, secrets, s, layout, ""); err != nil {
		PrintWarn("operator secrets vault not uploaded: " + err.Error())
	}
	return nil
}

func actionBackupRun(state *State, secrets *SecretStore) error {
	s, _, err := connectBackupBackend(state, secrets)
	if err != nil {
		return err
	}
	defer s.Close()
	if _, err := s.MustRun(backupSystemctl(backupLayoutForState(state), "start wg-monitor-backup.service")); err != nil {
		return err
	}
	PrintOK("wg-monitor-backup.service started")
	return nil
}

func actionBackupPushSecrets(state *State, secrets *SecretStore, statePath string) error {
	s, layout, err := connectBackupBackend(state, secrets)
	if err != nil {
		return err
	}
	defer s.Close()
	return pushBackupSecretsToBackend(state, secrets, s, layout, statePath)
}

func actionBackupPassword(state *State, secrets *SecretStore, show bool) error {
	pass, generated, err := ensureBackupPassphrase(secrets, false)
	if err != nil {
		return err
	}
	if show {
		if generated {
			PrintWarn("Save this recovery password in your password manager:")
		}
		PrintInfo("WG_BACKUP_PASSPHRASE=" + pass)
	}
	s, layout, err := connectBackupBackend(state, secrets)
	if err != nil {
		return err
	}
	defer s.Close()
	return uploadBackupPassphrase(s, layout, pass)
}

func actionBackupRestore(_ *State, secrets *SecretStore, path string) error {
	if strings.TrimSpace(path) == "" {
		path = strings.TrimSpace(Ask("Path to encrypted wg-monitor backup", ""))
	}
	if path == "" {
		return fmt.Errorf("backup archive path is required")
	}
	pass, _ := secrets.Get(backupPassphraseEnv, "Backup recovery password", nil)
	return ImportEncryptedFullBackup(path, pass, true)
}

func connectBackupBackend(state *State, secrets *SecretStore) (*SSH, backupRemoteLayout, error) {
	if state == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return nil, backupRemoteLayout{}, fmt.Errorf("backend is not configured")
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return nil, backupRemoteLayout{}, err
	}
	s, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return nil, backupRemoteLayout{}, err
	}
	return s, backupLayoutForState(state), nil
}

func installBackupOnBackend(state *State, secrets *SecretStore, s *SSH, layout backupRemoteLayout) error {
	pass := secrets.GetNonInteractive(backupPassphraseEnv)
	if pass == "" {
		return fmt.Errorf("%s missing", backupPassphraseEnv)
	}
	if err := uploadBackupPassphrase(s, layout, pass); err != nil {
		return err
	}
	for _, dir := range []string{path.Dir(layout.PassphrasePath), path.Dir(layout.OperatorVault), layout.OutDir, layout.UnitDir} {
		if err := stepEnsureDir(s, shellSingleQuote(dir), ""); err != nil {
			return err
		}
	}
	service, err := RenderBackupService(BackupServiceParams{
		User:            layout.User,
		Group:           layout.Group,
		BinaryPath:      layout.BinaryPath,
		ConfigPath:      layout.ConfigPath,
		PassphrasePath:  layout.PassphrasePath,
		OperatorVault:   layout.OperatorVault,
		OutDir:          layout.OutDir,
		LayoutRoot:      layout.LayoutRoot,
		ReadWritePath:   layout.ReadWritePath,
		SendTelegram:    true,
		ProtectHomeMode: layout.ProtectHomeMode,
		OmitUserGroup:   layout.OmitUserGroup,
		OmitHardening:   layout.OmitHardening,
	})
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, path.Join(layout.UnitDir, "wg-monitor-backup.service"), service, "644"); err != nil {
		return err
	}
	timer, err := ReadStaticTemplate("wg-monitor-backup.timer")
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, path.Join(layout.UnitDir, "wg-monitor-backup.timer"), timer, "644"); err != nil {
		return err
	}
	if layout.UserSystemd {
		_, _ = s.MustRun("loginctl enable-linger " + shellSingleQuote(layout.User) + " 2>/dev/null || true")
	}
	if _, err := s.MustRun(backupSystemctl(layout, "daemon-reload") + " && " + backupSystemctl(layout, "enable --now wg-monitor-backup.timer")); err != nil {
		return err
	}
	PrintOK("encrypted nightly backup enabled")
	_ = state
	return nil
}

func backupSystemctl(layout backupRemoteLayout, args string) string {
	args = strings.TrimSpace(args)
	if layout.UserSystemd {
		return "uid=$(id -u); export XDG_RUNTIME_DIR=/run/user/$uid; systemctl --user " + args
	}
	return "systemctl " + args
}

func uploadBackupPassphrase(s *SSH, layout backupRemoteLayout, pass string) error {
	if err := stepEnsureDir(s, shellSingleQuote(path.Dir(layout.PassphrasePath)), ""); err != nil {
		return err
	}
	if err := stepUploadFile(s, layout.PassphrasePath, []byte(strings.TrimSpace(pass)+"\n"), "640"); err != nil {
		return err
	}
	_, _ = s.MustRun("chown " + shellSingleQuote(layout.User+":"+layout.Group) + " " + shellSingleQuote(layout.PassphrasePath))
	return nil
}

func pushBackupSecretsToBackend(_ *State, secrets *SecretStore, s *SSH, layout backupRemoteLayout, statePath string) error {
	pass, _, err := ensureBackupPassphrase(secrets, false)
	if err != nil {
		return err
	}
	if statePath == "" {
		statePath = DefaultStatePath()
	}
	tmpDir, err := os.MkdirTemp("", "wg-monitor-operator-secrets.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	plain := filepath.Join(tmpDir, "operator-secrets.tgz")
	if err := ExportSecrets(plain, statePath); err != nil {
		return err
	}
	plainBody, err := os.ReadFile(plain)
	if err != nil {
		return err
	}
	encrypted, err := backup.Encrypt(plainBody, []byte(pass), backup.DefaultParams())
	if err != nil {
		return err
	}
	if err := stepEnsureDir(s, shellSingleQuote(path.Dir(layout.OperatorVault)), ""); err != nil {
		return err
	}
	if err := stepUploadFile(s, layout.OperatorVault, encrypted, "600"); err != nil {
		return err
	}
	_, _ = s.MustRun("chown " + shellSingleQuote(layout.User+":"+layout.Group) + " " + shellSingleQuote(layout.OperatorVault))
	PrintOK("operator secrets vault uploaded")
	return nil
}

func ensureBackupPassphrase(secrets *SecretStore, rotate bool) (string, bool, error) {
	if secrets == nil {
		return "", false, fmt.Errorf("secret store is required")
	}
	if !rotate {
		if pass := strings.TrimSpace(secrets.GetNonInteractive(backupPassphraseEnv)); pass != "" {
			return pass, false, nil
		}
	}
	pass, err := generateBackupPassphrase()
	if err != nil {
		return "", false, err
	}
	if err := secrets.Set(backupPassphraseEnv, pass); err != nil && !strings.Contains(err.Error(), "disabled") {
		return "", false, err
	}
	PrintWarn("Generated new backup recovery password. Save it in your password manager:")
	PrintInfo(backupPassphraseEnv + "=" + pass)
	return pass, true, nil
}

func generateBackupPassphrase() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
