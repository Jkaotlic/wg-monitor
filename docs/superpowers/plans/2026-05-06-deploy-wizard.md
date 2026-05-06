# Deploy Wizard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Заменить 4 Python-скрипта (`deploy_keenetic*.py`, `deploy_vps_main.py`, `deploy_cli.py`) одним самодостаточным Go-бинарём `wg-monitor-deploy` с интерактивным wizard'ом, который скачивает агент/бэкенд бинари из GitHub Releases и идемпотентно деплоит их на VPS и Keenetic.

**Architecture:** Новый Go-пакет `cmd/deploy/` с разделением: `main.go` (CLI/меню) → `actions.go` (последовательности шагов) → `steps.go` (мелкие переиспользуемые шаги) → `ssh.go` / `state.go` / `templates.go` / `github.go` (инфраструктура). Шаблоны конфигов запекаются через `embed.FS`. Секреты — через env vars + fallback на prompt. CI на GitHub Actions matrix собирает 7 артефактов на каждый тег.

**Tech Stack:** Go 1.22+, `golang.org/x/crypto/ssh`, `github.com/BurntSushi/toml`, `embed.FS`, голый `net/http` для GitHub API, ANSI escape codes (без TUI-либ).

**Spec:** [docs/superpowers/specs/2026-05-06-deploy-wizard-design.md](../specs/2026-05-06-deploy-wizard-design.md)

---

## File Map

| Файл | Action | Responsibility |
|------|--------|----------------|
| `cmd/deploy/main.go` | Create | Точка входа, парсер CLI, dispatch на actions, главное меню |
| `cmd/deploy/ui.go` | Create | Print* helpers, Ask*, цвета, ANSI, isatty/NO_COLOR |
| `cmd/deploy/ui_test.go` | Create | Тесты рендеринга и парсинга ответов |
| `cmd/deploy/state.go` | Create | Структуры конфига, load/save `wizard.toml`, путь по платформе |
| `cmd/deploy/state_test.go` | Create | Round-trip TOML, schema_version валидация, путь |
| `cmd/deploy/secrets.go` | Create | EnvVar/MemoryFile/Prompt fallback chain, трекинг введённых секретов |
| `cmd/deploy/secrets_test.go` | Create | Приоритет источников, fallback |
| `cmd/deploy/ssh.go` | Create | Класс `SSH`: Connect, Run, RunSudo, UploadStdin, UploadSFTP, KnownHosts/TOFU |
| `cmd/deploy/ssh_test.go` | Create | TOFU known_hosts парсинг, Mock SSH через httptest-style fixtures где можно |
| `cmd/deploy/github.go` | Create | GetLatestRelease, DownloadAsset, sha256 verify, кэш |
| `cmd/deploy/github_test.go` | Create | Парсинг JSON фикстур, mock httptest сервер |
| `cmd/deploy/templates.go` | Create | embed.FS, RenderBackendYAML/RenderAgentYAML/RenderCaddyfile |
| `cmd/deploy/templates_test.go` | Create | Шаблон рендерится, содержит ожидаемые поля |
| `cmd/deploy/templates/S99wg-monitor` | Create | Скопировать из `deploy/agent/S99wg-monitor` |
| `cmd/deploy/templates/wg-monitor-backend.service` | Create | Скопировать из `deploy/backend/wg-monitor-backend.service` |
| `cmd/deploy/templates/Caddyfile.tmpl` | Create | На основе `deploy/backend/Caddyfile`, `{{.Domain}}` + `{{.Email}}` |
| `cmd/deploy/templates/backend.yaml.tmpl` | Create | На основе `deploy/backend/backend.yaml.example` + полные поля |
| `cmd/deploy/templates/agent.yaml.tmpl` | Create | На основе `deploy/agent/config.yaml.example` |
| `cmd/deploy/steps.go` | Create | Маленькие функции: stepCheckUser, stepEnsureDir, stepUploadBinary, stepInstallCaddy и т.д. |
| `cmd/deploy/steps_test.go` | Create | Тесты с моком SSH |
| `cmd/deploy/actions.go` | Create | actionUpdateBackend, actionUpdateAgent, actionInstallBackend, actionInstallAgent, actionAddRouter, actionStatus |
| `cmd/deploy/menu.go` | Create | Интерактивное меню, рендеринг шапки |
| `cmd/deploy/version.go` | Create | `Version` ldflags var + check-for-updates |
| `Makefile` | Modify | Добавить `build-deploy` target |
| `go.mod` / `go.sum` | Modify | Добавить `github.com/BurntSushi/toml`, поднять `golang.org/x/crypto` до прямой зависимости |
| `.github/workflows/release.yml` | Create | CI matrix на push тега `v*` |
| `.gitignore` | Modify | Добавить `wizard.toml`, `cmd/deploy/wizard.toml`, `dist/wg-monitor-deploy*` |
| `deploy/agent/deploy_keenetic.py` | **Delete** | Заменён wizard'ом |
| `deploy/agent/deploy_keenetic_binonly.py` | **Delete** | Заменён wizard'ом |
| `deploy/agent/requirements.txt` | **Delete** | Python больше не нужен |
| `deploy/backend/deploy_vps_main.py` | **Delete** | Заменён wizard'ом |
| `deploy/backend/deploy_cli.py` | **Delete** | Заменён wizard'ом |
| `deploy/agent/config.yaml.example` | **Delete** | Источник переехал в `cmd/deploy/templates/` |
| `deploy/agent/S99wg-monitor` | **Delete** | Источник переехал в `cmd/deploy/templates/` |
| `deploy/backend/backend.yaml.example` | **Delete** | Источник переехал в `cmd/deploy/templates/` |
| `deploy/backend/Caddyfile` | **Delete** | Источник переехал в `cmd/deploy/templates/Caddyfile.tmpl` |
| `deploy/backend/wg-monitor-backend.service` | **Delete** | Источник переехал в `cmd/deploy/templates/` |
| `DEPLOY.md` | Modify | Сократить до prereqs (VPS, бот, awg-manager) + одна команда `wg-monitor-deploy` |
| `README.md` | Modify | Заменить упоминания старых скриптов |

---

## Milestones

1. **Skeleton + version flag** — пустой `cmd/deploy/main.go` который билдится и `--version` работает
2. **UI primitives** — цвета, prompts
3. **State (wizard.toml)** — load/save конфига
4. **Secrets** — env vars + fallback chain
5. **Templates (embed)** — перенос файлов из `deploy/`, рендеринг
6. **GitHub Releases client** — latest release, download, sha256
7. **SSH wrapper** — Connect, Run, UploadStdin, UploadSFTP, TOFU
8. **Steps + Actions: update-backend** — простейший action, end-to-end
9. **Actions: update-agent** — с dropbear stdin pipe
10. **Actions: install-backend / install-agent / add-router** — большие первичные установки
11. **Action: status** — read-only сводка
12. **Main + menu** — точка входа, интерактивное меню
13. **CI workflow** — `.github/workflows/release.yml`
14. **Cleanup + docs** — удалить старое, переписать DEPLOY.md и README

---

## Task 1: Skeleton — `cmd/deploy/main.go` + `version.go` + Makefile

**Files:**
- Create: `cmd/deploy/main.go`
- Create: `cmd/deploy/version.go`
- Modify: `Makefile`
- Modify: `go.mod` (добавить `github.com/BurntSushi/toml`)

- [ ] **Step 1: Создать `cmd/deploy/version.go`**

```go
package main

// Version is set via -ldflags at build time.
// Default keeps "dev" so unbuilt go run shows a clear marker.
var Version = "dev"
```

- [ ] **Step 2: Создать `cmd/deploy/main.go`**

```go
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(Version)
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("wg-monitor-deploy", Version)
		fmt.Println("(меню пока не реализовано — Task 12)")
		return
	}

	fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
	os.Exit(2)
}
```

- [ ] **Step 3: Добавить `build-deploy` в Makefile**

В существующем `Makefile`, после блока `build-host`, добавить:

```makefile
build-deploy:
	mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -o $(BIN_DIR)/wg-monitor-deploy ./cmd/deploy
```

И обновить `.PHONY` строку, добавив `build-deploy`:

```makefile
.PHONY: all build-host build-cli build-deploy build-mipsel build-aarch64 pack test clean size
```

- [ ] **Step 4: Добавить toml зависимость**

```bash
go get github.com/BurntSushi/toml@latest
```

Это обновит `go.mod` и `go.sum`.

- [ ] **Step 5: Билд и smoke-test**

```bash
make build-deploy
./bin/wg-monitor-deploy --version
```
Expected output: `dev`

```bash
./bin/wg-monitor-deploy
```
Expected output:
```
wg-monitor-deploy dev
(меню пока не реализовано — Task 12)
```

- [ ] **Step 6: Commit**

```bash
git add cmd/deploy/main.go cmd/deploy/version.go Makefile go.mod go.sum
git commit -m "feat(deploy): skeleton wg-monitor-deploy command"
```

---

## Task 2: UI primitives — `ui.go`

**Files:**
- Create: `cmd/deploy/ui.go`
- Create: `cmd/deploy/ui_test.go`

- [ ] **Step 1: Failing test для NoColor detection**

`cmd/deploy/ui_test.go`:

```go
package main

import (
	"os"
	"strings"
	"testing"
)

func TestColorize_NoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := Colorize("hello", ColorGreen)
	if got != "hello" {
		t.Errorf("expected plain text when NO_COLOR set, got %q", got)
	}
}

func TestColorize_WithColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	UseColor = true // force-enable for test (bypass isatty)
	got := Colorize("ok", ColorGreen)
	if !strings.Contains(got, "\033[32m") || !strings.Contains(got, "\033[0m") {
		t.Errorf("expected ANSI green wrapping, got %q", got)
	}
}
```

- [ ] **Step 2: Run test — fails, undefined symbols**

```bash
go test ./cmd/deploy/ -run TestColorize -v
```
Expected: FAIL — `undefined: Colorize`

- [ ] **Step 3: Реализовать `cmd/deploy/ui.go`**

```go
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// ANSI color codes.
const (
	ColorReset  = "\033[0m"
	ColorBold   = "\033[1m"
	ColorDim    = "\033[2m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
)

// UseColor controls whether Colorize emits escape codes.
// Auto-detected at startup; can be overridden by --no-color flag.
var UseColor = isTerminal(os.Stdout) && os.Getenv("NO_COLOR") == ""

func isTerminal(f *os.File) bool {
	// Minimal isatty: check if stdout is a char device.
	// Avoids pulling in mattn/go-isatty for a single use.
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func Colorize(s, color string) string {
	if !UseColor || os.Getenv("NO_COLOR") != "" {
		return s
	}
	return color + s + ColorReset
}

// Print helpers.

func PrintStep(n, total int, name string) {
	header := fmt.Sprintf("[%d/%d] %s", n, total, name)
	fmt.Println(Colorize(header, ColorBold))
}

func PrintOK(msg string) {
	fmt.Printf("  %s %s\n", Colorize("✓", ColorGreen), msg)
}

func PrintFail(msg string) {
	fmt.Printf("  %s %s\n", Colorize("✗", ColorRed), msg)
}

func PrintWarn(msg string) {
	fmt.Printf("  %s %s\n", Colorize("⚠", ColorYellow), msg)
}

func PrintInfo(msg string) {
	fmt.Printf("  %s %s\n", Colorize("→", ColorCyan), msg)
}

func PrintSkip(msg string) {
	fmt.Printf("  %s %s %s\n", Colorize("✓", ColorGreen), msg,
		Colorize("→ скипаю", ColorDim))
}

// Ask prompts for free-form input. If user enters empty string and defaultVal
// is non-empty, returns defaultVal.
func Ask(prompt, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// AskSecret prompts without echoing input.
func AskSecret(prompt string) string {
	fmt.Printf("%s: ", prompt)
	b, err := readPasswordNoEcho(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return ""
	}
	return string(b)
}

// AskChoice presents [A]/[B]/[C]... options and returns the selected key (uppercase).
// Re-prompts until a valid key is entered.
func AskChoice(prompt string, options []ChoiceOption) string {
	for _, o := range options {
		fmt.Printf("  [%s] %s\n", o.Key, o.Label)
	}
	for {
		fmt.Print(prompt + " > ")
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(strings.ToUpper(line))
		for _, o := range options {
			if line == strings.ToUpper(o.Key) {
				return strings.ToUpper(o.Key)
			}
		}
		PrintFail("Не понял. Введи букву из списка.")
	}
}

type ChoiceOption struct {
	Key   string
	Label string
}

// Confirm asks a yes/no question. defaultYes makes Enter == yes.
func Confirm(prompt string, defaultYes bool) bool {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	fmt.Printf("%s %s: ", prompt, suffix)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes"
}
```

- [ ] **Step 4: Реализовать `readPasswordNoEcho` для Windows и Unix**

Создать два файла build-tag разделённых:

