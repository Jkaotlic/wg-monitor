package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

type SecretSource int

const (
	SourceMissing SecretSource = iota
	SourceEnv
	SourceDisk
	SourceMemoryFile
	SourcePrompt
)

func (s SecretSource) String() string {
	switch s {
	case SourceEnv:
		return "env"
	case SourceDisk:
		return "disk cache"
	case SourceMemoryFile:
		return "memory file"
	case SourcePrompt:
		return "prompt"
	}
	return "missing"
}

type MemoryFileLookup struct {
	Path    string
	Pattern string // regexp with one capture group
}

// secretsCachePath returns the disk location of the wizard's auto-saved
// secret cache (KEY=VALUE per line, lock-tightened to 0600 on POSIX).
// Honoured: WG_SECRETS_FILE=<path> override; WG_NO_SECRET_CACHE=1 disables
// auto-load and auto-save (returns "" so callers skip disk operations).
func secretsCachePath() string {
	if os.Getenv("WG_NO_SECRET_CACHE") != "" {
		return ""
	}
	if p := os.Getenv("WG_SECRETS_FILE"); p != "" {
		return p
	}
	return filepath.Join(defaultCacheDir(), "secrets.env")
}

type SecretStore struct {
	prompted map[string]bool
	disk     map[string]string // loaded once on construction; written through on Set/prompt
	saved    map[string]bool   // env var names auto-persisted in this session
}

func NewSecretStore() *SecretStore {
	s := &SecretStore{
		prompted: map[string]bool{},
		disk:     map[string]string{},
		saved:    map[string]bool{},
	}
	if path := secretsCachePath(); path != "" {
		s.disk = loadSecretsFile(path)
	}
	return s
}

// Get tries: env var → disk cache → memory file (if provided) → prompt.
// Returns (secret, source). Empty string and SourceMissing if even prompt
// failed. Successful prompts auto-persist to the disk cache (unless
// WG_NO_SECRET_CACHE=1) so the next session finds the value.
func (s *SecretStore) Get(envVar, label string, mem *MemoryFileLookup) (string, SecretSource) {
	if v := os.Getenv(envVar); v != "" {
		return v, SourceEnv
	}
	if v, ok := s.disk[envVar]; ok && v != "" {
		return v, SourceDisk
	}
	if mem != nil {
		if v, ok := lookupMemoryFile(mem.Path, mem.Pattern); ok {
			return v, SourceMemoryFile
		}
	}
	v := AskSecret(label)
	if v == "" {
		return "", SourceMissing
	}
	s.recordPrompted(envVar)
	if err := s.Set(envVar, v); err == nil {
		s.saved[envVar] = true
	}
	return v, SourcePrompt
}

// GetNonInteractive returns the secret looked up via env → disk only,
// never prompting. Empty string when nothing is found. Use when the value
// must already exist (e.g. agent enrollment token regenerated weeks ago)
// — prompting would waste the operator's time on a value they cannot
// recall by hand.
func (s *SecretStore) GetNonInteractive(envVar string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return s.disk[envVar]
}

func (s *SecretStore) SourceNonInteractive(envVar string) SecretSource {
	if v := os.Getenv(envVar); v != "" {
		return SourceEnv
	}
	if v := s.disk[envVar]; v != "" {
		return SourceDisk
	}
	return SourceMissing
}

// Set persists a secret to the on-disk cache and the in-memory map. Used
// for values the wizard generates itself (e.g. agent enrollment tokens) so
// future sessions find them without re-prompting. When the cache is disabled
// (WG_NO_SECRET_CACHE=1) the value lives only in s.disk for the current
// process — caller MUST treat it as ephemeral. We surface this with an
// explicit ErrCacheDisabled so callers can decide whether to abort, warn
// the operator, or fall back to env.
var ErrCacheDisabled = errors.New("secret cache disabled (WG_NO_SECRET_CACHE=1) — value is process-only, save manually before exit")

func (s *SecretStore) Set(envVar, value string) error {
	s.disk[envVar] = value
	path := secretsCachePath()
	if path == "" {
		return ErrCacheDisabled
	}
	return writeSecretsFile(path, s.disk)
}

func (s *SecretStore) recordPrompted(envVar string) {
	s.prompted[envVar] = true
}

// PromptedSecrets returns the list of env-var names that were prompted in this session.
// Used to show a warning at the end advising the user to save them.
func (s *SecretStore) PromptedSecrets() []string {
	out := make([]string, 0, len(s.prompted))
	for k := range s.prompted {
		out = append(out, k)
	}
	return out
}

