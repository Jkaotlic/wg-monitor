package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
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
	// Extras -- хранилища и ключ оживления из архива (v0.53+): что и куда
	// положить на сервере. В старых архивах их нет.
	Extras []RestoreExtraFile
	// Warnings -- что из архива восстановить не получится и почему.
	Warnings []string
}

// RestoreExtraFile -- файл из архива, который восстановление кладёт на
// сервер рядом с базой: JSON-хранилище или revive.key.
type RestoreExtraFile struct {
	Name       string // имя в архиве
	LocalPath  string // куда извлечён локально
	RemotePath string // куда лечь на сервере (режим 0600)
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
		AdminUserID  int64  `yaml:"admin_user_id"`
	} `yaml:"telegram"`
	Wizard struct {
		TokenFile         string `yaml:"token_file"`
		BackendUpdateFile string `yaml:"backend_update_file"`
	} `yaml:"wizard"`
	Amnezia struct {
		SecretsPath string `yaml:"secrets_path"`
	} `yaml:"amnezia_premium"`
	SelfHosted struct {
		StorePath string `yaml:"store_path"`
	} `yaml:"amnezia_selfhosted"`
	HideMy struct {
		SecretsPath string `yaml:"secrets_path"`
	} `yaml:"hidemyname"`
	Revive struct {
		KeyFile string `yaml:"key_file"`
	} `yaml:"revive"`
}

// Имена файлов архива, которые восстановление кладёт на сервер помимо базы
// и конфига. Список хранилищ сверяется с бэкендом тестом
// TestRestoreStoreNamesMatchBackend.
const (
	restoreAmneziaStore    = "amnezia-premium.json" // #nosec G101 -- имя файла хранилища, не секрет
	restoreSelfHostedStore = "amnezia-selfhosted.json"
	restoreAwg3Store       = "awg3-panels.json"
	restoreHideMyStore     = "hidemyname.json"
	restoreReviveKey       = "revive.key"

	maxRestoreExtraBytes = 16 << 20
)

var restoreStoreNames = []string{restoreAmneziaStore, restoreSelfHostedStore, restoreAwg3Store, restoreHideMyStore}

// storeDestination -- где бэкенд с этим конфигом ищет хранилище name: путь
// из конфига, а не заданный -- рядом с базой. Панели awg3 лежат рядом с
// файлом своих серверов. Та же логика, что backend.ApplyStoreDefaults.
func (c *restoreBackendConfig) storeDestination(name string) string {
	dbDir := path.Dir(strings.TrimSpace(c.DBPath))
	explicit := func(configured, def string) string {
		if p := strings.TrimSpace(configured); p != "" {
			return p
		}
		return path.Join(dbDir, def)
	}
	switch name {
	case restoreAmneziaStore:
		return explicit(c.Amnezia.SecretsPath, restoreAmneziaStore)
	case restoreSelfHostedStore:
		return explicit(c.SelfHosted.StorePath, restoreSelfHostedStore)
	case restoreAwg3Store:
		return path.Join(path.Dir(explicit(c.SelfHosted.StorePath, restoreSelfHostedStore)), restoreAwg3Store)
	case restoreHideMyStore:
		return explicit(c.HideMy.SecretsPath, restoreHideMyStore)
	}
	return ""
}

var restoreDestinationRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// validateRestoreDestination: путь из backend.yaml архива уходит в скрипт,
// который на сервере исполняет root. Поэтому только обычные имена внутри
// каталогов wg-monitor и не поверх файлов, которые восстановление кладёт само.
func validateRestoreDestination(name, dest string) error {
	bad := func(why string) error {
		return fmt.Errorf("unsupported restore destination for %s: %q (%s)", name, dest, why)
	}
	if !restoreDestinationRe.MatchString(dest) || path.Clean(dest) != dest {
		return bad("need a clean absolute path of letters, digits, dot, dash, underscore")
	}
	if !strings.HasPrefix(dest, "/var/lib/wg-monitor/") && !strings.HasPrefix(dest, "/etc/wg-monitor/") {
		return bad("must be under /var/lib/wg-monitor or /etc/wg-monitor")
	}
	for _, reserved := range []string{
		restoreRemoteDBPath, restoreRemoteBotTokenPath, restoreRemoteWizardTokenPath, restoreRemoteBackendUpdatePath,
		"/etc/wg-monitor/backend.yaml", "/etc/wg-monitor/backup-passphrase.txt",
	} {
		if dest == reserved {
			return bad("path is taken by another wg-monitor file")
		}
	}
	return nil
}