`cmd/deploy/ui_unix.go`:
```go
//go:build !windows
// +build !windows

package main

import "golang.org/x/term"

func readPasswordNoEcho(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}
```

`cmd/deploy/ui_windows.go`:
```go
//go:build windows
// +build windows

package main

import "golang.org/x/term"

func readPasswordNoEcho(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}
```

(Эти файлы технически идентичны — `term.ReadPassword` cross-platform. Build-tag разделение оставлено на случай платформенной правки в будущем. Если предпочитаешь — объединить в один файл.)

```bash
go get golang.org/x/term@latest
```

- [ ] **Step 5: Run tests — pass**

```bash
go test ./cmd/deploy/ -run TestColorize -v
```
Expected: PASS

- [ ] **Step 6: Smoke-тест через main**

Временно добавить в `main.go` после `flag.Parse()`:
```go
if len(args) > 0 && args[0] == "smoke-ui" {
    PrintStep(1, 3, "Smoke test")
    PrintOK("это OK")
    PrintFail("это FAIL")
    PrintWarn("это WARN")
    PrintInfo("это INFO")
    PrintSkip("это SKIP")
    return
}
```

```bash
make build-deploy && ./bin/wg-monitor-deploy smoke-ui
```
Глазами убедиться что цвета и символы рендерятся.

Удалить smoke-блок после проверки.

- [ ] **Step 7: Commit**

```bash
git add cmd/deploy/ui.go cmd/deploy/ui_unix.go cmd/deploy/ui_windows.go cmd/deploy/ui_test.go cmd/deploy/main.go go.mod go.sum
git commit -m "feat(deploy): UI primitives — colors, prompts, ask/confirm"
```

---

## Task 3: State — `state.go` (wizard.toml)

**Files:**
- Create: `cmd/deploy/state.go`
- Create: `cmd/deploy/state_test.go`

- [ ] **Step 1: Failing test для round-trip TOML**

`cmd/deploy/state_test.go`:
```go
package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wizard.toml")

	in := &State{
		SchemaVersion: 1,
		Backend: BackendState{
			Host:    "1.2.3.4",
			Port:    22,
			User:    "root",
			Domain:  "example.com",
		},
		Telegram: TelegramState{
			ChatID:      -1001234567890,
			AdminUserID: 123456789,
		},
		Agents: []AgentState{
			{Nickname: "test", Host: "192.168.1.1", Port: 222, User: "root", Arch: "arm64", ThreadID: 42, AwgIface: "awg0", ExpectedExitIP: "1.2.3.4"},
		},
	}

	if err := SaveState(path, in); err != nil {
		t.Fatal(err)
	}

	out, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(in, out) {
		t.Errorf("round-trip mismatch:\nin:  %+v\nout: %+v", in, out)
	}
}

func TestLoadState_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.toml")
	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if s == nil {
		t.Fatal("expected default state, got nil")
	}
	if s.SchemaVersion != 1 {
		t.Errorf("expected default SchemaVersion=1, got %d", s.SchemaVersion)
	}
}

func TestLoadState_FutureSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wizard.toml")
	if err := os.WriteFile(path, []byte("schema_version = 999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadState(path)
	if err == nil {
		t.Fatal("expected error for unsupported schema_version")
	}
}
```

- [ ] **Step 2: Run test — fails (undefined)**

```bash
go test ./cmd/deploy/ -run TestState -v
```
Expected: FAIL — undefined State, SaveState, LoadState

- [ ] **Step 3: Реализовать `cmd/deploy/state.go`**

```go
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

const CurrentSchemaVersion = 1

type State struct {
	SchemaVersion int            `toml:"schema_version"`
	Backend       BackendState   `toml:"backend"`
	Telegram      TelegramState  `toml:"telegram"`
	Agents        []AgentState   `toml:"agents"`
}

type BackendState struct {
	Host                string `toml:"host"`
	Port                int    `toml:"port"`
	User                string `toml:"user"`
	Domain              string `toml:"domain"`
	LastDeploy          string `toml:"last_deploy"`
	LastDeployedVersion string `toml:"last_deployed_version"`
}

type TelegramState struct {
	ChatID      int64 `toml:"chat_id"`
	AdminUserID int64 `toml:"admin_user_id"`
}

type AgentState struct {
	Nickname            string `toml:"nickname"`
	Host                string `toml:"host"`
	Port                int    `toml:"port"`
	User                string `toml:"user"`
	Arch                string `toml:"arch"`
	ThreadID            int    `toml:"thread_id"`
	AwgIface            string `toml:"awg_iface"`
	ExpectedExitIP      string `toml:"expected_exit_ip"`
	LastDeploy          string `toml:"last_deploy"`
	LastDeployedVersion string `toml:"last_deployed_version"`
}

// LoadState reads wizard.toml from path. Missing file → returns default state, no error.
// Invalid schema_version → returns error.
func LoadState(path string) (*State, error) {
	s := &State{SchemaVersion: CurrentSchemaVersion}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.SchemaVersion > CurrentSchemaVersion {
		return nil, fmt.Errorf("wizard.toml schema_version=%d not supported (max=%d). Update wg-monitor-deploy",
			s.SchemaVersion, CurrentSchemaVersion)
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = CurrentSchemaVersion
	}
	return s, nil
}

func SaveState(path string, s *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := toml.NewEncoder(f)
	if err := enc.Encode(s); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}

// DefaultStatePath returns the OS-appropriate config location.
// Priority used by callers:
//   1. --config flag (handled in main)
//   2. ./wizard.toml if exists in cwd (handled in main)
//   3. this default
func DefaultStatePath() string {
	if runtime.GOOS == "windows" {
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			appdata = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return filepath.Join(appdata, "wg-monitor-deploy", "wizard.toml")
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		cfg = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(cfg, "wg-monitor-deploy", "wizard.toml")
}

// FindAgent returns a pointer to the agent with the given nickname, or nil.
func (s *State) FindAgent(nickname string) *AgentState {
	for i := range s.Agents {
		if s.Agents[i].Nickname == nickname {
			return &s.Agents[i]
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests — pass**

```bash
go test ./cmd/deploy/ -run TestState -v
go test ./cmd/deploy/ -run TestLoadState -v
```
Expected: PASS, PASS, PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/deploy/state.go cmd/deploy/state_test.go
git commit -m "feat(deploy): wizard.toml state load/save with schema version"
```

---

## Task 4: Secrets — `secrets.go`

**Files:**
- Create: `cmd/deploy/secrets.go`
- Create: `cmd/deploy/secrets_test.go`

- [ ] **Step 1: Failing test для env var lookup**

`cmd/deploy/secrets_test.go`:
```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetSecret_FromEnv(t *testing.T) {
	t.Setenv("WG_VPS_PASS", "envpass")
	store := NewSecretStore()
	got, src := store.Get("WG_VPS_PASS", "VPS root password", nil)
	if got != "envpass" {
		t.Errorf("got %q want %q", got, "envpass")
	}
	if src != SourceEnv {
		t.Errorf("got source %v want %v", src, SourceEnv)
	}
}

func TestGetSecret_FromMemoryFile(t *testing.T) {
	dir := t.TempDir()
	mem := filepath.Join(dir, "host_keenetic.md")
	os.WriteFile(mem, []byte("router\nuser root\npass MySecret123\nport 222\n"), 0o600)

	t.Setenv("WG_KEENETIC_PASS", "")
	store := NewSecretStore()
	got, src := store.Get("WG_KEENETIC_PASS", "Keenetic root password", &MemoryFileLookup{
		Path:    mem,
		Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
	})
	if got != "MySecret123" {
		t.Errorf("got %q want %q", got, "MySecret123")
	}
	if src != SourceMemoryFile {
		t.Errorf("got source %v want %v", src, SourceMemoryFile)
	}
}

func TestSecretStore_Trace(t *testing.T) {
	store := NewSecretStore()
	store.recordPrompted("WG_BOT_TOKEN")
	got := store.PromptedSecrets()
	if len(got) != 1 || got[0] != "WG_BOT_TOKEN" {
		t.Errorf("expected [WG_BOT_TOKEN], got %v", got)
	}
}
```

- [ ] **Step 2: Run — fails**

```bash
go test ./cmd/deploy/ -run TestGetSecret -v
go test ./cmd/deploy/ -run TestSecretStore -v
```

- [ ] **Step 3: Реализовать `cmd/deploy/secrets.go`**

```go
package main

import (
	"os"
	"regexp"
	"strings"
)

type SecretSource int

const (
	SourceMissing SecretSource = iota
	SourceEnv
	SourceMemoryFile
	SourcePrompt
)

func (s SecretSource) String() string {
	switch s {
	case SourceEnv:
		return "env"
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

type SecretStore struct {
	prompted map[string]bool
}

func NewSecretStore() *SecretStore {
	return &SecretStore{prompted: map[string]bool{}}
}

// Get tries: env var → memory file (if provided) → prompt.
// Returns (secret, source). Empty string and SourceMissing if even prompt failed.
func (s *SecretStore) Get(envVar, label string, mem *MemoryFileLookup) (string, SecretSource) {
	if v := os.Getenv(envVar); v != "" {
		return v, SourceEnv
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
	return v, SourcePrompt
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

// PrintSecretsSaveAdvice prints instructions to set the prompted secrets as env vars,
// so the user doesn't re-enter them next run.
func PrintSecretsSaveAdvice(store *SecretStore) {
	prompted := store.PromptedSecrets()
	if len(prompted) == 0 {
		return
	}
	PrintWarn("В этой сессии ты ввёл секреты вручную: " + strings.Join(prompted, ", "))
	fmt.Println("  Чтобы не вводить заново, сохрани их в env vars:")
	fmt.Println()
	fmt.Println("  PowerShell (постоянно):")
	for _, name := range prompted {
		fmt.Printf("    [Environment]::SetEnvironmentVariable(\"%s\", \"<значение>\", \"User\")\n", name)
	}
	fmt.Println()
	fmt.Println("  Bash/Zsh (~/.zshrc или ~/.bashrc):")
	for _, name := range prompted {
		fmt.Printf("    export %s=\"<значение>\"\n", name)
	}
	fmt.Println()
}
```

Добавить импорт `"fmt"` если не появился автоматически.

- [ ] **Step 4: Run tests — pass**

```bash
go test ./cmd/deploy/ -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/deploy/secrets.go cmd/deploy/secrets_test.go
git commit -m "feat(deploy): secret store — env vars + memory file fallback"
```

---

## Task 5: Templates — embed + render

**Files:**
- Create: `cmd/deploy/templates/S99wg-monitor` (copy from `deploy/agent/S99wg-monitor`)
- Create: `cmd/deploy/templates/wg-monitor-backend.service` (copy from `deploy/backend/wg-monitor-backend.service`)
- Create: `cmd/deploy/templates/Caddyfile.tmpl`
- Create: `cmd/deploy/templates/backend.yaml.tmpl`
- Create: `cmd/deploy/templates/agent.yaml.tmpl`
- Create: `cmd/deploy/templates.go`
- Create: `cmd/deploy/templates_test.go`

- [ ] **Step 1: Скопировать неизменные шаблоны**

```bash
mkdir -p cmd/deploy/templates
cp deploy/agent/S99wg-monitor cmd/deploy/templates/S99wg-monitor
cp deploy/backend/wg-monitor-backend.service cmd/deploy/templates/wg-monitor-backend.service
```

- [ ] **Step 2: Создать `cmd/deploy/templates/Caddyfile.tmpl`**

```
# wg-monitor backend reverse proxy.
# Terminate TLS via TLS-ALPN-01 on :443.

{
	email {{.Email}}
	auto_https disable_redirects
}

{{.Domain}} {
	reverse_proxy 127.0.0.1:8080 {
		header_up Host {host}
		header_up X-Real-IP {remote_host}
	}
	request_body {
		max_size 1MB
	}
	log {
		output stderr
		format console
	}
}
```

- [ ] **Step 3: Создать `cmd/deploy/templates/backend.yaml.tmpl`**

```yaml
listen: 127.0.0.1:8080
log_level: info
db_path: /var/lib/wg-monitor/state.db

telegram:
  bot_token: "{{.BotToken}}"
  chat_id: {{.ChatID}}
  admin_user_id: {{.AdminUserID}}

agents:
{{- range .Agents }}
  - nickname: {{.Nickname}}
    token: "{{.Token}}"
    thread_id: {{.ThreadID}}
{{- end }}

state:
  fail_threshold: 2
  recovery_threshold: 2
  mute_cutoff_hour: 23
  realert_every_sec: 3600
  realert_tick_sec: 60

heartbeat:
  stale_after_sec: 120
  stale_after_static_sec: 180
  stale_after_mobile_sec: 300
  resume_grace_sec: 30
  scan_interval_sec: 30
```

- [ ] **Step 4: Создать `cmd/deploy/templates/agent.yaml.tmpl`**