// SavedThisSession returns the env var names auto-saved to the disk cache
// during the current session.
func (s *SecretStore) SavedThisSession() []string {
	out := make([]string, 0, len(s.saved))
	for k := range s.saved {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lookupMemoryFile(path, pattern string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", false
	}
	m := re.FindStringSubmatch(string(data))
	if len(m) < 2 {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// loadSecretsFile reads KEY=VALUE pairs from path. Missing or unreadable
// file → empty map (best-effort; the wizard re-prompts on cache miss).
func loadSecretsFile(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// writeSecretsFile rewrites the secrets cache atomically with 0600 perms.
// All keys are emitted sorted so the file is deterministic across runs.
//
// First-time creation on Windows triggers a one-shot ACL warning: the 0o600
// mode is silently ignored by the Windows filesystem layer (NTFS uses ACLs,
// not POSIX bits) — by default the file inherits ACLs from %LOCALAPPDATA%,
// which means BUILTIN\Administrators on the local machine can read it. This
// is fine for a single-admin laptop, but worth surfacing.
func writeSecretsFile(path string, data map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	firstCreate := !fileExistsAt(path)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	w := bufio.NewWriter(f)
	for _, k := range keys {
		if _, err := fmt.Fprintf(w, "%s=%s\n", k, data[k]); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if firstCreate && runtime.GOOS == "windows" {
		PrintWarn(fmt.Sprintf(
			"%s создан. Mode 0600 на Windows — no-op (NTFS ACL, не POSIX). "+
				"По умолчанию файл наследует ACL от %%LOCALAPPDATA%% — читаем тобой и "+
				"BUILTIN\\Administrators этой машины. Для multi-user PC рассмотри BitLocker "+
				"или WG_NO_SECRET_CACHE=1 + хранение секретов в внешнем менеджере паролей.",
			path))
	}
	return nil
}

// fileExistsAt — true когда path существует и доступен для stat. False при
// любой ошибке. Используется для one-shot first-create detection.
func fileExistsAt(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// PrintSecretsSaveAdvice tells the user what got persisted this session and
// where the cache lives. Quiet when nothing was prompted/saved.
func PrintSecretsSaveAdvice(store *SecretStore) {
	saved := store.SavedThisSession()
	if len(saved) > 0 {
		path := secretsCachePath()
		PrintOK(fmt.Sprintf("секреты сохранены в %s — на следующий запуск wizard их подхватит", path))
	}
	prompted := store.PromptedSecrets()
	stillMissing := stringsMinus(prompted, saved)
	if len(stillMissing) == 0 {
		return
	}
	PrintWarn("В этой сессии ты ввёл секреты, но disk-кэш отключён или недоступен: " + strings.Join(stillMissing, ", "))
	fmt.Println("  Чтобы не вводить заново, экспортируй их через env vars или включи WG_NO_SECRET_CACHE=0.")
}

func stringsMinus(a, b []string) []string {
	skip := map[string]bool{}
	for _, x := range b {
		skip[x] = true
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		if !skip[x] {
			out = append(out, x)
		}
	}
	return out
}

type secretStatusRow struct {
	Name     string
	Label    string
	Required bool
}

func actionSecretsStatus(state *State, secrets *SecretStore) error {
	fmt.Println(Colorize("=== Secrets cache ===", ColorBold))
	if path := secretsCachePath(); path == "" {
		PrintWarn("disk cache disabled by WG_NO_SECRET_CACHE=1")
	} else if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			PrintWarn("secrets.env missing: " + path)
		} else {
			PrintWarn("secrets.env stat failed: " + err.Error())
		}
	} else {
		PrintOK("secrets.env: " + path)
	}

	rows := secretStatusRows(state)
	if len(rows) == 0 {
		PrintInfo("no routers/backend configured yet")
		return nil
	}

	fmt.Println()
	fmt.Println(Colorize("=== Required values ===", ColorBold))
	missingRequired := 0
	missingOptional := 0
	for _, row := range rows {
		src := secrets.SourceNonInteractive(row.Name)
		status := src.String()
		msg := fmt.Sprintf("%-32s %-12s %s", row.Name, status, row.Label)
		if src == SourceMissing {
			if row.Required {
				missingRequired++
				PrintFail(msg)
			} else {
				missingOptional++
				PrintWarn(msg)
			}
			continue
		}
		PrintOK(msg)
	}

	fmt.Println()
	switch {
	case missingRequired > 0:
		PrintFail(fmt.Sprintf("secrets status: %d required missing, %d optional missing", missingRequired, missingOptional))
	case missingOptional > 0:
		PrintWarn(fmt.Sprintf("secrets status: required OK, %d optional missing", missingOptional))
	default:
		PrintOK("secrets status: all known values are present")
	}
	return nil
}

func secretStatusRows(state *State) []secretStatusRow {
	var rows []secretStatusRow
	seen := map[string]bool{}
	add := func(name, label string, required bool) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		rows = append(rows, secretStatusRow{Name: name, Label: label, Required: required})
	}

	if state.Backend.Host != "" {
		if normalizeBackendSSHAuth(state.Backend.SSHAuth) == backendSSHAuthKey {
			add("WG_VPS_KEY_PASSPHRASE", "VPS SSH key passphrase", false)
		} else {
			add("WG_VPS_PASS", "VPS SSH password", true)
		}
		add("WG_BOT_TOKEN", "Telegram bot token", false)
	}
	if state.Backend.Domain != "" || len(state.Agents) > 0 {
		add("WIZARD_TOKEN", "wizard API token", true)
	}
	if len(state.Agents) > 0 {
		add("WG_KEENETIC_PASS", "fallback router SSH password", false)
	}
	for _, ag := range state.Agents {
		suffix := strings.ToUpper(ag.Nickname)
		add("WG_AGENT_TOKEN_"+suffix, "agent token for "+ag.Nickname, true)
		if strings.EqualFold(ag.DeployMode, "awgm") || ag.AWGMURL != "" {
			add("WG_AWGM_API_KEY_"+suffix, "AWG Manager API key for "+ag.Nickname, true)
			add("WG_AWGM_LOGIN_"+suffix, "AWG Manager login fallback for "+ag.Nickname, false)
			add("WG_AWGM_PASS_"+suffix, "AWG Manager password fallback for "+ag.Nickname, false)
		}
		add("WG_KEENETIC_PASS_"+suffix, "router SSH password for "+ag.Nickname, false)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Required != rows[j].Required {
			return rows[i].Required
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}