// resolveRestoreExtras сопоставляет извлечённым хранилищам и ключу их места
// на сервере по backend.yaml из того же архива.
func resolveRestoreExtras(backendYAMLPath string, extracted map[string]string) (extras []RestoreExtraFile, warnings []string, err error) {
	if len(extracted) == 0 {
		return nil, nil, nil
	}
	cfg, err := readRestoreBackendYAML(backendYAMLPath)
	if err != nil {
		return nil, nil, err
	}
	seenDest := map[string]string{}
	for _, name := range append(append([]string{}, restoreStoreNames...), restoreReviveKey) {
		local, ok := extracted[name]
		if !ok {
			continue
		}
		dest := cfg.storeDestination(name)
		if name == restoreReviveKey {
			dest = strings.TrimSpace(cfg.Revive.KeyFile)
			if dest == "" {
				warnings = append(warnings, "в архиве есть revive.key, но в backend.yaml не задан revive.key_file -- ключ на сервер не кладётся")
				continue
			}
		}
		if err := validateRestoreDestination(name, dest); err != nil {
			return nil, nil, err
		}
		if other, taken := seenDest[dest]; taken {
			return nil, nil, fmt.Errorf("unsupported restore destination for %s: %q (same path as %s)", name, dest, other)
		}
		seenDest[dest] = name
		extras = append(extras, RestoreExtraFile{Name: name, LocalPath: local, RemotePath: dest})
	}
	return extras, warnings, nil
}

// isEncryptedBackupFile -- архив зашифрован (формат v1 или v2)?
func isEncryptedBackupFile(archivePath string) (bool, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return false, fmt.Errorf("open backup archive: %w", err)
	}
	defer f.Close()
	head := make([]byte, 16)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, fmt.Errorf("read backup archive: %w", err)
	}
	return backup.IsEncrypted(head[:n]), nil
}

const (
	restoreRemoteDBPath            = "/var/lib/wg-monitor/state.db"
	restoreRemoteBotTokenPath      = "/etc/wg-monitor/bot-token.txt"    // #nosec G101 -- filesystem path for restored token file, not token material.
	restoreRemoteWizardTokenPath   = "/etc/wg-monitor/wizard-token.txt" // #nosec G101 -- filesystem path for restored token file, not token material.
	restoreRemoteBackendUpdatePath = "/var/lib/wg-monitor/backend-update.json"
)

// InspectRestoreBackup читает нешифрованный архив .tgz (старый формат).
func InspectRestoreBackup(archivePath string) (*RestoreBackup, func(), error) {
	return inspectRestoreBackup(archivePath, "", validateRestoreBackendYAML, true)
}

// InspectRestoreBackupWithPassphrase читает архив любого вида: нешифрованный
// .tgz, шифрованный v1 и потоковый v2 (малый и полный архивы v0.53+).
// Шифрованный расшифровывается потоком прямо в распаковку -- открытого
// архива целиком на диске не появляется.
func InspectRestoreBackupWithPassphrase(archivePath, passphrase string) (*RestoreBackup, func(), error) {
	return inspectRestoreBackup(archivePath, passphrase, validateRestoreBackendYAML, true)
}

func InspectRestoreBackupForImport(archivePath string) (*RestoreBackup, func(), error) {
	return inspectRestoreBackup(archivePath, "", validateRestoreBackendYAMLForImport, false)
}