```yaml
backend:
  url: {{.BackendURL}}
  token: "{{.Token}}"

agent:
  nickname: {{.Nickname}}
  interval_sec: 60
  awg_iface: {{.AWGIface}}
  expected_exit_ip: {{.ExpectedExitIP}}

awg_manager:
  url: http://127.0.0.1:2222

checks:
  awg:
    handshake_max_age_sec: 180

  dns:
    auto_discover: true
    test_domain: "example.com"
    fail_threshold: 2

state:
  path: /opt/var/wg-monitor/state.json
```

- [ ] **Step 5: Failing test для рендеринга**

`cmd/deploy/templates_test.go`:
```go
package main

import (
	"strings"
	"testing"
)

func TestRenderBackendYAML(t *testing.T) {
	got, err := RenderBackendYAML(BackendParams{
		BotToken:    "1234:ABCD",
		ChatID:      -1001,
		AdminUserID: 42,
		Agents: []AgentEntry{
			{Nickname: "testkeen", Token: "deadbeef", ThreadID: 7},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		`bot_token: "1234:ABCD"`,
		`chat_id: -1001`,
		`admin_user_id: 42`,
		`nickname: testkeen`,
		`token: "deadbeef"`,
		`thread_id: 7`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered yaml missing %q\nfull:\n%s", want, s)
		}
	}
}

func TestRenderAgentYAML(t *testing.T) {
	got, err := RenderAgentYAML(AgentParams{
		BackendURL:     "https://example.com",
		Token:          "feedface",
		Nickname:       "router1",
		AWGIface:       "awg0",
		ExpectedExitIP: "1.2.3.4",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		"url: https://example.com",
		`token: "feedface"`,
		"nickname: router1",
		"awg_iface: awg0",
		"expected_exit_ip: 1.2.3.4",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered yaml missing %q", want)
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
	for _, name := range []string{"S99wg-monitor", "wg-monitor-backend.service"} {
		got, err := ReadStaticTemplate(name)
		if err != nil {
			t.Errorf("ReadStaticTemplate(%q): %v", name, err)
		}
		if len(got) == 0 {
			t.Errorf("empty %s", name)
		}
	}
}
```

- [ ] **Step 6: Run — fails**

```bash
go test ./cmd/deploy/ -run TestRender -v
go test ./cmd/deploy/ -run TestStaticTemplates -v
```

- [ ] **Step 7: Реализовать `cmd/deploy/templates.go`**

```go
package main

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates
var templatesFS embed.FS

type AgentEntry struct {
	Nickname string
	Token    string
	ThreadID int
}

type BackendParams struct {
	BotToken    string
	ChatID      int64
	AdminUserID int64
	Agents      []AgentEntry
}

type AgentParams struct {
	BackendURL     string
	Token          string
	Nickname       string
	AWGIface       string
	ExpectedExitIP string
}

type CaddyParams struct {
	Domain string
	Email  string
}

func renderTemplate(name string, data any) ([]byte, error) {
	raw, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, fmt.Errorf("read embedded template %s: %w", name, err)
	}
	t, err := template.New(name).Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

func RenderBackendYAML(p BackendParams) ([]byte, error) {
	return renderTemplate("backend.yaml.tmpl", p)
}

func RenderAgentYAML(p AgentParams) ([]byte, error) {
	return renderTemplate("agent.yaml.tmpl", p)
}

func RenderCaddyfile(p CaddyParams) ([]byte, error) {
	return renderTemplate("Caddyfile.tmpl", p)
}

// ReadStaticTemplate returns an embedded file verbatim (no template processing).
// Use for files like S99wg-monitor and wg-monitor-backend.service.
func ReadStaticTemplate(name string) ([]byte, error) {
	return templatesFS.ReadFile("templates/" + name)
}
```

- [ ] **Step 8: Run tests — pass**

```bash
go test ./cmd/deploy/ -run TestRender -v
go test ./cmd/deploy/ -run TestStaticTemplates -v
```

- [ ] **Step 9: Commit**

```bash
git add cmd/deploy/templates cmd/deploy/templates.go cmd/deploy/templates_test.go
git commit -m "feat(deploy): embed config templates + render helpers"
```

---

## Task 6: GitHub Releases client — `github.go`

**Files:**
- Create: `cmd/deploy/github.go`
- Create: `cmd/deploy/github_test.go`
- Create: `cmd/deploy/testdata/github_release.json`

- [ ] **Step 1: Создать тестовую фикстуру `cmd/deploy/testdata/github_release.json`**

(Сокращённый ответ GitHub API — только нужные поля.)

```json
{
  "tag_name": "v0.9.0",
  "name": "v0.9.0",
  "published_at": "2026-05-06T12:00:00Z",
  "assets": [
    {
      "name": "wg-monitor-deploy-linux-amd64",
      "browser_download_url": "https://example.com/wg-monitor-deploy-linux-amd64",
      "size": 5242880
    },
    {
      "name": "wg-monitor-agent-linux-arm64",
      "browser_download_url": "https://example.com/wg-monitor-agent-linux-arm64",
      "size": 2097152
    },
    {
      "name": "wg-monitor-backend-linux-amd64",
      "browser_download_url": "https://example.com/wg-monitor-backend-linux-amd64",
      "size": 8388608
    },
    {
      "name": "checksums.txt",
      "browser_download_url": "https://example.com/checksums.txt",
      "size": 256
    }
  ]
}
```

- [ ] **Step 2: Failing test**

`cmd/deploy/github_test.go`:
```go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRelease(t *testing.T) {
	data, err := os.ReadFile("testdata/github_release.json")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := ParseRelease(data)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v0.9.0" {
		t.Errorf("tag = %q want v0.9.0", rel.TagName)
	}
	if a := rel.AssetByName("wg-monitor-agent-linux-arm64"); a == nil {
		t.Error("expected to find arm64 agent asset")
	}
	if a := rel.AssetByName("nope"); a != nil {
		t.Error("expected nil for missing asset")
	}
}

func TestDownloadAsset_VerifySha(t *testing.T) {
	body := []byte("hello world binary")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	checksums := fmt.Sprintf("%s  testbin\n", want)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/testbin":
			w.Write(body)
		case "/checksums.txt":
			w.Write([]byte(checksums))
		}
	}))
	defer srv.Close()

	cache := t.TempDir()
	dl := &Downloader{
		HTTP:     srv.Client(),
		CacheDir: cache,
	}

	got, err := dl.GetAsset(srv.URL+"/testbin", "testbin", srv.URL+"/checksums.txt", "v0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, cache) {
		t.Errorf("path %q not under cache %q", got, cache)
	}
	data, _ := os.ReadFile(got)
	if string(data) != string(body) {
		t.Error("downloaded body mismatch")
	}

	// Second call hits cache.
	got2, err := dl.GetAsset(srv.URL+"/testbin", "testbin", srv.URL+"/checksums.txt", "v0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if got != got2 {
		t.Error("expected same cached path on second call")
	}
}

func TestDownloadAsset_BadSha(t *testing.T) {
	body := []byte("real")
	wrong := strings.Repeat("0", 64)
	checksums := wrong + "  testbin\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/testbin":
			w.Write(body)
		case "/checksums.txt":
			w.Write([]byte(checksums))
		}
	}))
	defer srv.Close()

	cache := t.TempDir()
	dl := &Downloader{HTTP: srv.Client(), CacheDir: cache}
	_, err := dl.GetAsset(srv.URL+"/testbin", "testbin", srv.URL+"/checksums.txt", "v0.9.0")
	if err == nil {
		t.Fatal("expected sha mismatch error")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("error should mention sha256, got %v", err)
	}
	// Also verify nothing committed to cache.
	entries, _ := filepath.Glob(filepath.Join(cache, "**/testbin"))
	if len(entries) > 0 {
		t.Errorf("expected no leftover files in cache, got %v", entries)
	}
}
```

- [ ] **Step 3: Run — fails**

```bash
go test ./cmd/deploy/ -run TestParseRelease -v
go test ./cmd/deploy/ -run TestDownloadAsset -v
```

- [ ] **Step 4: Реализовать `cmd/deploy/github.go`**

```go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RepoOwner and RepoName are set via -ldflags at build time so forks can rebuild
// without code changes:
//   -X main.RepoOwner=myname -X main.RepoName=wg-monitor-fork
var (
	RepoOwner = "anex"
	RepoName  = "wg-monitor"
)

type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

func (r *Release) AssetByName(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

func ParseRelease(data []byte) (*Release, error) {
	var r Release
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

type Downloader struct {
	HTTP     *http.Client
	CacheDir string
}

func NewDownloader() *Downloader {
	return &Downloader{
		HTTP: &http.Client{Timeout: 60 * time.Second},
		CacheDir: defaultCacheDir(),
	}
}

func defaultCacheDir() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	return filepath.Join(cache, "wg-monitor-deploy")
}

// GetLatestRelease calls https://api.github.com/repos/<owner>/<repo>/releases/latest.
func (d *Downloader) GetLatestRelease() (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", RepoOwner, RepoName)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "wg-monitor-deploy/"+Version)
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, string(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return ParseRelease(body)
}

// GetAsset downloads and caches an asset, verifying its sha256 against checksumsURL.
// Returns the local cache path. Re-downloads if cached file's sha doesn't match.
func (d *Downloader) GetAsset(assetURL, assetName, checksumsURL, tag string) (string, error) {
	tagDir := filepath.Join(d.CacheDir, tag)
	if err := os.MkdirAll(tagDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(tagDir, assetName)

	wantSha, err := d.fetchExpectedSha(checksumsURL, assetName)
	if err != nil {
		return "", fmt.Errorf("checksums.txt: %w", err)
	}

	// Cache hit?
	if existing, err := os.ReadFile(target); err == nil {
		if hashHex(existing) == wantSha {
			return target, nil
		}
		os.Remove(target)
	}

	tmp := target + ".tmp"
	if err := d.downloadTo(assetURL, tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	body, err := os.ReadFile(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	got := hashHex(body)
	if got != wantSha {
		os.Remove(tmp)
		return "", fmt.Errorf("sha256 mismatch for %s: got %s want %s", assetName, got, wantSha)
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return "", err
	}
	return target, nil
}

func (d *Downloader) fetchExpectedSha(checksumsURL, assetName string) (string, error) {
	resp, err := d.HTTP.Get(checksumsURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// checksums.txt format: "<sha256>  <filename>"
		if f[1] == assetName || strings.HasSuffix(f[1], "/"+assetName) {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("asset %s not found in checksums", assetName)
}

func (d *Downloader) downloadTo(url, path string) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "wg-monitor-deploy/"+Version)
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
```

- [ ] **Step 5: Run — pass**

```bash
go test ./cmd/deploy/ -run TestParseRelease -v
go test ./cmd/deploy/ -run TestDownloadAsset -v
```

- [ ] **Step 6: Commit**

```bash
git add cmd/deploy/github.go cmd/deploy/github_test.go cmd/deploy/testdata/github_release.json
git commit -m "feat(deploy): GitHub Releases client with cache + sha256 verify"
```

---

## Task 7: SSH wrapper — `ssh.go`

**Files:**
- Create: `cmd/deploy/ssh.go`
- Create: `cmd/deploy/ssh_test.go`

> **Замечание про тесты:** SSH-клиент сложно полностью изолировать без реального демона. Юнит-тестируем только: парсер known_hosts, helper'ы (формирование команд). Полная проверка — manual smoke в Task 8 (update-backend) против тестовой VM.

- [ ] **Step 1: go get SSH dep**

```bash
go get golang.org/x/crypto/ssh@latest
```

- [ ] **Step 2: Failing test для known_hosts парсера**

`cmd/deploy/ssh_test.go`:
```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestKnownHosts_NewHost_Adds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	kh, err := NewKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}

	// Fake key.
	signer, err := genTestSigner()
	if err != nil {
		t.Skip("genTestSigner unavailable, skipping")
	}
	pub := signer.PublicKey()

	if err := kh.HostKeyCallback("1.2.3.4:22", nil, pub); err != nil {
		t.Errorf("first connect TOFU should succeed, got %v", err)
	}

	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		t.Error("expected known_hosts to be written on first connect")
	}

	// Second connect with same key — should NOT error.
	if err := kh.HostKeyCallback("1.2.3.4:22", nil, pub); err != nil {
		t.Errorf("second connect with same key should succeed, got %v", err)
	}
}

func TestKnownHosts_KeyMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	kh, err := NewKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}

	s1, err := genTestSigner()
	if err != nil {
		t.Skip()
	}
	s2, err := genTestSigner()
	if err != nil {
		t.Skip()
	}

	kh.HostKeyCallback("1.2.3.4:22", nil, s1.PublicKey())

	err = kh.HostKeyCallback("1.2.3.4:22", nil, s2.PublicKey())
	if err == nil {
		t.Fatal("expected MITM detection error on key change")
	}
}

func genTestSigner() (ssh.Signer, error) {
	// Generate ephemeral ed25519 key for tests.
	// (Implementation in ssh.go must export this helper, or use real ssh.NewSignerFromKey.)
	return generateEd25519Signer()
}
```

- [ ] **Step 3: Run — fails (undefined)**

```bash
go test ./cmd/deploy/ -run TestKnownHosts -v
```

- [ ] **Step 4: Реализовать `cmd/deploy/ssh.go`**

```go
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SSH struct {
	client *ssh.Client
	host   string
}

func ConnectSSH(host string, port int, user, password string, kh *KnownHosts) (*SSH, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: kh.HostKeyCallback,
		Timeout:         10 * time.Second,
	}
	c, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", addr, err)
	}
	return &SSH{client: c, host: addr}, nil
}

func (s *SSH) Close() error {
	return s.client.Close()
}

// Run executes a command, returning stdout, stderr, exit code.
// Does NOT fail on non-zero exit — caller decides.
func (s *SSH) Run(cmd string) (string, string, int, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer sess.Close()

	var sout, serr safeBuf
	sess.Stdout = &sout
	sess.Stderr = &serr

	err = sess.Run(cmd)
	rc := 0
	if err != nil {
		var ee *ssh.ExitError
		if errors.As(err, &ee) {
			rc = ee.ExitStatus()
			err = nil
		} else {
			rc = -1
		}
	}
	return sout.String(), serr.String(), rc, err
}

// MustRun runs a command and returns an error if rc != 0 or the SSH layer failed.
func (s *SSH) MustRun(cmd string) (string, error) {
	out, errS, rc, err := s.Run(cmd)
	if err != nil {
		return out, fmt.Errorf("ssh transport: %w", err)
	}
	if rc != 0 {
		return out, fmt.Errorf("cmd %q exit %d: %s", cmd, rc, errS)
	}
	return out, nil
}

// UploadStdin streams raw bytes via `cat > path` (works on dropbear without SFTP).
func (s *SSH) UploadStdin(remotePath string, data []byte) error {
	sess, err := s.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	if err := sess.Start(fmt.Sprintf("cat > %s", remotePath)); err != nil {
		return err
	}
	view := data
	for len(view) > 0 {
		chunk := view
		if len(chunk) > 32768 {
			chunk = chunk[:32768]
		}
		if _, err := stdin.Write(chunk); err != nil {
			return err
		}
		view = view[len(chunk):]
	}
	stdin.Close()
	return sess.Wait()
}

// UploadSFTP uploads via SFTP. Use on systems with full openssh (e.g. VPS).
// Falls back to UploadStdin on dropbear (caller decides which to use).
func (s *SSH) UploadSFTP(remotePath string, data []byte) error {
	// Use scp-equivalent via ssh exec to avoid pulling github.com/pkg/sftp.
	// Pattern: open session, send `dd of=PATH bs=1M`, write to stdin.
	sess, err := s.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	if err := sess.Start(fmt.Sprintf("dd of=%s bs=1M status=none", remotePath)); err != nil {
		return err
	}
	if _, err := io.Copy(stdin, bytesReader(data)); err != nil {
		stdin.Close()
		return err
	}
	stdin.Close()
	return sess.Wait()
}

// safeBuf is a thread-safe bytes.Buffer (ssh stdout/stderr writes can race).
type safeBuf struct {
	mu sync.Mutex
	b  []byte
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}
func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}

func bytesReader(b []byte) io.Reader {
	return &byteReader{b: b}
}

type byteReader struct {
	b   []byte
	pos int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

// ----- Known hosts (TOFU) -----

type KnownHosts struct {
	path string
	mu   sync.Mutex
}

func NewKnownHosts(path string) (*KnownHosts, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	return &KnownHosts{path: path}, nil
}

// HostKeyCallback implements ssh.HostKeyCallback with TOFU semantics:
//   - first connect to a host: append fingerprint to known_hosts, accept
//   - subsequent connects: must match
//   - mismatch: return error with clear message
func (k *KnownHosts) HostKeyCallback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	cb, err := knownhosts.New(k.path)
	if err != nil {
		return err
	}
	err = cb(hostname, remote, key)
	if err == nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			// First time seeing this host — append.
			line := knownhosts.Line([]string{hostname}, key)
			f, ferr := os.OpenFile(k.path, os.O_APPEND|os.O_WRONLY, 0o600)
			if ferr != nil {
				return ferr
			}
			defer f.Close()
			if _, werr := f.WriteString(line + "\n"); werr != nil {
				return werr
			}
			return nil
		}
		return fmt.Errorf("HOST KEY CHANGED for %s — possible MITM attack. "+
			"Inspect %s and remove the offending line if you trust the new key",
			hostname, k.path)
	}
	return err
}

// generateEd25519Signer is exported for tests.
func generateEd25519Signer() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}
```

- [ ] **Step 5: Run tests — pass**

```bash
go test ./cmd/deploy/ -run TestKnownHosts -v
```

- [ ] **Step 6: Commit**

```bash
git add cmd/deploy/ssh.go cmd/deploy/ssh_test.go go.mod go.sum
git commit -m "feat(deploy): SSH wrapper with TOFU known_hosts + UploadStdin for dropbear"
```

---

## Task 8: First action: `update-backend` — end-to-end через CLI

**Files:**
- Create: `cmd/deploy/steps.go` (первая порция)
- Create: `cmd/deploy/actions.go` (первый action)
- Modify: `cmd/deploy/main.go` (диспетчер)

- [ ] **Step 1: Реализовать helper-шаги в `cmd/deploy/steps.go`**

```go
package main

import (
	"fmt"
	"strings"
)

// stepCheckSSH connects and reports OK/fail.
func stepCheckSSH(s *SSH, label string) error {
	out, err := s.MustRun("uname -a")
	if err != nil {
		PrintFail(fmt.Sprintf("SSH к %s не отвечает: %v", label, err))
		return err
	}
	PrintOK(fmt.Sprintf("SSH к %s OK (%s)", label, strings.TrimSpace(out)))
	return nil
}

// stepDownloadAsset fetches a binary from GitHub Releases (cached).
// Returns local path.
func stepDownloadAsset(dl *Downloader, rel *Release, assetName string) (string, error) {
	asset := rel.AssetByName(assetName)
	if asset == nil {
		PrintFail(fmt.Sprintf("в релизе %s нет артефакта %s", rel.TagName, assetName))
		return "", fmt.Errorf("missing asset %s", assetName)
	}
	checks := rel.AssetByName("checksums.txt")
	if checks == nil {
		PrintFail("в релизе нет checksums.txt — отказываюсь без верификации")
		return "", fmt.Errorf("missing checksums.txt")
	}
	PrintInfo(fmt.Sprintf("скачиваю %s...", assetName))
	path, err := dl.GetAsset(asset.DownloadURL, assetName, checks.DownloadURL, rel.TagName)
	if err != nil {
		PrintFail(err.Error())
		return "", err
	}
	PrintOK(fmt.Sprintf("%s готов (%s)", assetName, path))
	return path, nil
}

// stepUploadAndSwap: stop service → upload to TMP via dd-stdin → sha256 verify → atomic mv → start.
// service = systemd unit name (e.g. "wg-monitor-backend") or empty to skip systemctl
func stepUploadAndSwap(s *SSH, localPath, remotePath, service string) error {
	data, err := readFile(localPath)
	if err != nil {
		PrintFail("read local: " + err.Error())
		return err
	}
	wantSha := hashHex(data)

	if service != "" {
		PrintInfo("systemctl stop " + service)
		if _, err := s.MustRun("systemctl stop " + service); err != nil {
			PrintFail(err.Error())
			return err
		}
	}

	tmp := remotePath + ".new"
	PrintInfo(fmt.Sprintf("upload → %s (%d bytes)", tmp, len(data)))
	if err := s.UploadSFTP(tmp, data); err != nil {
		PrintFail("upload: " + err.Error())
		return err
	}

	out, err := s.MustRun("sha256sum " + tmp + " | awk '{print $1}'")
	if err != nil {
		PrintFail("sha256sum: " + err.Error())
		return err
	}
	gotSha := strings.TrimSpace(out)
	if gotSha != wantSha {
		PrintFail(fmt.Sprintf("sha256 mismatch: local %s remote %s", wantSha[:16], gotSha[:16]))
		s.Run("rm -f " + tmp)
		return fmt.Errorf("sha256 mismatch")
	}
	PrintOK("sha256 совпадает")

	cmd := fmt.Sprintf("mv %s %s && chmod 755 %s", tmp, remotePath, remotePath)
	if _, err := s.MustRun(cmd); err != nil {
		PrintFail("atomic swap: " + err.Error())
		return err
	}
	PrintOK("бинарь обновлён")

	if service != "" {
		PrintInfo("systemctl start " + service)
		if _, err := s.MustRun("systemctl start " + service); err != nil {
			PrintFail(err.Error())
			return err
		}
		PrintOK(service + " запущен")
	}
	return nil
}

// stepVerifyHTTP: curl URL, expect 200.
func stepVerifyHTTP(s *SSH, url string) error {
	cmd := fmt.Sprintf("curl -sS -o /dev/null -w '%%{http_code}' %s", url)
	out, err := s.MustRun(cmd)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	code := strings.TrimSpace(out)
	if code != "200" {
		PrintFail(fmt.Sprintf("%s → HTTP %s", url, code))
		return fmt.Errorf("expected 200 got %s", code)
	}
	PrintOK(fmt.Sprintf("%s → 200 OK", url))
	return nil
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
```

Импорт `"os"` и других — добавить.

- [ ] **Step 2: Реализовать `actionUpdateBackend` в `cmd/deploy/actions.go`**

```go
package main

import (
	"fmt"
	"time"
)

func actionUpdateBackend(state *State, secrets *SecretStore, dl *Downloader) error {
	if state.Backend.Host == "" {
		PrintFail("В wizard.toml нет [backend] — сначала запусти install-backend")
		return fmt.Errorf("no backend configured")
	}

	rel, err := dl.GetLatestRelease()
	if err != nil {
		PrintFail("GitHub API: " + err.Error())
		return err
	}
	PrintOK(fmt.Sprintf("последний релиз: %s", rel.TagName))

	pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
	if pass == "" {
		PrintFail("пароль обязателен")
		return fmt.Errorf("missing password")
	}

	khPath := defaultCacheDir() + "/known_hosts"
	kh, err := NewKnownHosts(khPath)
	if err != nil {
		return err
	}

	port := state.Backend.Port
	if port == 0 {
		port = 22
	}
	user := state.Backend.User
	if user == "" {
		user = "root"
	}

	PrintStep(1, 4, "SSH к VPS")
	s, err := ConnectSSH(state.Backend.Host, port, user, pass, kh)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	defer s.Close()
	if err := stepCheckSSH(s, state.Backend.Host); err != nil {
		return err
	}

	PrintStep(2, 4, "Скачать бэкенд бинарь")
	localPath, err := stepDownloadAsset(dl, rel, "wg-monitor-backend-linux-amd64")
	if err != nil {
		return err
	}

	PrintStep(3, 4, "Atomic upload + restart")
	if err := stepUploadAndSwap(s, localPath, "/usr/local/bin/wg-monitor-backend", "wg-monitor-backend"); err != nil {
		return err
	}

	PrintStep(4, 4, "Verify /health")
	if state.Backend.Domain == "" {
		PrintWarn("домен не задан в wizard.toml — пропускаю /health проверку")
	} else {
		url := "https://" + state.Backend.Domain + "/health"
		if err := stepVerifyHTTP(s, url); err != nil {
			return err
		}
	}

	state.Backend.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	state.Backend.LastDeployedVersion = rel.TagName
	return nil
}
```

- [ ] **Step 3: Подключить в `main.go` подкоманду `update-backend`**

Заменить тело `main()` в `cmd/deploy/main.go`:

```go
func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	configPath := flag.String("config", "", "path to wizard.toml (default: platform default)")
	noColor := flag.Bool("no-color", false, "disable ANSI colors")
	flag.Parse()

	if *versionFlag {
		fmt.Println(Version)
		return
	}
	if *noColor {
		UseColor = false
	}

	statePath := *configPath
	if statePath == "" {
		// cwd-fallback first, for repo-local dev
		if _, err := os.Stat("wizard.toml"); err == nil {
			statePath = "wizard.toml"
		} else {
			statePath = DefaultStatePath()
		}
	}
	state, err := LoadState(statePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load state:", err)
		os.Exit(1)
	}
	secrets := NewSecretStore()
	dl := NewDownloader()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("wg-monitor-deploy", Version, "— меню в Task 12")
		fmt.Println("пока доступно: update-backend")
		return
	}

	switch args[0] {
	case "update-backend":
		if err := actionUpdateBackend(state, secrets, dl); err != nil {
			os.Exit(1)
		}
		if err := SaveState(statePath, state); err != nil {
			fmt.Fprintln(os.Stderr, "save state:", err)
		}
		PrintSecretsSaveAdvice(secrets)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		os.Exit(2)
	}
}
```

- [ ] **Step 4: Билд + smoke-тест против реального VPS**