// inspectRestoreBackup: withExtras=false -- хранилища и ключ не извлекаются
// (импорт секретов оператора: на сервер ничего не кладётся).
func inspectRestoreBackup(archivePath, passphrase string, validateBackendYAML func(string) error, withExtras bool) (*RestoreBackup, func(), error) {
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
	br := bufio.NewReaderSize(in, 64<<10)
	var (
		src io.Reader = br
		dec io.Reader // не nil -- архив шифрованный, поток надо дочитать до конца
	)
	if head, _ := br.Peek(16); backup.IsEncrypted(head) {
		if strings.TrimSpace(passphrase) == "" {
			cleanup()
			return nil, nil, fmt.Errorf("backup archive is encrypted: the backup recovery password is required")
		}
		dec, err = backup.NewDecryptReader(br, []byte(strings.TrimSpace(passphrase)))
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		src = dec
	}
	gz, err := gzip.NewReader(src)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("gzip backup archive: %w", err)
	}
	defer gz.Close()
	extraNames := append(append([]string{}, restoreStoreNames...), restoreReviveKey)
	extracted := map[string]string{}

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
			if !slices.Contains(extraNames, base) {
				continue // Ignore future archive members.
			}
			if name != base {
				cleanup()
				return nil, nil, fmt.Errorf("backup archive contains unexpected restore member path %q", hdr.Name)
			}
			if _, dup := extracted[name]; dup {
				cleanup()
				return nil, nil, fmt.Errorf("backup archive contains duplicate restore member %q", hdr.Name)
			}
			if !withExtras {
				extracted[name] = ""
				continue
			}
			body, err := readArchiveMemberLimited(tr, name, maxRestoreExtraBytes)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			dst := filepath.Join(tmpDir, name)
			if err := os.WriteFile(dst, body, 0o600); err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("create extracted %s: %w", name, err)
			}
			extracted[name] = dst
		}
	}
	if dec != nil {
		// tar останавливается на своём конце; дочитываем поток, иначе
		// обрезанный или дописанный шифрованный архив сойдёт за целый.
		if _, err := io.Copy(io.Discard, gz); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("gzip backup archive: %w", err)
		}
		if _, err := io.Copy(io.Discard, dec); err != nil {
			cleanup()
			return nil, nil, err
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

	var (
		extras   []RestoreExtraFile
		warnings []string
	)
	if withExtras {
		extras, warnings, err = resolveRestoreExtras(seen["backend.yaml"], extracted)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
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
		Extras:          extras,
		Warnings:        warnings,
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
	// Кто админ -- здесь не проверяем: импорт принимает и очень старые
	// архивы, где в backend.yaml была только группа. Строгая проверка живёт
	// в validateRestoreBackendYAML, по которому идёт восстановление на VPS.
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
	var stores []string
	reviveKey := "no"
	for _, e := range b.Extras {
		if e.Name == restoreReviveKey {
			reviveKey = "yes"
			continue
		}
		stores = append(stores, e.Name)
	}
	out := fmt.Sprintf(
		"backup: %s\ncreated: %s\nhost: %s\nbackend: %s\nstate.db: %d bytes\nagents: %d\nstores: %s\nrevive.key: %s",
		b.ArchivePath,
		emptyDash(b.Manifest["created_utc"]),
		emptyDash(b.Manifest["host"]),
		emptyDash(b.Manifest["backend_version"]),
		size,
		len(b.Agents),
		emptyDash(strings.Join(stores, ", ")),
		reviveKey,
	)
	if b.Manifest["kind"] == "small" {
		out += "\nkind: small (no event history: after restore the screens stay empty until agents report, up to a minute)"
	}
	return out
}

func actionRestoreBackup(state *State, secrets *SecretStore, dl *Downloader, opts RestoreBackupOptions) error {
	path := strings.TrimSpace(opts.ArchivePath)
	if path == "" {
		path = strings.TrimSpace(Ask("Path to wg-monitor backup (.tgz or .tgz.enc)", ""))
	}
	if path == "" {
		return fmt.Errorf("backup archive path is required")
	}

	passphrase := ""
	encrypted, err := isEncryptedBackupFile(path)
	if err != nil {
		return err
	}
	if encrypted {
		passphrase, _ = secrets.Get(backupPassphraseEnv, "Backup recovery password", nil)
		if strings.TrimSpace(passphrase) == "" {
			return fmt.Errorf("backup archive is encrypted: the backup recovery password is required")
		}
	}
	backup, cleanup, err := InspectRestoreBackupWithPassphrase(path, passphrase)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Println(RenderRestoreBackupPreview(backup))
	for _, w := range backup.Warnings {
		PrintWarn(w)
	}

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
	if err := applyRestoreBackupOnRemote(s, backup.Extras); err != nil {
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

	adminID := parseAdminUserIDFromYAMLFile(backup.BackendYAMLPath)
	if state.Telegram.AdminUserID == 0 {
		state.Telegram.AdminUserID = adminID
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
	if err := applyRestoreBackupOnRemote(s, backup.Extras); err != nil {
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

const restoreRemoteStaging = "/tmp/wg-monitor-restore"

func uploadRestoreBackupFiles(s *SSH, backup *RestoreBackup) error {
	// Каталог 0700: в нём база, а с v0.53 -- хранилища и ключ оживления.
	if _, err := s.MustRun("rm -rf " + restoreRemoteStaging + " && mkdir -m 700 -p " + restoreRemoteStaging); err != nil {
		return err
	}
	db, err := os.ReadFile(backup.StateDBPath)
	if err != nil {
		return fmt.Errorf("read extracted state.db: %w", err)
	}
	if err := stepUploadFile(s, restoreRemoteStaging+"/state.db", db, "600"); err != nil {
		return err
	}
	yamlBytes, err := os.ReadFile(backup.BackendYAMLPath)
	if err != nil {
		return fmt.Errorf("read extracted backend.yaml: %w", err)
	}
	if err := stepUploadFile(s, restoreRemoteStaging+"/backend.yaml", yamlBytes, "600"); err != nil {
		return err
	}
	for _, e := range backup.Extras {
		body, err := os.ReadFile(e.LocalPath)
		if err != nil {
			return fmt.Errorf("read extracted %s: %w", e.Name, err)
		}
		if err := stepUploadFile(s, restoreRemoteStaging+"/"+e.Name, body, "600"); err != nil {
			return err
		}
	}
	return nil
}

func applyRestoreBackupOnRemote(s *SSH, extras []RestoreExtraFile) error {
	if err := ensureRemoteSQLite3(s); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	if _, err := s.MustRun(buildRestoreRemoteScript(stamp, extras...)); err != nil {
		return err
	}
	return nil
}

// buildRestoreRemoteScript собирает скрипт, который на сервере кладёт базу,
// конфиг, а также хранилища и ключ оживления (extras) на место: проверки до
// остановки бэкенда, копии прежних файлов, откат при сбое, уборка каталога
// с выгруженными секретами при любом исходе.
func buildRestoreRemoteScript(stamp string, extras ...RestoreExtraFile) string {
	var preflight, rollback, backupOld, install strings.Builder
	for _, e := range extras {
		src := shellSingleQuote(restoreRemoteStaging + "/" + e.Name)
		dst := shellSingleQuote(e.RemotePath)
		bak := shellSingleQuote(e.RemotePath + ".bak." + stamp)
		dir := shellSingleQuote(path.Dir(e.RemotePath))
		fmt.Fprintf(&preflight, "test -s %s\n", src)
		fmt.Fprintf(&rollback, "\tif [ -f %s ]; then\n\t\tcp -p %s %s || true\n\tfi\n", bak, bak, dst)
		fmt.Fprintf(&backupOld, "if [ -f %s ]; then cp -p %s %s; fi\n", dst, dst, bak)
		fmt.Fprintf(&install, "[ -d %s ] || install -d -m 700 -o wgmonitor -g wgmonitor %s\n", dir, dir)
		fmt.Fprintf(&install, "install -m 600 -o wgmonitor -g wgmonitor %s %s\n", src, dst)
	}
	return fmt.Sprintf(`set -eu
test "$(sqlite3 /tmp/wg-monitor-restore/state.db 'PRAGMA integrity_check;')" = "ok"
test -s /etc/wg-monitor/bot-token.txt
test -s /etc/wg-monitor/wizard-token.txt
%[2]srollback() {
	if [ -f /var/lib/wg-monitor/state.db.bak.%[1]s ]; then
		cp -p /var/lib/wg-monitor/state.db.bak.%[1]s /var/lib/wg-monitor/state.db || true
	fi
	if [ -f /etc/wg-monitor/backend.yaml.bak.%[1]s ]; then
		cp -p /etc/wg-monitor/backend.yaml.bak.%[1]s /etc/wg-monitor/backend.yaml || true
	fi
%[3]s	systemctl start wg-monitor-backend 2>/dev/null || true
}
systemctl stop wg-monitor-backend 2>/dev/null || true
trap 'rc=$?; if [ "$rc" != 0 ]; then rollback; fi; rm -rf /tmp/wg-monitor-restore; exit $rc' EXIT
if [ -f /var/lib/wg-monitor/state.db ]; then cp -p /var/lib/wg-monitor/state.db /var/lib/wg-monitor/state.db.bak.%[1]s; fi
if [ -f /etc/wg-monitor/backend.yaml ]; then cp -p /etc/wg-monitor/backend.yaml /etc/wg-monitor/backend.yaml.bak.%[1]s; fi
%[4]sinstall -m 600 -o wgmonitor -g wgmonitor /tmp/wg-monitor-restore/state.db /var/lib/wg-monitor/state.db
install -m 640 -o root -g wgmonitor /tmp/wg-monitor-restore/backend.yaml /etc/wg-monitor/backend.yaml
%[5]ssystemctl start wg-monitor-backend
trap - EXIT
rm -rf /tmp/wg-monitor-restore
`, stamp, preflight.String(), rollback.String(), backupOld.String(), install.String())
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

// parseAdminUserIDFromYAMLFile -- admin_user_id из backend.yaml архива.
// Группы больше нет (цикл 5): chat_id из архива никому не нужен.
func parseAdminUserIDFromYAMLFile(path string) (adminUserID int64) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if rest, ok := strings.CutPrefix(line, "admin_user_id:"); ok {
			adminUserID = parseInt64Or(strings.TrimSpace(rest), 0)
		}
	}
	return adminUserID
}