```bash
make build-deploy
# создать или скопировать рядом wizard.toml с [backend] host = "..." domain = "..."
# (примерно: cp /etc/wg-monitor/backend.yaml -> вытащить domain)
$env:WG_VPS_PASS = "<пароль>"
./bin/wg-monitor-deploy update-backend
```

Ожидаемый вывод (примерно):
```
✓ последний релиз: v0.9.0
[1/4] SSH к VPS
  ✓ SSH к 103.106.1.253 OK (Linux ...)
[2/4] Скачать бэкенд бинарь
  → скачиваю wg-monitor-backend-linux-amd64...
  ✓ wg-monitor-backend-linux-amd64 готов
[3/4] Atomic upload + restart
  → systemctl stop wg-monitor-backend
  → upload → /usr/local/bin/wg-monitor-backend.new (...)
  ✓ sha256 совпадает
  ✓ бинарь обновлён
  → systemctl start wg-monitor-backend
  ✓ wg-monitor-backend запущен
[4/4] Verify /health
  ✓ https://wgmonitor.jkaotlic.duckdns.org/health → 200 OK
```

**ВАЖНО:** Перед smoke-тестом убедиться, что в GitHub Releases есть тестовый релиз с артефактом `wg-monitor-backend-linux-amd64` + `checksums.txt`. Если нет — сначала Task 13 (CI), либо временно патчем подменить URL на локальный mock-сервер. Альтернатива для smoke без CI: добавить временный флаг `--bin <path>` который пропускает скачивание (это хак для одной проверки, удалить после).

- [ ] **Step 5: Run unit tests целиком**

```bash
go test ./cmd/deploy/... -v
```
Все тесты должны быть PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/deploy/steps.go cmd/deploy/actions.go cmd/deploy/main.go
git commit -m "feat(deploy): update-backend action — first end-to-end flow"
```

---

## Task 9: Action: `update-agent`

**Files:**
- Modify: `cmd/deploy/steps.go` (добавить шаги для агента)
- Modify: `cmd/deploy/actions.go` (добавить `actionUpdateAgent`)
- Modify: `cmd/deploy/main.go` (роутинг)

- [ ] **Step 1: Добавить `stepDetectKeeneticArch` в `steps.go`**

```go
// stepDetectKeeneticArch returns "arm64" or "mipsle" based on `uname -m`.
// Aborts on unsupported arch.
func stepDetectKeeneticArch(s *SSH) (string, error) {
	out, err := s.MustRun("uname -m")
	if err != nil {
		return "", err
	}
	arch := strings.TrimSpace(out)
	switch arch {
	case "aarch64", "arm64":
		PrintOK("архитектура: arm64")
		return "arm64", nil
	case "mips", "mipsel", "mipsle":
		PrintOK("архитектура: mipsle")
		return "mipsle", nil
	default:
		PrintFail(fmt.Sprintf("неподдерживаемая архитектура %q (поддержано: aarch64, mipsel)", arch))
		return "", fmt.Errorf("unsupported arch: %s", arch)
	}
}
```

- [ ] **Step 2: Добавить `stepUploadAgentBinary` (через UploadStdin для dropbear)**

```go
// stepUploadAgentBinary stops, swaps, restarts the Keenetic agent.
// Uses UploadStdin (dropbear has no SFTP/dd-friendly stack on some firmwares).
func stepUploadAgentBinary(s *SSH, localPath, remotePath string) error {
	data, err := readFile(localPath)
	if err != nil {
		PrintFail("read local: " + err.Error())
		return err
	}
	wantSha := hashHex(data)

	PrintInfo("ensure /opt/var/wg-monitor exists")
	if _, err := s.MustRun("mkdir -p /opt/var/wg-monitor /opt/bin /opt/etc/wg-monitor /opt/etc/init.d"); err != nil {
		PrintFail(err.Error())
		return err
	}

	PrintInfo("остановка агента")
	s.Run("/opt/etc/init.d/S99wg-monitor stop 2>/dev/null; killall -9 wg-monitor 2>/dev/null; sleep 1; true")

	tmp := remotePath + ".new"
	PrintInfo(fmt.Sprintf("upload → %s (%d bytes, через stdin pipe)", tmp, len(data)))
	if err := s.UploadStdin(tmp, data); err != nil {
		PrintFail("upload: " + err.Error())
		return err
	}
	if _, err := s.MustRun("chmod 755 " + tmp); err != nil {
		PrintFail(err.Error())
		return err
	}

	out, err := s.MustRun("sha256sum " + tmp + " | awk '{print $1}'")
	if err != nil {
		PrintFail("sha256sum: " + err.Error())
		return err
	}
	gotSha := strings.TrimSpace(out)
	if gotSha != wantSha {
		PrintFail(fmt.Sprintf("sha256 mismatch: local %s remote %s", wantSha[:16], gotSha[:16]))
		s.Run("rm -f " + tmp)
		return fmt.Errorf("sha256 mismatch")
	}
	PrintOK("sha256 совпадает")

	if _, err := s.MustRun("mv " + tmp + " " + remotePath); err != nil {
		PrintFail("atomic swap: " + err.Error())
		return err
	}
	PrintOK("бинарь обновлён")

	if _, err := s.MustRun("/opt/etc/init.d/S99wg-monitor start"); err != nil {
		PrintFail("start: " + err.Error())
		return err
	}

	// Wait briefly for the daemon to come up.
	time.Sleep(2 * time.Second)
	out, _ = s.MustRun("pidof wg-monitor")
	if strings.TrimSpace(out) == "" {
		PrintFail("процесс wg-monitor не появился после старта")
		return fmt.Errorf("agent did not start")
	}
	PrintOK("агент запущен (PID " + strings.TrimSpace(out) + ")")
	return nil
}
```

Добавить импорт `"time"` если не появился.

- [ ] **Step 3: Реализовать `actionUpdateAgent`**

В `cmd/deploy/actions.go`:

```go
func actionUpdateAgent(state *State, secrets *SecretStore, dl *Downloader, nickname string) error {
	if len(state.Agents) == 0 {
		PrintFail("В wizard.toml нет [[agents]] — сначала install-agent / add-router")
		return fmt.Errorf("no agents configured")
	}
	var ag *AgentState
	if nickname != "" {
		ag = state.FindAgent(nickname)
		if ag == nil {
			PrintFail("агент с никнеймом " + nickname + " не найден в wizard.toml")
			return fmt.Errorf("agent not found")
		}
	} else if len(state.Agents) == 1 {
		ag = &state.Agents[0]
	} else {
		PrintWarn("несколько агентов — укажи --agent <nickname>")
		for _, a := range state.Agents {
			fmt.Println("  -", a.Nickname)
		}
		return fmt.Errorf("ambiguous agent")
	}

	rel, err := dl.GetLatestRelease()
	if err != nil {
		PrintFail("GitHub API: " + err.Error())
		return err
	}
	PrintOK("последний релиз: " + rel.TagName)

	envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
	memFile := os.ExpandEnv("$HOME/.claude/projects/c--Users-Anex-Projects-wg-monitor/memory/host_keenetic.md")
	pass, _ := secrets.Get(envName, "пароль root для "+ag.Nickname, &MemoryFileLookup{
		Path:    memFile,
		Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
	})
	if pass == "" {
		// Fallback to global WG_KEENETIC_PASS
		pass, _ = secrets.Get("WG_KEENETIC_PASS", "пароль root", nil)
	}
	if pass == "" {
		return fmt.Errorf("missing password")
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}

	PrintStep(1, 4, "SSH к роутеру "+ag.Nickname)
	s, err := ConnectSSH(ag.Host, portOrDefault(ag.Port, 222), userOrDefault(ag.User, "root"), pass, kh)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	defer s.Close()
	if err := stepCheckSSH(s, ag.Host); err != nil {
		return err
	}

	PrintStep(2, 4, "Определить архитектуру")
	arch, err := stepDetectKeeneticArch(s)
	if err != nil {
		return err
	}
	if ag.Arch == "" {
		ag.Arch = arch
	}

	PrintStep(3, 4, "Скачать агент бинарь")
	assetName := "wg-monitor-agent-linux-" + arch
	localPath, err := stepDownloadAsset(dl, rel, assetName)
	if err != nil {
		return err
	}

	PrintStep(4, 4, "Stop → upload → swap → start")
	if err := stepUploadAgentBinary(s, localPath, "/opt/bin/wg-monitor"); err != nil {
		return err
	}

	ag.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	ag.LastDeployedVersion = rel.TagName
	return nil
}

func portOrDefault(p, def int) int {
	if p == 0 {
		return def
	}
	return p
}

func userOrDefault(u, def string) string {
	if u == "" {
		return def
	}
	return u
}
```

Добавить `import "os"` и `import "strings"` если не подтянулись.

- [ ] **Step 4: Добавить роутинг `update-agent` в `main.go`**

```go
case "update-agent":
    agentFlag := ""
    // Parse --agent flag from remaining args
    for i := 1; i < len(args); i++ {
        if args[i] == "--agent" && i+1 < len(args) {
            agentFlag = args[i+1]
        }
    }
    if err := actionUpdateAgent(state, secrets, dl, agentFlag); err != nil {
        os.Exit(1)
    }
    if err := SaveState(statePath, state); err != nil {
        fmt.Fprintln(os.Stderr, "save state:", err)
    }
    PrintSecretsSaveAdvice(secrets)
```

- [ ] **Step 5: Билд + smoke против реального роутера**

```bash
make build-deploy
./bin/wg-monitor-deploy update-agent
```

Ожидаемый вывод:
```
✓ последний релиз: v0.9.0
[1/4] SSH к роутеру testkeen
  ✓ SSH к 192.168.31.1 OK (Linux ...)
[2/4] Определить архитектуру
  ✓ архитектура: arm64
[3/4] Скачать агент бинарь
  ✓ wg-monitor-agent-linux-arm64 готов
[4/4] Stop → upload → swap → start
  → ensure /opt/var/wg-monitor exists
  → остановка агента
  → upload → /opt/bin/wg-monitor.new (5234567 bytes, через stdin pipe)
  ✓ sha256 совпадает
  ✓ бинарь обновлён
  ✓ агент запущен (PID 12453)
```

- [ ] **Step 6: Run unit tests**

```bash
go test ./cmd/deploy/...
```

- [ ] **Step 7: Commit**

```bash
git add cmd/deploy/steps.go cmd/deploy/actions.go cmd/deploy/main.go
git commit -m "feat(deploy): update-agent action with dropbear stdin pipe"
```

---

## Task 10: First-time installs — `install-backend`, `install-agent`, `add-router`

> Эти actions большие (10–14 шагов каждый). Реализуем по частям. Подзадачи: 10a, 10b, 10c.

### Task 10a: `install-backend` (12 шагов)

**Files:**
- Modify: `cmd/deploy/steps.go` (добавить шаги для install)
- Modify: `cmd/deploy/actions.go` (добавить `actionInstallBackend`)
- Modify: `cmd/deploy/main.go`

- [ ] **Step 1: Добавить шаги установки в `steps.go`**

```go
// stepEnsureUser creates a system user if missing.
func stepEnsureUser(s *SSH, name string) error {
	out, _ := s.MustRun("id -u " + name + " 2>/dev/null; true")
	if strings.TrimSpace(out) != "" {
		PrintSkip("user " + name + " существует")
		return nil
	}
	cmd := fmt.Sprintf("useradd --system --no-create-home --shell /usr/sbin/nologin %s", name)
	if _, err := s.MustRun(cmd); err != nil {
		PrintFail(err.Error())
		return err
	}
	PrintOK("user " + name + " создан")
	return nil
}

// stepEnsureDir mkdir -p with chown.
func stepEnsureDir(s *SSH, path, owner string) error {
	if _, err := s.MustRun("mkdir -p " + path); err != nil {
		PrintFail(err.Error())
		return err
	}
	if owner != "" {
		s.MustRun("chown " + owner + " " + path)
	}
	PrintOK(path)
	return nil
}

// stepUploadFile uploads bytes via UploadSFTP and chmod's.
func stepUploadFile(s *SSH, remotePath string, data []byte, mode string) error {
	if err := s.UploadSFTP(remotePath, data); err != nil {
		PrintFail("upload: " + err.Error())
		return err
	}
	if _, err := s.MustRun("chmod " + mode + " " + remotePath); err != nil {
		PrintFail(err.Error())
		return err
	}
	PrintOK(remotePath)
	return nil
}

// stepCheckCaddyInstalled returns true if caddy is on PATH.
func stepCheckCaddyInstalled(s *SSH) bool {
	_, _, rc, _ := s.Run("which caddy")
	return rc == 0
}

// stepInstallCaddy: A/M/S choice. A only if Debian-family.
func stepInstallCaddy(s *SSH) error {
	if stepCheckCaddyInstalled(s) {
		PrintSkip("caddy уже установлен")
		return nil
	}
	PrintWarn("Caddy не установлен. Команды для установки на Debian/Ubuntu:")
	fmt.Println(Colorize("    apt install -y debian-keyring debian-archive-keyring apt-transport-https", ColorDim))
	fmt.Println(Colorize("    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \\", ColorDim))
	fmt.Println(Colorize("      | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg", ColorDim))
	fmt.Println(Colorize("    echo 'deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] \\", ColorDim))
	fmt.Println(Colorize("      https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main' \\", ColorDim))
	fmt.Println(Colorize("      > /etc/apt/sources.list.d/caddy-stable.list", ColorDim))
	fmt.Println(Colorize("    apt update && apt install -y caddy", ColorDim))
	fmt.Println()

	// Detect Debian family for [A] availability.
	_, _, rc, _ := s.Run("test -f /etc/debian_version")
	debian := rc == 0

	opts := []ChoiceOption{}
	if debian {
		opts = append(opts, ChoiceOption{"A", "Сделай за меня по SSH"})
	}
	opts = append(opts,
		ChoiceOption{"M", "Я сам поставлю — нажму Enter когда готов"},
		ChoiceOption{"S", "Скипнуть"},
	)
	choice := AskChoice("Что делаем?", opts)

	switch choice {
	case "A":
		install := strings.Join([]string{
			"apt install -y debian-keyring debian-archive-keyring apt-transport-https",
			"curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg",
			"echo 'deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main' > /etc/apt/sources.list.d/caddy-stable.list",
			"apt update",
			"apt install -y caddy",
		}, " && ")
		if _, err := s.MustRun(install); err != nil {
			PrintFail(err.Error())
			return err
		}
		PrintOK("caddy установлен")
	case "M":
		Ask("Поставь Caddy и нажми Enter", "")
		if !stepCheckCaddyInstalled(s) {
			PrintFail("Caddy всё ещё не найден. Прерываю.")
			return fmt.Errorf("caddy not installed")
		}
		PrintOK("caddy найден")
	case "S":
		PrintWarn("Caddy скипнут. /health не сможет ответить через TLS.")
	}
	return nil
}
```

- [ ] **Step 2: Реализовать `actionInstallBackend`**

```go
func actionInstallBackend(state *State, secrets *SecretStore, dl *Downloader) error {
	rel, err := dl.GetLatestRelease()
	if err != nil {
		return err
	}
	PrintOK("последний релиз: " + rel.TagName)

	// 1. Запросить параметры
	state.Backend.Host = Ask("VPS host или IP", state.Backend.Host)
	state.Backend.Port = parseIntOr(Ask("SSH port", strOrDefault(state.Backend.Port, "22")), 22)
	state.Backend.User = orDefault(Ask("SSH user", strOrDefaultS(state.Backend.User, "root")), "root")
	state.Backend.Domain = Ask("Домен бэкенда (например wgmon.example.com)", state.Backend.Domain)
	caddyEmail := Ask("Email для Let's Encrypt", "admin@"+state.Backend.Domain)

	if state.Telegram.ChatID == 0 {
		state.Telegram.ChatID = parseInt64Or(Ask("Telegram chat_id (отрицательное число)", ""), 0)
	}
	if state.Telegram.AdminUserID == 0 {
		state.Telegram.AdminUserID = parseInt64Or(Ask("Telegram admin user_id (твой User ID)", ""), 0)
	}

	pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
	botToken, _ := secrets.Get("WG_BOT_TOKEN", "Telegram bot token (1234:ABC...)", nil)

	if pass == "" || botToken == "" || state.Backend.Host == "" || state.Backend.Domain == "" {
		PrintFail("обязательные поля пустые")
		return fmt.Errorf("missing required fields")
	}

	// Запросить хотя бы одного агента (для backend.yaml.agents)
	if len(state.Agents) == 0 {
		nick := Ask("Никнейм первого роутера (a-z, 2-16)", "testkeen")
		thread := parseIntOr(Ask("Telegram thread_id для топика этого роутера", "1"), 1)
		state.Agents = append(state.Agents, AgentState{
			Nickname: nick,
			ThreadID: thread,
		})
	}
	// Сгенерить токен агенту если ещё нет в env
	agentTokens := map[string]string{}
	for i := range state.Agents {
		ag := &state.Agents[i]
		envName := "WG_AGENT_TOKEN_" + strings.ToUpper(ag.Nickname)
		tok := os.Getenv(envName)
		if tok == "" {
			tok = randomHexToken(32)
			PrintWarn(fmt.Sprintf("сгенерирован токен для %s — сохрани в %s", ag.Nickname, envName))
			fmt.Println("    " + tok)
		}
		agentTokens[ag.Nickname] = tok
	}

	// 2. SSH
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	PrintStep(1, 12, "SSH к VPS")
	s, err := ConnectSSH(state.Backend.Host, state.Backend.Port, state.Backend.User, pass, kh)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	defer s.Close()

	PrintStep(2, 12, "User wgmonitor")
	if err := stepEnsureUser(s, "wgmonitor"); err != nil {
		return err
	}

	PrintStep(3, 12, "Директории")
	stepEnsureDir(s, "/etc/wg-monitor", "")
	stepEnsureDir(s, "/var/lib/wg-monitor", "wgmonitor:wgmonitor")

	PrintStep(4, 12, "backend.yaml")
	var entries []AgentEntry
	for _, ag := range state.Agents {
		entries = append(entries, AgentEntry{
			Nickname: ag.Nickname,
			Token:    agentTokens[ag.Nickname],
			ThreadID: ag.ThreadID,
		})
	}
	yaml, err := RenderBackendYAML(BackendParams{
		BotToken:    botToken,
		ChatID:      state.Telegram.ChatID,
		AdminUserID: state.Telegram.AdminUserID,
		Agents:      entries,
	})
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/wg-monitor/backend.yaml", yaml, "600"); err != nil {
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
	PrintOK("daemon-reload + enable")

	PrintStep(6, 12, "Caddy")
	if err := stepInstallCaddy(s); err != nil {
		return err
	}

	PrintStep(7, 12, "Caddyfile")
	cf, err := RenderCaddyfile(CaddyParams{Domain: state.Backend.Domain, Email: caddyEmail})
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/caddy/Caddyfile", cf, "644"); err != nil {
		return err
	}
	if _, err := s.MustRun("systemctl enable --now caddy && systemctl reload caddy"); err != nil {
		PrintWarn("caddy reload не прошёл — возможно, не установлен")
	} else {
		PrintOK("caddy reloaded")
	}

	PrintStep(8, 12, "Скачать backend бинарь")
	localPath, err := stepDownloadAsset(dl, rel, "wg-monitor-backend-linux-amd64")
	if err != nil {
		return err
	}

	PrintStep(9, 12, "Upload + sha + swap")
	if err := stepUploadAndSwap(s, localPath, "/usr/local/bin/wg-monitor-backend", ""); err != nil {
		return err
	}

	PrintStep(10, 12, "Start service")
	if _, err := s.MustRun("systemctl start wg-monitor-backend"); err != nil {
		return err
	}
	PrintOK("wg-monitor-backend started")

	time.Sleep(3 * time.Second)

	PrintStep(11, 12, "Verify systemctl is-active")
	out, _ := s.MustRun("systemctl is-active wg-monitor-backend")
	if strings.TrimSpace(out) != "active" {
		PrintFail("сервис не active. Логи:")
		jr, _ := s.MustRun("journalctl -u wg-monitor-backend -n 30 --no-pager")
		fmt.Println(jr)
		return fmt.Errorf("service not active")
	}
	PrintOK("active")

	PrintStep(12, 12, "Verify /health через домен")
	url := "https://" + state.Backend.Domain + "/health"
	if err := stepVerifyHTTP(s, url); err != nil {
		PrintWarn("health check не прошёл — возможно DNS ещё не прогрелся, проверь руками")
	}

	state.Backend.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	state.Backend.LastDeployedVersion = rel.TagName
	return nil
}

// --- helpers ---

func parseIntOr(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	if n == 0 {
		return def
	}
	return n
}

func parseInt64Or(s string, def int64) int64 {
	if s == "" {
		return def
	}
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}

func strOrDefault(n int, def string) string {
	if n == 0 {
		return def
	}
	return fmt.Sprintf("%d", n)
}

func strOrDefaultS(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func randomHexToken(nBytes int) string {
	b := make([]byte, nBytes)
	rand.Read(b)
	return hex.EncodeToString(b)
}
```

Импорты добавить: `"crypto/rand"` (псевдоним если конфликт с ssh-импортом — `mathrand "crypto/rand"`), `"encoding/hex"`.

Если `crypto/rand` конфликтует с `crypto/ed25519` импортом в `ssh.go` — вынести `randomHexToken` в `ssh.go` где `rand` уже импортирован.

- [ ] **Step 3: Подключить `install-backend` в `main.go`**

```go
case "install-backend":
    if err := actionInstallBackend(state, secrets, dl); err != nil {
        os.Exit(1)
    }
    SaveState(statePath, state)
    PrintSecretsSaveAdvice(secrets)
```

- [ ] **Step 4: Билд + smoke против чистой VM (или существующего VPS — будет идемпотентно)**

```bash
make build-deploy
./bin/wg-monitor-deploy install-backend
```

Wizard должен последовательно пройти 12 шагов. На существующем VPS все шаги должны быть `✓ skip`-ом или быстрым no-op.

- [ ] **Step 5: Commit**

```bash
git add cmd/deploy/steps.go cmd/deploy/actions.go cmd/deploy/main.go
git commit -m "feat(deploy): install-backend action — 12 idempotent steps"
```

### Task 10b: `install-agent`

- [ ] **Step 1: Добавить `actionInstallAgent` в `actions.go`**

```go
func actionInstallAgent(state *State, secrets *SecretStore, dl *Downloader, nickname string) error {
	rel, err := dl.GetLatestRelease()
	if err != nil {
		return err
	}

	// 1. Найти или создать агента
	var ag *AgentState
	if nickname != "" {
		ag = state.FindAgent(nickname)
	}
	if ag == nil {
		// Создать новую запись
		nick := nickname
		if nick == "" {
			nick = Ask("Никнейм роутера (a-z0-9_-, 2-16)", "")
		}
		if nick == "" {
			return fmt.Errorf("nickname required")
		}
		state.Agents = append(state.Agents, AgentState{Nickname: nick})
		ag = &state.Agents[len(state.Agents)-1]
	}

	ag.Host = Ask("Хост роутера", strOrDefaultS(ag.Host, "192.168.31.1"))
	ag.Port = parseIntOr(Ask("SSH port", strOrDefault(ag.Port, "222")), 222)
	ag.User = orDefault(Ask("SSH user", strOrDefaultS(ag.User, "root")), "root")
	ag.AwgIface = orDefault(Ask("AmneziaWG iface", strOrDefaultS(ag.AwgIface, "awg0")), "awg0")
	ag.ExpectedExitIP = Ask("Expected exit IP (что bot должен видеть как public IP)", ag.ExpectedExitIP)
	if ag.ThreadID == 0 {
		ag.ThreadID = parseIntOr(Ask("Telegram thread_id топика этого роутера", "1"), 1)
	}

	envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
	memFile := os.ExpandEnv("$HOME/.claude/projects/c--Users-Anex-Projects-wg-monitor/memory/host_keenetic.md")
	pass, _ := secrets.Get(envName, "пароль root для "+ag.Nickname, &MemoryFileLookup{
		Path:    memFile,
		Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
	})
	if pass == "" {
		pass, _ = secrets.Get("WG_KEENETIC_PASS", "пароль root", nil)
	}
	if pass == "" {
		return fmt.Errorf("missing password")
	}

	tokenEnv := "WG_AGENT_TOKEN_" + strings.ToUpper(ag.Nickname)
	tok := os.Getenv(tokenEnv)
	if tok == "" {
		PrintWarn("Токен агента не найден в " + tokenEnv + ".")
		PrintWarn("При install-backend он должен был сгенерироваться. Введи руками или сгенерирую новый:")
		tok = Ask("Token (Enter — сгенерировать новый)", "")
		if tok == "" {
			tok = randomHexToken(32)
			PrintWarn("новый токен: " + tok + " — сохрани в " + tokenEnv + " И в backend.yaml на VPS!")
		}
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}

	PrintStep(1, 7, "SSH к роутеру")
	s, err := ConnectSSH(ag.Host, ag.Port, ag.User, pass, kh)
	if err != nil {
		return err
	}
	defer s.Close()

	PrintStep(2, 7, "Архитектура")
	arch, err := stepDetectKeeneticArch(s)
	if err != nil {
		return err
	}
	ag.Arch = arch

	PrintStep(3, 7, "Директории /opt/{bin,etc/wg-monitor,etc/init.d,var/wg-monitor}")
	if _, err := s.MustRun("mkdir -p /opt/bin /opt/etc/wg-monitor /opt/etc/init.d /opt/var/wg-monitor"); err != nil {
		return err
	}
	PrintOK("ok")

	PrintStep(4, 7, "config.yaml")
	cfg, err := RenderAgentYAML(AgentParams{
		BackendURL:     "https://" + state.Backend.Domain,
		Token:          tok,
		Nickname:       ag.Nickname,
		AWGIface:       ag.AwgIface,
		ExpectedExitIP: ag.ExpectedExitIP,
	})
	if err != nil {
		return err
	}
	// dropbear: prefer UploadStdin
	if err := s.UploadStdin("/opt/etc/wg-monitor/config.yaml", cfg); err != nil {
		return err
	}
	s.MustRun("chmod 600 /opt/etc/wg-monitor/config.yaml")
	PrintOK("/opt/etc/wg-monitor/config.yaml")

	PrintStep(5, 7, "init.d скрипт")
	initd, err := ReadStaticTemplate("S99wg-monitor")
	if err != nil {
		return err
	}
	if err := s.UploadStdin("/opt/etc/init.d/S99wg-monitor", initd); err != nil {
		return err
	}
	s.MustRun("chmod +x /opt/etc/init.d/S99wg-monitor")
	PrintOK("/opt/etc/init.d/S99wg-monitor")

	PrintStep(6, 7, "Скачать агент бинарь")
	assetName := "wg-monitor-agent-linux-" + arch
	localPath, err := stepDownloadAsset(dl, rel, assetName)
	if err != nil {
		return err
	}

	PrintStep(7, 7, "Upload + start")
	if err := stepUploadAgentBinary(s, localPath, "/opt/bin/wg-monitor"); err != nil {
		return err
	}

	ag.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	ag.LastDeployedVersion = rel.TagName
	return nil
}
```

- [ ] **Step 2: Подключить в `main.go`**

```go
case "install-agent":
    nick := ""
    for i := 1; i < len(args); i++ {
        if args[i] == "--agent" && i+1 < len(args) {
            nick = args[i+1]
        }
    }
    if err := actionInstallAgent(state, secrets, dl, nick); err != nil {
        os.Exit(1)
    }
    SaveState(statePath, state)
    PrintSecretsSaveAdvice(secrets)
```

- [ ] **Step 3: Smoke**

```bash
./bin/wg-monitor-deploy install-agent --agent testkeen
```

- [ ] **Step 4: Commit**

```bash
git add cmd/deploy/actions.go cmd/deploy/main.go
git commit -m "feat(deploy): install-agent action"
```

### Task 10c: `add-router`

- [ ] **Step 1: Реализовать `actionAddRouter`**

Добавить в `actions.go`:

```go
func actionAddRouter(state *State, secrets *SecretStore, dl *Downloader) error {
	if state.Backend.Host == "" {
		PrintFail("сначала install-backend (нужно куда добавлять)")
		return fmt.Errorf("no backend")
	}

	nick := Ask("Никнейм нового роутера (a-z0-9_-, уникальный)", "")
	if nick == "" {
		return fmt.Errorf("nickname required")
	}
	if state.FindAgent(nick) != nil {
		PrintFail("такой никнейм уже есть в wizard.toml")
		return fmt.Errorf("duplicate nickname")
	}

	// 1. Сгенерировать токен.
	tok := randomHexToken(32)
	PrintWarn(fmt.Sprintf("сгенерирован токен для %s — сохрани в WG_AGENT_TOKEN_%s",
		nick, strings.ToUpper(nick)))
	fmt.Println("    " + tok)

	// 2. Добавить в backend.yaml на VPS.
	PrintStep(1, 3, "Обновить backend.yaml на VPS")
	pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
	if pass == "" {
		return fmt.Errorf("missing VPS password")
	}
	kh, _ := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	bs, err := ConnectSSH(state.Backend.Host, state.Backend.Port, state.Backend.User, pass, kh)
	if err != nil {
		return err
	}
	defer bs.Close()

	threadID := parseIntOr(Ask("Telegram thread_id для нового топика", ""), 0)

	// Добавить в state и регенерить backend.yaml
	state.Agents = append(state.Agents, AgentState{
		Nickname: nick,
		ThreadID: threadID,
	})

	// Все токены: для уже существующих — из env, для нового — только что сгенерированный
	var entries []AgentEntry
	for _, a := range state.Agents {
		envName := "WG_AGENT_TOKEN_" + strings.ToUpper(a.Nickname)
		t := os.Getenv(envName)
		if a.Nickname == nick {
			t = tok
		}
		if t == "" {
			PrintFail(fmt.Sprintf("токен для %s неизвестен (нет в %s). Прервать?", a.Nickname, envName))
			return fmt.Errorf("missing token for %s", a.Nickname)
		}
		entries = append(entries, AgentEntry{
			Nickname: a.Nickname,
			Token:    t,
			ThreadID: a.ThreadID,
		})
	}

	// Bot token берём заново — т.к. не в state
	botToken, _ := secrets.Get("WG_BOT_TOKEN", "Telegram bot token", nil)
	yaml, err := RenderBackendYAML(BackendParams{
		BotToken:    botToken,
		ChatID:      state.Telegram.ChatID,
		AdminUserID: state.Telegram.AdminUserID,
		Agents:      entries,
	})
	if err != nil {
		return err
	}
	if err := stepUploadFile(bs, "/etc/wg-monitor/backend.yaml", yaml, "600"); err != nil {
		return err
	}

	PrintStep(2, 3, "Перезапустить бэкенд")
	if _, err := bs.MustRun("systemctl restart wg-monitor-backend"); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	out, _ := bs.MustRun("systemctl is-active wg-monitor-backend")
	if strings.TrimSpace(out) != "active" {
		jr, _ := bs.MustRun("journalctl -u wg-monitor-backend -n 30 --no-pager")
		PrintFail("бэкенд не active после restart:\n" + jr)
		return fmt.Errorf("backend not active")
	}
	PrintOK("бэкенд перезапущен")

	PrintStep(3, 3, "Установить агента на новый роутер")
	// Сохранить токен в env для текущего процесса, чтобы install-agent его подхватил.
	os.Setenv("WG_AGENT_TOKEN_"+strings.ToUpper(nick), tok)
	return actionInstallAgent(state, secrets, dl, nick)
}
```

- [ ] **Step 2: Подключить `add-router` в main.go**

```go
case "add-router":
    if err := actionAddRouter(state, secrets, dl); err != nil {
        os.Exit(1)
    }
    SaveState(statePath, state)
    PrintSecretsSaveAdvice(secrets)
```

- [ ] **Step 3: Commit**

```bash
git add cmd/deploy/actions.go cmd/deploy/main.go
git commit -m "feat(deploy): add-router action — generate token + update backend + install agent"
```

---

## Task 11: `status` action — read-only сводка

**Files:**
- Modify: `cmd/deploy/actions.go`
- Modify: `cmd/deploy/main.go`

- [ ] **Step 1: Реализовать `actionStatus`**

```go
func actionStatus(state *State, secrets *SecretStore) error {
	if state.Backend.Host == "" && len(state.Agents) == 0 {
		PrintFail("wizard.toml пустой — нечего проверять")
		return fmt.Errorf("nothing to check")
	}

	kh, _ := NewKnownHosts(defaultCacheDir() + "/known_hosts")

	if state.Backend.Host != "" {
		fmt.Println(Colorize("=== Backend ===", ColorBold))
		pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
		if pass == "" {
			PrintWarn("WG_VPS_PASS не задан — пропускаю VPS")
		} else {
			s, err := ConnectSSH(state.Backend.Host, state.Backend.Port, state.Backend.User, pass, kh)
			if err != nil {
				PrintFail(err.Error())
			} else {
				out, _ := s.MustRun("systemctl is-active wg-monitor-backend")
				PrintInfo("systemctl: " + strings.TrimSpace(out))
				if state.Backend.Domain != "" {
					stepVerifyHTTP(s, "https://"+state.Backend.Domain+"/health")
				}
				vout, _ := s.MustRun("/usr/local/bin/wg-monitor-backend --version 2>&1 || true")
				PrintInfo("version: " + strings.TrimSpace(vout))
				s.Close()
			}
		}
		fmt.Println()
	}

	for _, ag := range state.Agents {
		fmt.Println(Colorize("=== Agent: "+ag.Nickname+" ===", ColorBold))
		envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
		memFile := os.ExpandEnv("$HOME/.claude/projects/c--Users-Anex-Projects-wg-monitor/memory/host_keenetic.md")
		pass, _ := secrets.Get(envName, "пароль root для "+ag.Nickname, &MemoryFileLookup{
			Path:    memFile,
			Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
		})
		if pass == "" {
			pass, _ = secrets.Get("WG_KEENETIC_PASS", "пароль root", nil)
		}
		if pass == "" {
			PrintWarn("пароль не задан — пропускаю")
			continue
		}

		s, err := ConnectSSH(ag.Host, portOrDefault(ag.Port, 222), userOrDefault(ag.User, "root"), pass, kh)
		if err != nil {
			PrintFail(err.Error())
			continue
		}
		out, _ := s.MustRun("pidof wg-monitor")
		if strings.TrimSpace(out) != "" {
			PrintOK("running (PID " + strings.TrimSpace(out) + ")")
		} else {
			PrintFail("не запущен")
		}
		vout, _ := s.MustRun("/opt/bin/wg-monitor --version 2>&1 || true")
		PrintInfo("version: " + strings.TrimSpace(vout))
		s.Close()
		fmt.Println()
	}
	return nil
}
```

- [ ] **Step 2: Подключить в main.go**

```go
case "status":
    if err := actionStatus(state, secrets); err != nil {
        os.Exit(1)
    }
```

- [ ] **Step 3: Smoke + commit**

```bash
./bin/wg-monitor-deploy status
```

```bash
git add cmd/deploy/actions.go cmd/deploy/main.go
git commit -m "feat(deploy): status action — read-only summary of VPS + agents"
```

---

## Task 12: Меню — `menu.go`

**Files:**
- Create: `cmd/deploy/menu.go`
- Modify: `cmd/deploy/main.go`

- [ ] **Step 1: Реализовать `cmd/deploy/menu.go`**

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func RunMenu(state *State, statePath string, secrets *SecretStore, dl *Downloader) {
	for {
		printMenuHeader(state)
		printMenuItems(state)
		fmt.Print("> ")
		var line string
		fmt.Fscanln(os.Stdin, &line)
		line = strings.TrimSpace(strings.ToUpper(line))

		switch line {
		case "1":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionInstallBackend(state, secrets, dl)
			})
		case "2":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionUpdateBackend(state, secrets, dl)
			})
		case "3":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionInstallAgent(state, secrets, dl, "")
			})
		case "4":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionUpdateAgent(state, secrets, dl, "")
			})
		case "5":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionAddRouter(state, secrets, dl)
			})
		case "6":
			actionStatus(state, secrets)
		case "7":
			openInEditor(statePath)
			// Reload after edit.
			if reloaded, err := LoadState(statePath); err == nil {
				*state = *reloaded
			}
		case "Q", "":
			return
		default:
			PrintFail("Не понял. Введи 1–7 или Q.")
		}
		fmt.Println()
		Ask("[Enter] чтобы вернуться в меню", "")
	}
}

func printMenuHeader(state *State) {
	fmt.Println()
	fmt.Println(Colorize("╔═══════════════════════════════════════════════════════════╗", ColorCyan))
	fmt.Printf("%s wg-monitor deploy wizard         %-15s%s\n",
		Colorize("║", ColorCyan), Version, Colorize("║", ColorCyan))
	fmt.Println(Colorize("╠═══════════════════════════════════════════════════════════╣", ColorCyan))
	if state.Backend.Host != "" {
		fmt.Printf("%s VPS:    %s  %-30s%s\n",
			Colorize("║", ColorCyan), state.Backend.Host, state.Backend.Domain, Colorize("║", ColorCyan))
	}
	for _, a := range state.Agents {
		fmt.Printf("%s Router: %s:%d (%s)%-20s%s\n",
			Colorize("║", ColorCyan), a.Host, a.Port, a.Nickname, "", Colorize("║", ColorCyan))
	}
	fmt.Println(Colorize("╚═══════════════════════════════════════════════════════════╝", ColorCyan))
}

func printMenuItems(state *State) {
	fmt.Println()
	fmt.Println("  [1] Первичная установка бэкенда на VPS")
	if state.Backend.Host != "" {
		fmt.Printf("  [2] Обновить бэкенд на VPS  %s\n",
			Colorize("(last: "+state.Backend.LastDeploy+")", ColorDim))
	} else {
		fmt.Println("  [2] Обновить бэкенд на VPS  " + Colorize("(сначала установи)", ColorDim))
	}
	fmt.Println("  [3] Первичная установка агента на роутер")
	if len(state.Agents) > 0 {
		fmt.Printf("  [4] Обновить агента на роутере  %s\n",
			Colorize("(last: "+state.Agents[0].LastDeploy+")", ColorDim))
	} else {
		fmt.Println("  [4] Обновить агента на роутере  " + Colorize("(сначала установи)", ColorDim))
	}
	fmt.Println("  [5] Добавить новый роутер")
	fmt.Println("  [6] Проверить статус")
	fmt.Println("  [7] Открыть wizard.toml в редакторе")
	fmt.Println("  [Q] Выход")
	fmt.Println()
}

func runActionAndSave(state *State, statePath string, secrets *SecretStore, fn func() error) {
	if err := fn(); err != nil {
		PrintFail("action failed: " + err.Error())
	}
	if err := SaveState(statePath, state); err != nil {
		PrintFail("save state: " + err.Error())
	}
	PrintSecretsSaveAdvice(secrets)
}

func openInEditor(path string) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "nano"
		}
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}
```

- [ ] **Step 2: Подключить меню в `main.go`**

Заменить блок `if len(args) == 0`:

```go
if len(args) == 0 {
    RunMenu(state, statePath, secrets, dl)
    return
}
```

- [ ] **Step 3: Smoke**

```bash
./bin/wg-monitor-deploy
```

Глазами: меню рендерится, цифры работают, Q выходит.

- [ ] **Step 4: Commit**

```bash
git add cmd/deploy/menu.go cmd/deploy/main.go
git commit -m "feat(deploy): interactive menu"
```

---

## Task 13: CI workflow — `.github/workflows/release.yml`

**Files:**
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Создать workflow**

```yaml
name: Release

on:
  push:
    tags:
      - 'v*'

jobs:
  build:
    strategy:
      matrix:
        include:
          # Wizard binaries
          - { os: ubuntu-latest, goos: linux,   goarch: amd64, name: wg-monitor-deploy-linux-amd64,   pkg: ./cmd/deploy }
          - { os: ubuntu-latest, goos: windows, goarch: amd64, name: wg-monitor-deploy-windows-amd64.exe, pkg: ./cmd/deploy }
          - { os: ubuntu-latest, goos: darwin,  goarch: amd64, name: wg-monitor-deploy-darwin-amd64,  pkg: ./cmd/deploy }
          - { os: ubuntu-latest, goos: darwin,  goarch: arm64, name: wg-monitor-deploy-darwin-arm64,  pkg: ./cmd/deploy }
          # Backend
          - { os: ubuntu-latest, goos: linux, goarch: amd64, name: wg-monitor-backend-linux-amd64, pkg: ./cmd/backend }
          # Agent (UPX-packed)
          - { os: ubuntu-latest, goos: linux, goarch: arm64,  name: wg-monitor-agent-linux-arm64,  pkg: ./cmd/agent, upx: true }
          - { os: ubuntu-latest, goos: linux, goarch: mipsle, gomips: softfloat, name: wg-monitor-agent-linux-mipsle, pkg: ./cmd/agent, upx: true }
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'

      - name: Install UPX
        if: matrix.upx
        run: sudo apt-get update && sudo apt-get install -y upx-ucl

      - name: Build
        env:
          GOOS:    ${{ matrix.goos }}
          GOARCH:  ${{ matrix.goarch }}
          GOMIPS:  ${{ matrix.gomips }}
          CGO_ENABLED: 0
        run: |
          mkdir -p dist
          go build -trimpath \
            -ldflags "-s -w -X main.Version=${{ github.ref_name }}" \
            -o dist/${{ matrix.name }} ${{ matrix.pkg }}

      - name: UPX pack
        if: matrix.upx
        run: upx --best --lzma dist/${{ matrix.name }}

      - uses: actions/upload-artifact@v4
        with:
          name: ${{ matrix.name }}
          path: dist/${{ matrix.name }}
          if-no-files-found: error

  release:
    needs: build
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/download-artifact@v4
        with:
          path: dist
          merge-multiple: true

      - name: Generate checksums
        run: |
          cd dist
          sha256sum * > checksums.txt
          cat checksums.txt

      - name: Create Release
        uses: softprops/action-gh-release@v2
        with:
          files: dist/*
          generate_release_notes: true
          fail_on_unmatched_files: true
```

- [ ] **Step 2: Push тестовый тег**

```bash
git add .github/workflows/release.yml
git commit -m "ci: release workflow — build 7 artifacts on tag push"
git tag v0.9.0-rc1
git push origin main v0.9.0-rc1
```

- [ ] **Step 3: Проверить Actions**

Открыть `https://github.com/anex/wg-monitor/actions` — все matrix-jobs должны быть зелёные. Открыть Release — там должны висеть 7 артефактов + `checksums.txt`.

Если RC сломан:
- удалить релиз вручную через UI
- удалить тег: `git tag -d v0.9.0-rc1 && git push origin :v0.9.0-rc1`
- починить workflow, попробовать снова с `v0.9.0-rc2`

- [ ] **Step 4: End-to-end smoke с настоящего релиза**

Когда RC зелёный:
```bash
make build-deploy
./bin/wg-monitor-deploy update-backend
```
Wizard должен сходить в GitHub API, увидеть `v0.9.0-rc1`, скачать, развернуть. Если всё ок — ставим финальный тег `v0.9.0`.

---

## Task 14: Cleanup — удалить старые скрипты, переписать DEPLOY.md

**Files:**
- Delete: `deploy/agent/deploy_keenetic.py`
- Delete: `deploy/agent/deploy_keenetic_binonly.py`
- Delete: `deploy/agent/requirements.txt`
- Delete: `deploy/agent/config.yaml.example`
- Delete: `deploy/agent/S99wg-monitor`
- Delete: `deploy/backend/deploy_vps_main.py`
- Delete: `deploy/backend/deploy_cli.py`
- Delete: `deploy/backend/backend.yaml.example`
- Delete: `deploy/backend/Caddyfile`
- Delete: `deploy/backend/wg-monitor-backend.service`
- Modify: `DEPLOY.md`
- Modify: `README.md`
- Modify: `.gitignore`

- [ ] **Step 1: Удалить старые скрипты и шаблоны**

```bash
git rm deploy/agent/deploy_keenetic.py \
       deploy/agent/deploy_keenetic_binonly.py \
       deploy/agent/requirements.txt \
       deploy/agent/config.yaml.example \
       deploy/agent/S99wg-monitor \
       deploy/backend/deploy_vps_main.py \
       deploy/backend/deploy_cli.py \
       deploy/backend/backend.yaml.example \
       deploy/backend/Caddyfile \
       deploy/backend/wg-monitor-backend.service
```

(`deploy/agent/configs/` и `deploy/diag/` оставить — это другое.)

- [ ] **Step 2: Переписать `DEPLOY.md`**

Заменить содержимое на:

````markdown
# Деплой wg-monitor

Полный деплой делает один интерактивный wizard — `wg-monitor-deploy`. Никаких ручных команд по SSH, никакого Python.

## Что нужно заранее (вручную)

| Компонент | Как получить |
| --- | --- |
| **VPS** | Linux amd64, минимум 256 МБ RAM, открытый порт 443. Любой провайдер. |
| **Домен** | Свой домен или DuckDNS, A-запись на IP VPS |
| **Telegram-бот** | Создать через [@BotFather](https://t.me/BotFather), сохранить токен |
| **Telegram-группа** | Супергруппа с топиками, бот добавлен админом, для каждого роутера — свой топик |
| **Telegram IDs** | chat_id, message_thread_id (через `getUpdates`), ваш user_id (через [@userinfobot](https://t.me/userinfobot)) |
| **Роутер Keenetic** | OS 4/5, [Entware](https://docs.keenetic.com/...) и [awg-manager](https://github.com/hoaxisr/awg-manager) 2.8+ установлены |

## Шаги

1. Скачай `wg-monitor-deploy` под свою OS из [Releases](https://github.com/anex/wg-monitor/releases/latest):
   - Windows: `wg-monitor-deploy-windows-amd64.exe`
   - macOS Apple Silicon: `wg-monitor-deploy-darwin-arm64`
   - macOS Intel: `wg-monitor-deploy-darwin-amd64`
   - Linux: `wg-monitor-deploy-linux-amd64`

2. **macOS** — снять Gatekeeper-карантин:
   ```bash
   xattr -d com.apple.quarantine wg-monitor-deploy-darwin-arm64
   chmod +x wg-monitor-deploy-darwin-arm64
   ```
   **Linux:** `chmod +x wg-monitor-deploy-linux-amd64`

3. Запусти:
   ```bash
   ./wg-monitor-deploy
   ```
   (или двойной клик на Windows)

4. Выбери в меню `[1] Первичная установка бэкенда`. Wizard проведёт через 12 шагов: спросит домен, токен бота, IDs, пароль root для VPS — и всё развернёт.

5. После бэкенда — `[3] Первичная установка агента`. Введи host роутера, его никнейм, awg-iface (`awg0` обычно). Wizard сам определит архитектуру (arm64/mipsle), скачает нужный бинарь и установит как Entware-сервис.

## Обновление

```bash
./wg-monitor-deploy update-backend     # без интерактива, по wizard.toml
./wg-monitor-deploy update-agent
```

Wizard скачивает свежие бинари из последнего GitHub Release.

## Файлы

- `wizard.toml` — конфиг wizard'а (хосты, домен, никнеймы). Не содержит секретов. По умолчанию: `~/.config/wg-monitor-deploy/wizard.toml` (Linux/macOS) или `%APPDATA%\wg-monitor-deploy\wizard.toml` (Windows).
- Секреты — через env vars: `WG_VPS_PASS`, `WG_KEENETIC_PASS_<NICKNAME>`, `WG_BOT_TOKEN`. Wizard напомнит после первого ввода.

## Подкоманды

```
wg-monitor-deploy                    # меню
wg-monitor-deploy install-backend    # без меню
wg-monitor-deploy update-backend
wg-monitor-deploy install-agent [--agent <nickname>]
wg-monitor-deploy update-agent  [--agent <nickname>]
wg-monitor-deploy add-router
wg-monitor-deploy status
wg-monitor-deploy --version
wg-monitor-deploy --no-color
wg-monitor-deploy --config <path>
```
````

- [ ] **Step 3: Обновить `README.md`**

Найти разделы со ссылками на `deploy/agent/deploy_keenetic.py`, `python deploy/...` и т.д., заменить на ссылку на `wg-monitor-deploy` и `DEPLOY.md`.

```bash
grep -n "deploy_keenetic\|deploy_vps_main\|deploy/agent/deploy\|deploy/backend/deploy\|paramiko" README.md
```
Каждое упоминание — заменить на cookie-cutter:

```markdown
Деплой делается интерактивным wizard'ом. Скачай `wg-monitor-deploy` из [Releases](https://github.com/anex/wg-monitor/releases/latest), запусти и следуй инструкциям. Подробнее в [DEPLOY.md](DEPLOY.md).
```

- [ ] **Step 4: Обновить `.gitignore`**

Добавить:
```
# Deploy wizard local config (contains hosts; not secrets, but local-only)
wizard.toml
cmd/deploy/wizard.toml

# Locally built deploy binaries
dist/wg-monitor-deploy*
bin/wg-monitor-deploy*
```

- [ ] **Step 5: Финальный smoke**

Полный прогон: на чистой машине (или контейнере) скачать `wg-monitor-deploy-linux-amd64` из Releases, запустить против тестовых VPS+роутера, пройти меню. Никаких сообщений об отсутствии Python, paramiko, Go.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "chore(deploy): retire Python scripts, simplify DEPLOY.md to wizard-only"
```

- [ ] **Step 7: Финальный тег**

После того как `v0.9.0-rcN` зелёный и smoke прошёл:
```bash
git tag v0.9.0
git push origin main v0.9.0
```

CI соберёт финальный релиз.

---

## Self-Review (заметки автора плана)

**Spec coverage:**
- ✅ §3 Architecture → Tasks 1-12 покрывают все компоненты
- ✅ §4 Distribution → Task 13 (CI workflow + 7 артефактов)
- ✅ §5 UX → Tasks 8-12 (steps + actions + menu)
- ✅ §6 State+Secrets → Tasks 3, 4
- ✅ §7 Templates → Task 5
- ✅ §8.1 Logging → НЕ покрыт явно — добавлено бы как минор после Task 12. Решение: оставить на minor follow-up, не блокирует первый release.
- ✅ §8.4 Versioning → Tasks 1, 13 (Version ldflag + tag-driven CI)
- ✅ §8.5 TOFU known_hosts → Task 7
- ✅ §9 Migration → Task 14

**Type consistency:** проверено — структуры `BackendParams`, `AgentParams`, `CaddyParams`, `State`, `BackendState`, `AgentState` определены в Task 5 / Task 3 и единообразно используются в Tasks 8-12.

**Placeholder scan:** один намеренный TODO — §8.1 logging — явно вынесен как follow-up. Остальное полностью прописано.

**Scope:** план большой (14 milestone'ов), но работа линейна, каждая задача даёт работающий артефакт. Можно реализовывать инкрементально — после Task 8 уже есть рабочий `update-backend`, заменяющий `deploy_vps_main.py`. Удаление старых скриптов — финальный шаг.
