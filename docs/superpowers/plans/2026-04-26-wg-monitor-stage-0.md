# wg-monitor Stage 0 (Bootstrapping) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Working end-to-end pipe `Keenetic agent → HTTPS → Caddy → Go backend → stdout` with cross-compiled UPX-packed agent under 3 MB. Sets up infrastructure for Stage 1 (4 checks + state machine + TG bot).

**Architecture:** Two Go binaries (`wg-monitor` agent, `wg-monitor-backend` backend) sharing a `pkg/wire` types package for wire format. Stage 0 deliberately avoids SQLite/bcrypt/Telegram — auth is plain string-compare against tokens in YAML, "checks" is a single hard-coded `agent_heartbeat=ok`. Caddy on VPS Main `:443` terminates TLS via TLS-ALPN-01 and reverse-proxies to backend on `127.0.0.1:8080`. No cgo anywhere — cross-compile is `GOOS=linux GOARCH=mipsle GOMIPS=softfloat`.

**Tech Stack:** Go 1.22+, `net/http` stdlib, `gopkg.in/yaml.v3`, Caddy 2 (apt), systemd, Entware init.d. UPX for binary compression. No external Go dependencies beyond YAML.

---

## Spec references

- Source spec: `docs/superpowers/specs/2026-04-25-wg-monitor-design.md` (status: Approved)
- Resolved values used here:
  - Subdomain: `wgmonitor.jkaotlic.duckdns.org`
  - Bot token path: `/root/wgmon-secrets/bot-token.txt` (NOT used in Stage 0 — referenced only in spec)
  - Group chat_id: `-1003651873378` (NOT used in Stage 0)
  - Admin user_id: `136513775` (NOT used in Stage 0)
  - Nickname regexp: `^[a-z][a-z0-9_-]{1,15}$` (validated in Stage 0 config parsing)
  - `awg_iface` + `expected_exit_ip`: present in agent config but unused in Stage 0 (no checks yet)

## What is intentionally NOT in Stage 0

- SQLite database, `users`/`events`/`incident_state`/`pending_commands` tables — Stage 1
- bcrypt token hashing — Stage 1 (auth in Stage 0 = plain string compare to tokens listed in `backend.yaml`)
- 4 real checks (`awg_handshake`, `awg_routing`, `awg_marker`, `dns_doh`) — Stage 1
- Telegram bot, topics, callback handlers — Stage 1
- `install.sh` bootstrap one-liner — Stage 1 (Stage 0 uses manual `scp` deploy)
- `wg-monitor-cli` (`add-user` etc.) — Stage 1
- Long-poll `/v1/cmd` and command handlers — Stage 3
- Agent self-update — Stage 5

---

## File Structure

```
wg-monitor/
├── .gitignore
├── go.mod
├── go.sum
├── Makefile
├── README.md
├── cmd/
│   ├── agent/
│   │   └── main.go                          # agent entrypoint
│   └── backend/
│       └── main.go                          # backend entrypoint
├── pkg/
│   └── wire/
│       └── types.go                         # shared Report/Check structs (JSON wire format)
├── internal/
│   ├── agent/
│   │   ├── config.go                        # YAML parse + validate
│   │   ├── config_test.go
│   │   ├── client.go                        # HTTP client w/ bearer + 1 retry
│   │   ├── client_test.go
│   │   ├── reporter.go                      # heartbeat loop
│   │   └── reporter_test.go
│   └── backend/
│       ├── config.go                        # YAML parse + agent token map
│       ├── config_test.go
│       ├── auth.go                          # bearer-token middleware (string compare)
│       ├── auth_test.go
│       ├── handler.go                       # /v1/report + /healthz handlers
│       └── handler_test.go
├── deploy/
│   ├── backend/
│   │   ├── Caddyfile                        # site block for wgmonitor.jkaotlic.duckdns.org
│   │   ├── wg-monitor-backend.service       # systemd unit
│   │   └── backend.yaml.example             # sample config (NOT committed token)
│   └── agent/
│       ├── S99wg-monitor                    # Entware init.d script
│       └── config.yaml.example              # sample agent config
└── docs/
    └── superpowers/
        ├── specs/2026-04-25-wg-monitor-design.md  (existing)
        └── plans/2026-04-26-wg-monitor-stage-0.md (this file)
```

**File responsibilities:**
- `pkg/wire/types.go` — single source of truth for JSON wire format. Used by both agent (encode) and backend (decode). Tests are exhaustive on this since drift here breaks everything.
- `internal/agent/*` — agent-only code, never imported by backend.
- `internal/backend/*` — backend-only code, never imported by agent.
- `cmd/*/main.go` — thin entrypoint, wires up config + components, no business logic.
- `deploy/*` — non-Go artifacts (systemd, init.d, Caddy, sample configs). Hand-edited, not generated.

---

## Prerequisites (one-time, before Task 1)

- Go 1.22+ installed locally (`go version`)
- `git` configured with `user.email = asnekhaev@gmail.com` (per memory `feedback_git_global_email_placeholder`)
- UPX 4.x in PATH locally (Windows: `choco install upx` or download from upx.github.io)
- SSH access to `vps-main` alias works (`ssh vps-main 'echo ok'` returns `ok`)
- SSH access to one test Keenetic router (architecture mipsel preferred) with Entware mounted at `/opt`. Note its hostname/IP — referred to as `<TEST_KEEN>` in tasks below.

---

## Task 1: Initialize Go module + repo skeleton

**Files:**
- Create: `.gitignore`
- Create: `go.mod`
- Create: `README.md`

- [ ] **Step 1: Set local git email (per `feedback_git_global_email_placeholder`)**

```bash
cd /c/Users/Anex/Projects/wg-monitor
git config user.email asnekhaev@gmail.com
git config user.name "Anatoly Nekhaev"
```

- [ ] **Step 2: Create `.gitignore`**

```
# Binaries
/bin/
*.exe
*.upx
wg-monitor
wg-monitor-backend

# Go test cache
*.test
*.out
coverage.txt

# Local config (never commit real tokens)
*.local.yaml
backend.yaml
config.yaml

# Editor
.idea/
.vscode/
*.swp
.DS_Store
```

- [ ] **Step 3: Initialize Go module**

```bash
go mod init github.com/anex/wg-monitor
```

Expected: creates `go.mod` with `module github.com/anex/wg-monitor` and `go 1.22` (or whatever local version).

- [ ] **Step 4: Add YAML dependency**

```bash
go get gopkg.in/yaml.v3@v3.0.1
```

Expected: `go.mod` lists `gopkg.in/yaml.v3 v3.0.1`, `go.sum` populated.

- [ ] **Step 5: Create minimal `README.md`**

```markdown
# wg-monitor

Telegram-fronted monitoring bot for an AmneziaWG fleet (~10 Keenetic routers with Entware).

- Agent (Go, mipsel/aarch64) on each router pushes per-minute reports.
- Backend (Go) on VPS Main behind Caddy at `https://wgmonitor.jkaotlic.duckdns.org/`.

See `docs/superpowers/specs/2026-04-25-wg-monitor-design.md` for the approved design.

## Build

```
make build-host        # local OS, for tests/dev
make build-mipsel      # Keenetic with MIPS little-endian softfloat
make build-aarch64     # Keenetic with ARM64
make pack              # UPX --best on cross-compiled binaries
```

## Stage status

- Stage 0 (bootstrapping): in progress
```

- [ ] **Step 6: Initial commit**

```bash
git add .gitignore go.mod go.sum README.md
git commit -m "chore: initialize Go module and repo skeleton"
```

---

## Task 2: Define wire format (`pkg/wire/types.go`)

**Files:**
- Create: `pkg/wire/types.go`
- Create: `pkg/wire/types_test.go`

- [ ] **Step 1: Write failing test for `Report` JSON round-trip**

Create `pkg/wire/types_test.go`:

```go
package wire

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReport_JSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	r := Report{
		Timestamp:    ts,
		AgentVersion: "0.1.0",
		Checks: []Check{
			{Name: "agent_heartbeat", Status: "ok", DurationMs: 1, Details: map[string]any{}},
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("ts: got %v want %v", got.Timestamp, ts)
	}
	if got.AgentVersion != "0.1.0" {
		t.Errorf("agent_version: got %q", got.AgentVersion)
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "agent_heartbeat" || got.Checks[0].Status != "ok" {
		t.Errorf("checks roundtrip mismatch: %+v", got.Checks)
	}
}

func TestReport_JSONFieldNames(t *testing.T) {
	r := Report{
		Timestamp:    time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		AgentVersion: "0.1.0",
		Checks:       []Check{{Name: "agent_heartbeat", Status: "ok", DurationMs: 1}},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"ts"`, `"agent_version"`, `"checks"`, `"name"`, `"status"`, `"duration_ms"`} {
		if !contains(s, want) {
			t.Errorf("expected JSON to contain %s, got: %s", want, s)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run, verify it fails**

```bash
go test ./pkg/wire/...
```

Expected: FAIL with `undefined: Report` and `undefined: Check`.

- [ ] **Step 3: Implement `pkg/wire/types.go`**

```go
// Package wire defines the JSON wire format shared by agent and backend.
// Field tags here are the contract — changing them is a breaking change.
package wire

import "time"

type Report struct {
	Timestamp    time.Time `json:"ts"`
	AgentVersion string    `json:"agent_version"`
	Checks       []Check   `json:"checks"`
}

type Check struct {
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	DurationMs int64          `json:"duration_ms"`
	Details    map[string]any `json:"details,omitempty"`
}
```

- [ ] **Step 4: Run, verify pass**

```bash
go test ./pkg/wire/... -v
```

Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/wire/
git commit -m "feat(wire): add Report and Check wire-format types"
```

---

## Task 3: Backend config parser (`internal/backend/config.go`)

**Files:**
- Create: `internal/backend/config.go`
- Create: `internal/backend/config_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/backend/config_test.go`:

```go
package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.yaml")
	body := `
listen: 127.0.0.1:8080
log_level: info
agents:
  - nickname: testkeen
    token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8080" {
		t.Errorf("listen: %q", cfg.Listen)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Nickname != "testkeen" {
		t.Errorf("agents: %+v", cfg.Agents)
	}
	if cfg.Agents[0].Token != "deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe" {
		t.Errorf("token mismatch")
	}
}

func TestLoadConfig_RejectsBadNickname(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.yaml")
	body := `
listen: 127.0.0.1:8080
agents:
  - nickname: "Bad Name!"
    token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on bad nickname, got nil")
	}
}

func TestLoadConfig_RejectsShortToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.yaml")
	body := `
listen: 127.0.0.1:8080
agents:
  - nickname: testkeen
    token: short
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on short token, got nil")
	}
}

func TestLoadConfig_RejectsDuplicateNickname(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.yaml")
	body := `
listen: 127.0.0.1:8080
agents:
  - nickname: dup
    token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
  - nickname: dup
    token: cafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeef
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on duplicate nickname, got nil")
	}
}
```

- [ ] **Step 2: Run, verify FAIL**

```bash
go test ./internal/backend/...
```

Expected: FAIL `undefined: LoadConfig`.

- [ ] **Step 3: Implement `internal/backend/config.go`**

```go
package backend

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

var nicknameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

type Config struct {
	Listen   string        `yaml:"listen"`
	LogLevel string        `yaml:"log_level"`
	Agents   []AgentConfig `yaml:"agents"`
}

type AgentConfig struct {
	Nickname string `yaml:"nickname"`
	Token    string `yaml:"token"`
}

func LoadConfig(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8080"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	seen := make(map[string]struct{}, len(cfg.Agents))
	for i, a := range cfg.Agents {
		if !nicknameRegexp.MatchString(a.Nickname) {
			return nil, fmt.Errorf("agents[%d]: nickname %q must match %s", i, a.Nickname, nicknameRegexp)
		}
		if len(a.Token) < 32 {
			return nil, fmt.Errorf("agents[%d] %s: token must be at least 32 chars", i, a.Nickname)
		}
		if _, dup := seen[a.Nickname]; dup {
			return nil, fmt.Errorf("agents[%d]: duplicate nickname %q", i, a.Nickname)
		}
		seen[a.Nickname] = struct{}{}
	}
	return &cfg, nil
}
```

- [ ] **Step 4: Run, verify PASS**

```bash
go test ./internal/backend/... -v -run TestLoadConfig
```

Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/config.go internal/backend/config_test.go
git commit -m "feat(backend): YAML config parser with nickname/token validation"
```

---

## Task 4: Backend bearer-token auth middleware (`internal/backend/auth.go`)

**Files:**
- Create: `internal/backend/auth.go`
- Create: `internal/backend/auth_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/backend/auth_test.go`:

```go
package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddleware_RejectsMissingHeader(t *testing.T) {
	tokens := map[string]string{"deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe": "testkeen"}
	mw := AuthMiddleware(tokens)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("POST", "/v1/report", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code: got %d want 401", rec.Code)
	}
	if called {
		t.Error("inner handler must not be called on bad auth")
	}
}

func TestAuthMiddleware_RejectsBadToken(t *testing.T) {
	tokens := map[string]string{"deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe": "testkeen"}
	mw := AuthMiddleware(tokens)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("POST", "/v1/report", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code: got %d want 401", rec.Code)
	}
}

func TestAuthMiddleware_AcceptsValidToken_AttachesNickname(t *testing.T) {
	const token = "deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe"
	tokens := map[string]string{token: "testkeen"}
	mw := AuthMiddleware(tokens)
	var gotNick string
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNick = NicknameFromContext(r.Context())
	}))
	req := httptest.NewRequest("POST", "/v1/report", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("code: got %d want 200", rec.Code)
	}
	if gotNick != "testkeen" {
		t.Errorf("nickname: got %q want testkeen", gotNick)
	}
}

func TestAuthMiddleware_RejectsMalformedHeader(t *testing.T) {
	const token = "deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe"
	tokens := map[string]string{token: "testkeen"}
	mw := AuthMiddleware(tokens)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	cases := []string{"Bearer", "", "Basic " + token, "Bearer  " + token, "bearer " + token}
	for _, hdr := range cases {
		req := httptest.NewRequest("POST", "/v1/report", nil)
		req.Header.Set("Authorization", hdr)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("hdr=%q: code %d want 401", hdr, rec.Code)
		}
	}
}
```

- [ ] **Step 2: Run, verify FAIL**

```bash
go test ./internal/backend/... -run TestAuthMiddleware
```

Expected: FAIL `undefined: AuthMiddleware` and `undefined: NicknameFromContext`.

- [ ] **Step 3: Implement `internal/backend/auth.go`**

```go
package backend

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

type ctxKey int

const ctxKeyNickname ctxKey = iota

// AuthMiddleware returns middleware that requires `Authorization: Bearer <token>`.
// On match, the matched nickname is attached to the request context.
// Token comparison uses subtle.ConstantTimeCompare (timing-safe).
func AuthMiddleware(tokenToNickname map[string]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(hdr, prefix) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			presented := strings.TrimPrefix(hdr, prefix)
			if presented == "" || strings.HasPrefix(presented, " ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			for token, nick := range tokenToNickname {
				if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
					ctx := context.WithValue(r.Context(), ctxKeyNickname, nick)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

// NicknameFromContext returns the agent nickname attached by AuthMiddleware,
// or empty string if not present.
func NicknameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyNickname).(string)
	return v
}
```

- [ ] **Step 4: Run, verify PASS**

```bash
go test ./internal/backend/... -run TestAuthMiddleware -v
```

Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/auth.go internal/backend/auth_test.go
git commit -m "feat(backend): bearer-token middleware with constant-time compare"
```

---

## Task 5: Backend handlers `/healthz` and `/v1/report` (`internal/backend/handler.go`)

**Files:**
- Create: `internal/backend/handler.go`
- Create: `internal/backend/handler_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/backend/handler_test.go`:

```go
package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

func newServer(t *testing.T) (http.Handler, *bytes.Buffer) {
	t.Helper()
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, nil))
	tokens := map[string]string{
		"deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe": "testkeen",
	}
	return NewMux(logger, tokens), logBuf
}

func TestHealthz(t *testing.T) {
	h, _ := newServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("code: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("body: %q", rec.Body.String())
	}
}

func TestReport_HappyPath(t *testing.T) {
	h, logBuf := newServer(t)
	report := wire.Report{
		Timestamp:    time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		AgentVersion: "0.1.0",
		Checks: []wire.Check{
			{Name: "agent_heartbeat", Status: "ok", DurationMs: 1},
		},
	}
	body, _ := json.Marshal(report)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code: %d body: %s", rec.Code, rec.Body.String())
	}
	logged := logBuf.String()
	for _, want := range []string{`"nickname":"testkeen"`, `"agent_version":"0.1.0"`, `"check_count":1`} {
		if !strings.Contains(logged, want) {
			t.Errorf("log missing %s; full log: %s", want, logged)
		}
	}
}

func TestReport_RejectsBadJSON(t *testing.T) {
	h, _ := newServer(t)
	req := httptest.NewRequest("POST", "/v1/report", io.NopCloser(strings.NewReader("not json")))
	req.Header.Set("Authorization", "Bearer deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code: %d want 400", rec.Code)
	}
}

func TestReport_Unauthorized(t *testing.T) {
	h, _ := newServer(t)
	req := httptest.NewRequest("POST", "/v1/report", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code: %d", rec.Code)
	}
}

func TestReport_RejectsGet(t *testing.T) {
	h, _ := newServer(t)
	req := httptest.NewRequest("GET", "/v1/report", nil)
	req.Header.Set("Authorization", "Bearer deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("code: %d want 405", rec.Code)
	}
}
```

- [ ] **Step 2: Run, verify FAIL**

```bash
go test ./internal/backend/... -run "TestHealthz|TestReport"
```

Expected: FAIL `undefined: NewMux`.

- [ ] **Step 3: Implement `internal/backend/handler.go`**

```go
package backend

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/anex/wg-monitor/pkg/wire"
)

const maxReportBytes = 64 * 1024 // 64 KiB — heartbeat-only report is ~200 B

// NewMux builds the backend HTTP handler.
// tokenToNickname maps bearer-tokens to agent nicknames (loaded from config).
func NewMux(logger *slog.Logger, tokenToNickname map[string]string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	auth := AuthMiddleware(tokenToNickname)
	mux.Handle("/v1/report", auth(http.HandlerFunc(reportHandler(logger))))
	return mux
}

func reportHandler(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBytes+1))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if len(body) > maxReportBytes {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		var rep wire.Report
		if err := json.Unmarshal(body, &rep); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		nick := NicknameFromContext(r.Context())
		logger.Info("report",
			"nickname", nick,
			"agent_version", rep.AgentVersion,
			"ts", rep.Timestamp,
			"check_count", len(rep.Checks),
			"checks", checkSummary(rep.Checks),
		)
		w.WriteHeader(http.StatusOK)
	}
}

func checkSummary(checks []wire.Check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Name + "=" + c.Status
	}
	return out
}
```

- [ ] **Step 4: Run, verify PASS**

```bash
go test ./internal/backend/... -v
```

Expected: 5 new tests PASS, all earlier tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/handler.go internal/backend/handler_test.go
git commit -m "feat(backend): /v1/report and /healthz handlers with structured logging"
```

---

## Task 6: Backend entrypoint (`cmd/backend/main.go`)

**Files:**
- Create: `cmd/backend/main.go`
- Create: `deploy/backend/backend.yaml.example`

- [ ] **Step 1: Create example config (NOT a real token)**

Create `deploy/backend/backend.yaml.example`:

```yaml
listen: 127.0.0.1:8080
log_level: info
agents:
  - nickname: testkeen
    # 64-hex-char random — generate with: openssl rand -hex 32
    token: REPLACE_WITH_GENERATED_HEX_TOKEN_64_CHARS_LONG_DEADBEEFCAFEBABEDEADBEEF
```

- [ ] **Step 2: Implement `cmd/backend/main.go`**

```go
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anex/wg-monitor/internal/backend"
)

func main() {
	configPath := flag.String("config", "/etc/wg-monitor/backend.yaml", "path to YAML config")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := backend.LoadConfig(*configPath)
	if err != nil {
		logger.Error("config load", "err", err, "path", *configPath)
		os.Exit(2)
	}
	logger.Info("starting", "listen", cfg.Listen, "agents", len(cfg.Agents))

	tokenMap := make(map[string]string, len(cfg.Agents))
	for _, a := range cfg.Agents {
		tokenMap[a.Token] = a.Nickname
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           backend.NewMux(logger, tokenMap),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		logger.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("listen", "err", err)
		os.Exit(1)
	}
	logger.Info("stopped")
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go build -o bin/wg-monitor-backend ./cmd/backend/
```

Expected: `bin/wg-monitor-backend` exists, no errors.

- [ ] **Step 4: Smoke-run locally**

Generate a test config and start the server:

```bash
mkdir -p tmp
TOKEN=$(openssl rand -hex 32)
cat > tmp/backend.local.yaml <<EOF
listen: 127.0.0.1:18080
log_level: info
agents:
  - nickname: smoketest
    token: $TOKEN
EOF
./bin/wg-monitor-backend -config tmp/backend.local.yaml &
SERVER_PID=$!
sleep 1

# Healthz
curl -sf http://127.0.0.1:18080/healthz
echo

# Report (success)
curl -s -X POST http://127.0.0.1:18080/v1/report \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"ts":"2026-04-26T12:00:00Z","agent_version":"0.1.0","checks":[{"name":"agent_heartbeat","status":"ok","duration_ms":1}]}' \
  -w 'HTTP %{http_code}\n'

# Report (bad token)
curl -s -X POST http://127.0.0.1:18080/v1/report \
  -H "Authorization: Bearer wrong" -d '{}' \
  -w 'HTTP %{http_code}\n'

kill $SERVER_PID
```

Expected output:
- `ok`
- `HTTP 200`
- `unauthorized` followed by `HTTP 401`
- Server stdout shows JSON log lines for the successful report including `"nickname":"smoketest"`.

- [ ] **Step 5: Commit**

```bash
git add cmd/backend/main.go deploy/backend/backend.yaml.example
git commit -m "feat(backend): wire up cmd/backend with graceful shutdown"
```

---

## Task 7: Agent config parser (`internal/agent/config.go`)

**Files:**
- Create: `internal/agent/config.go`
- Create: `internal/agent/config_test.go`
- Create: `deploy/agent/config.yaml.example`

- [ ] **Step 1: Write failing test**

Create `internal/agent/config_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: https://wgmonitor.jkaotlic.duckdns.org
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe

agent:
  nickname: testkeen
  interval_sec: 60
  awg_iface: awg0
  expected_exit_ip: 89.125.101.122
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Backend.URL != "https://wgmonitor.jkaotlic.duckdns.org" {
		t.Errorf("url: %q", cfg.Backend.URL)
	}
	if cfg.Agent.Nickname != "testkeen" {
		t.Errorf("nickname: %q", cfg.Agent.Nickname)
	}
	if cfg.Agent.Interval() != 60*time.Second {
		t.Errorf("interval: %v", cfg.Agent.Interval())
	}
	if cfg.Agent.AwgIface != "awg0" {
		t.Errorf("awg_iface: %q", cfg.Agent.AwgIface)
	}
}

func TestLoadConfig_DefaultsInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: https://wgmonitor.jkaotlic.duckdns.org
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
agent:
  nickname: testkeen
  awg_iface: awg0
  expected_exit_ip: 1.2.3.4
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Interval() != 60*time.Second {
		t.Errorf("default interval: got %v want 60s", cfg.Agent.Interval())
	}
}

func TestLoadConfig_RejectsBadNickname(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: https://x
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
agent:
  nickname: "Bad Name"
  awg_iface: awg0
  expected_exit_ip: 1.2.3.4
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on bad nickname")
	}
}

func TestLoadConfig_RejectsHTTPURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: http://insecure.example
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
agent:
  nickname: testkeen
  awg_iface: awg0
  expected_exit_ip: 1.2.3.4
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error on plaintext HTTP URL")
	}
}
```

- [ ] **Step 2: Run, verify FAIL**

```bash
go test ./internal/agent/...
```

Expected: FAIL `undefined: LoadConfig`.

- [ ] **Step 3: Implement `internal/agent/config.go`**

```go
package agent

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var nicknameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

type Config struct {
	Backend BackendConfig `yaml:"backend"`
	Agent   AgentConfig   `yaml:"agent"`
}

type BackendConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

type AgentConfig struct {
	Nickname       string `yaml:"nickname"`
	IntervalSec    int    `yaml:"interval_sec"`
	AwgIface       string `yaml:"awg_iface"`
	ExpectedExitIP string `yaml:"expected_exit_ip"`
}

func (a AgentConfig) Interval() time.Duration {
	if a.IntervalSec <= 0 {
		return 60 * time.Second
	}
	return time.Duration(a.IntervalSec) * time.Second
}

func LoadConfig(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if !strings.HasPrefix(cfg.Backend.URL, "https://") {
		return nil, fmt.Errorf("backend.url must start with https://, got %q", cfg.Backend.URL)
	}
	if len(cfg.Backend.Token) < 32 {
		return nil, fmt.Errorf("backend.token must be at least 32 chars")
	}
	if !nicknameRegexp.MatchString(cfg.Agent.Nickname) {
		return nil, fmt.Errorf("agent.nickname %q must match %s", cfg.Agent.Nickname, nicknameRegexp)
	}
	if cfg.Agent.AwgIface == "" {
		return nil, fmt.Errorf("agent.awg_iface is required (no default — per-user, see spec Q4)")
	}
	if cfg.Agent.ExpectedExitIP == "" {
		return nil, fmt.Errorf("agent.expected_exit_ip is required (no default — per-user, see spec Q4)")
	}
	return &cfg, nil
}
```

- [ ] **Step 4: Run, verify PASS**

```bash
go test ./internal/agent/... -v -run TestLoadConfig
```

Expected: 4 PASS.

- [ ] **Step 5: Create `deploy/agent/config.yaml.example`**

```yaml
backend:
  url: https://wgmonitor.jkaotlic.duckdns.org
  # 64-hex-char random — backend operator generates this and gives it to you
  token: REPLACE_WITH_64_HEX_CHARS_FROM_BACKEND_OPERATOR_DEADBEEFCAFEBABEDEADBEEF

agent:
  nickname: testkeen          # ASCII lowercase, 2-16 chars, regexp ^[a-z][a-z0-9_-]{1,15}$
  interval_sec: 60
  awg_iface: awg0             # AmneziaWG interface name on this router (per-user)
  expected_exit_ip: 1.2.3.4   # public IP this router should appear as when egressing via awg_iface (per-user)
```

- [ ] **Step 6: Commit**

```bash
git add internal/agent/config.go internal/agent/config_test.go deploy/agent/config.yaml.example
git commit -m "feat(agent): YAML config parser with HTTPS/token/nickname/iface validation"
```

---

## Task 8: Agent HTTP client (`internal/agent/client.go`)

**Files:**
- Create: `internal/agent/client.go`
- Create: `internal/agent/client_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/agent/client_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestClient_SendReport_AttachesBearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok-abc", "0.1.0", 2*time.Second)
	rep := wire.Report{Timestamp: time.Now(), AgentVersion: "0.1.0"}
	if err := c.SendReport(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("auth header: %q", gotAuth)
	}
}

func TestClient_SendReport_PostsJSON(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 2*time.Second)
	rep := wire.Report{
		Timestamp:    time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		AgentVersion: "0.1.0",
		Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok", DurationMs: 1}},
	}
	if err := c.SendReport(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	var got wire.Report
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AgentVersion != "0.1.0" || len(got.Checks) != 1 {
		t.Errorf("body roundtrip: %+v", got)
	}
}

func TestClient_SendReport_ErrorsOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 2*time.Second)
	err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected error on 502, got nil")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("err should mention 502: %v", err)
	}
}

func TestClient_SendReport_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.SendReport(ctx, wire.Report{Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected error on cancellation")
	}
}

func TestClient_SendReport_RecordsMetrics(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 2*time.Second)
	for i := 0; i < 3; i++ {
		if err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&hits) != 3 {
		t.Errorf("hits: %d want 3", hits)
	}
}
```

- [ ] **Step 2: Run, verify FAIL**

```bash
go test ./internal/agent/... -run TestClient
```

Expected: FAIL `undefined: NewClient`.

- [ ] **Step 3: Implement `internal/agent/client.go`**

```go
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type Client struct {
	baseURL string
	token   string
	version string
	http    *http.Client
}

func NewClient(baseURL, token, version string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		version: version,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *Client) SendReport(ctx context.Context, report wire.Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/report", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wg-monitor/"+c.version)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("backend returned %d: %s", resp.StatusCode, string(preview))
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drain for keep-alive
	return nil
}
```

- [ ] **Step 4: Run, verify PASS**

```bash
go test ./internal/agent/... -v -run TestClient
```

Expected: 5 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/client.go internal/agent/client_test.go
git commit -m "feat(agent): HTTP client with bearer auth and JSON POST"
```

---

## Task 9: Agent heartbeat reporter (`internal/agent/reporter.go`)

**Files:**
- Create: `internal/agent/reporter.go`
- Create: `internal/agent/reporter_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/agent/reporter_test.go`:

```go
package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeSender struct {
	mu      sync.Mutex
	reports []wire.Report
	hits    int32
	errOn   int32
	err     error
}

func (f *fakeSender) SendReport(ctx context.Context, r wire.Report) error {
	n := atomic.AddInt32(&f.hits, 1)
	f.mu.Lock()
	f.reports = append(f.reports, r)
	f.mu.Unlock()
	if f.errOn > 0 && n <= f.errOn {
		return f.err
	}
	return nil
}

func TestReporter_TicksAndSends(t *testing.T) {
	fs := &fakeSender{}
	rep := NewReporter(fs, "0.1.0", 30*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Millisecond)
	defer cancel()
	rep.Run(ctx)
	hits := atomic.LoadInt32(&fs.hits)
	if hits < 3 || hits > 5 {
		t.Errorf("hits=%d want 3..5 (3 ticks + initial)", hits)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.reports) == 0 {
		t.Fatal("no reports recorded")
	}
	first := fs.reports[0]
	if first.AgentVersion != "0.1.0" {
		t.Errorf("agent_version: %q", first.AgentVersion)
	}
	if len(first.Checks) != 1 || first.Checks[0].Name != "agent_heartbeat" || first.Checks[0].Status != "ok" {
		t.Errorf("expected single agent_heartbeat=ok check, got: %+v", first.Checks)
	}
}

func TestReporter_StopsOnContextCancel(t *testing.T) {
	fs := &fakeSender{}
	rep := NewReporter(fs, "0.1.0", 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rep.Run(ctx); close(done) }()
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestReporter_ContinuesAfterSendError(t *testing.T) {
	fs := &fakeSender{errOn: 1, err: testErr("temp fail")}
	rep := NewReporter(fs, "0.1.0", 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	rep.Run(ctx)
	if atomic.LoadInt32(&fs.hits) < 3 {
		t.Errorf("only %d hits — reporter should keep ticking despite first failure", fs.hits)
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }
```

- [ ] **Step 2: Run, verify FAIL**

```bash
go test ./internal/agent/... -run TestReporter
```

Expected: FAIL `undefined: NewReporter`.

- [ ] **Step 3: Implement `internal/agent/reporter.go`**

```go
package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type Sender interface {
	SendReport(ctx context.Context, r wire.Report) error
}

type Reporter struct {
	sender   Sender
	version  string
	interval time.Duration
}

func NewReporter(sender Sender, version string, interval time.Duration) *Reporter {
	return &Reporter{sender: sender, version: version, interval: interval}
}

// Run sends an immediate report and then one per interval until ctx is done.
// Send errors are logged but do not stop the loop — Stage 0 has no JSONL buffer.
func (r *Reporter) Run(ctx context.Context) {
	r.sendOnce(ctx)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sendOnce(ctx)
		}
	}
}

func (r *Reporter) sendOnce(ctx context.Context) {
	start := time.Now()
	report := wire.Report{
		Timestamp:    start.UTC(),
		AgentVersion: r.version,
		Checks: []wire.Check{
			{
				Name:       "agent_heartbeat",
				Status:     "ok",
				DurationMs: time.Since(start).Milliseconds(),
			},
		},
	}
	if err := r.sender.SendReport(ctx, report); err != nil {
		slog.Warn("send report failed", "err", err)
	}
}
```

- [ ] **Step 4: Run, verify PASS**

```bash
go test ./internal/agent/... -v -run TestReporter
```

Expected: 3 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/reporter.go internal/agent/reporter_test.go
git commit -m "feat(agent): heartbeat-only reporter loop"
```

---

## Task 10: Agent entrypoint (`cmd/agent/main.go`)

**Files:**
- Create: `cmd/agent/main.go`

- [ ] **Step 1: Implement**

```go
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anex/wg-monitor/internal/agent"
)

// Version is overridable at link time: -ldflags "-X main.Version=0.1.0"
var Version = "0.1.0-dev"

func main() {
	configPath := flag.String("config", "/opt/etc/wg-monitor/config.yaml", "path to YAML config")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		logger.Error("config load", "err", err, "path", *configPath)
		os.Exit(2)
	}
	logger.Info("starting", "nickname", cfg.Agent.Nickname, "backend", cfg.Backend.URL,
		"interval", cfg.Agent.Interval(), "version", Version)

	client := agent.NewClient(cfg.Backend.URL, cfg.Backend.Token, Version, 10*time.Second)
	rep := agent.NewReporter(client, Version, cfg.Agent.Interval())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rep.Run(ctx)
	logger.Info("stopped")
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build -o bin/wg-monitor ./cmd/agent/
```

Expected: `bin/wg-monitor` exists.

- [ ] **Step 3: Smoke-run (against local backend from Task 6)**

In one terminal, run backend:

```bash
./bin/wg-monitor-backend -config tmp/backend.local.yaml
```

In another:

```bash
cat > tmp/agent.local.yaml <<EOF
backend:
  url: http://127.0.0.1:18080
  token: $TOKEN
agent:
  nickname: smoketest
  interval_sec: 2
  awg_iface: dummy0
  expected_exit_ip: 1.2.3.4
EOF
```

Note: HTTP URL will fail because of the `https://` validator. Temporarily relax for smoke or run via stunnel/Caddy on local. **Simplest:** comment out the `strings.HasPrefix(... "https://")` check during smoke, then re-enable. **Better:** add a `-allow-http` flag to agent for tests. For Stage 0 smoke, use the version below where we add the flag:

- [ ] **Step 4: Add `-allow-http` flag for tests/dev**

Update `cmd/agent/main.go` — add flag and pass to a new exported helper:

```go
	allowHTTP := flag.Bool("allow-http", false, "allow http:// backend URL (dev only)")
```

After `flag.Parse()` and before `LoadConfig`, set a package-level toggle if needed. **Simpler design**: make agent.LoadConfig accept an option. Refactor:

In `internal/agent/config.go`, change signature:

```go
type LoadOption func(*loadOpts)
type loadOpts struct{ allowHTTP bool }

func WithAllowHTTP() LoadOption { return func(o *loadOpts) { o.allowHTTP = true } }

func LoadConfig(path string, opts ...LoadOption) (*Config, error) {
	o := loadOpts{}
	for _, op := range opts { op(&o) }
	// ... existing read+unmarshal ...
	if o.allowHTTP {
		if !strings.HasPrefix(cfg.Backend.URL, "http://") && !strings.HasPrefix(cfg.Backend.URL, "https://") {
			return nil, fmt.Errorf("backend.url must start with http:// or https://, got %q", cfg.Backend.URL)
		}
	} else {
		if !strings.HasPrefix(cfg.Backend.URL, "https://") {
			return nil, fmt.Errorf("backend.url must start with https://, got %q", cfg.Backend.URL)
		}
	}
	// ... rest unchanged ...
}
```

Update `internal/agent/config_test.go` `TestLoadConfig_RejectsHTTPURL` to also test that `WithAllowHTTP()` lets http through:

```go
func TestLoadConfig_AllowHTTPOption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
backend:
  url: http://127.0.0.1:18080
  token: deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe
agent:
  nickname: testkeen
  awg_iface: awg0
  expected_exit_ip: 1.2.3.4
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil { t.Fatal(err) }
	if _, err := LoadConfig(path, WithAllowHTTP()); err != nil {
		t.Fatalf("WithAllowHTTP should permit http: %v", err)
	}
}
```

In `cmd/agent/main.go`, pass the flag:

```go
	var loadOpts []agent.LoadOption
	if *allowHTTP {
		loadOpts = append(loadOpts, agent.WithAllowHTTP())
	}
	cfg, err := agent.LoadConfig(*configPath, loadOpts...)
```

- [ ] **Step 5: Re-run all tests, verify PASS**

```bash
go test ./...
```

Expected: every test PASSES (including the new `TestLoadConfig_AllowHTTPOption`).

- [ ] **Step 6: Smoke run end-to-end (HTTP, local)**

Terminal A (backend, stays running):

```bash
./bin/wg-monitor-backend -config tmp/backend.local.yaml
```

Terminal B:

```bash
go build -o bin/wg-monitor ./cmd/agent/
./bin/wg-monitor -config tmp/agent.local.yaml -allow-http
```

Expected: every 2 seconds, terminal A prints a JSON log line like:

```json
{"time":"...","level":"INFO","msg":"report","nickname":"smoketest","agent_version":"0.1.0-dev","ts":"...","check_count":1,"checks":["agent_heartbeat=ok"]}
```

Terminal B prints a `starting` line and stays quiet (errors would log).

Stop both with `Ctrl+C`.

- [ ] **Step 7: Commit**

```bash
git add cmd/agent/main.go internal/agent/config.go internal/agent/config_test.go
git commit -m "feat(agent): wire up cmd/agent with -allow-http dev flag"
```

---

## Task 11: Makefile (cross-compile + UPX pack)

**Files:**
- Create: `Makefile`

- [ ] **Step 1: Implement**

```makefile
# wg-monitor build matrix
# Stage 0: pure Go, no cgo. Cross-compile via standard GOOS/GOARCH/GOMIPS env.

VERSION ?= 0.1.0-dev
LDFLAGS := -s -w -X main.Version=$(VERSION)
GOFLAGS := -trimpath -ldflags "$(LDFLAGS)"

BIN_DIR := bin

.PHONY: all build-host build-mipsel build-aarch64 pack test clean size

all: build-host

# Local OS, used for go test and dev
build-host:
	mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -o $(BIN_DIR)/wg-monitor ./cmd/agent
	go build $(GOFLAGS) -o $(BIN_DIR)/wg-monitor-backend ./cmd/backend

# Keenetic with MIPS little-endian, no FPU (most common: Realtek/Qualcomm SoCs)
build-mipsel:
	mkdir -p $(BIN_DIR)/linux-mipsle
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build $(GOFLAGS) -o $(BIN_DIR)/linux-mipsle/wg-monitor ./cmd/agent

# Keenetic with ARM64 (newer flagship models like Hopper, Peak)
build-aarch64:
	mkdir -p $(BIN_DIR)/linux-arm64
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BIN_DIR)/linux-arm64/wg-monitor ./cmd/agent

# UPX-pack cross-compiled agent binaries.
# UPX must be in PATH. Install: choco install upx (Win) or apt install upx-ucl (Linux).
pack: build-mipsel build-aarch64
	upx --best --lzma $(BIN_DIR)/linux-mipsle/wg-monitor
	upx --best --lzma $(BIN_DIR)/linux-arm64/wg-monitor
	@$(MAKE) size

# Verify packed binaries are under target sizes.
# Spec target: 1.5–3 MB. We hard-fail if either exceeds 3 MiB.
size:
	@for f in $(BIN_DIR)/linux-mipsle/wg-monitor $(BIN_DIR)/linux-arm64/wg-monitor; do \
		if [ -f "$$f" ]; then \
			bytes=$$(wc -c < "$$f"); \
			mib=$$(awk "BEGIN{printf \"%.2f\", $$bytes/1048576}"); \
			echo "$$f: $$bytes bytes ($$mib MiB)"; \
			if [ $$bytes -gt 3145728 ]; then echo "FAIL: $$f exceeds 3 MiB"; exit 1; fi; \
		fi; \
	done

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)
```

- [ ] **Step 2: Run `make test`**

```bash
make test
```

Expected: all tests PASS.

- [ ] **Step 3: Run `make build-mipsel build-aarch64`**

```bash
make build-mipsel build-aarch64
ls -la bin/linux-mipsle/ bin/linux-arm64/
```

Expected: `wg-monitor` exists in both directories. Unpacked size: ~6–9 MiB each.

- [ ] **Step 4: Run `make pack`**

```bash
make pack
```

Expected: UPX prints compression ratio. Final size for each binary: ~1.5–2.5 MiB. `make size` prints sizes and exits 0.

If UPX missing: install via `choco install upx` (Win) and re-run.

- [ ] **Step 5: Commit**

```bash
git add Makefile
git commit -m "build: Makefile with cross-compile (mipsel/aarch64) and UPX pack"
```

---

## Task 12: Caddy install + Caddyfile on VPS Main

**Files:**
- Create: `deploy/backend/Caddyfile`

- [ ] **Step 1: Create local `deploy/backend/Caddyfile`**

```
# wg-monitor backend reverse proxy
# Terminate TLS via TLS-ALPN-01 on :443 (no :80 needed — AdGuard owns it).

{
	# Default ACME issuer is LE; tls-alpn-01 is auto when :80 is not bound.
	email asnekhaev@gmail.com
}

wgmonitor.jkaotlic.duckdns.org {
	reverse_proxy 127.0.0.1:8080 {
		header_up Host {host}
		header_up X-Real-IP {remote_host}
	}
	# 1 MiB body cap — Stage 0 reports are <1 KiB, but agents will send
	# install bootstrap chunks via the same proxy in later stages.
	request_body {
		max_size 1MB
	}
	# Compact log line per request; full Caddy access log to journal.
	log {
		output stderr
		format console
	}
}
```

- [ ] **Step 2: Install Caddy on VPS Main**

```bash
ssh vps-main 'apt update -y -qq && \
  apt install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl && \
  curl -1sLf "https://dl.cloudsmith.io/public/caddy/stable/gpg.key" | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg && \
  curl -1sLf "https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt" | tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null && \
  apt update -y -qq && \
  apt install -y -qq caddy'
```

Expected: `caddy version` returns v2.x. systemd unit `caddy.service` is installed.

Per memory `feedback_apt_noninteractive`: if you see debconf hangs or NEEDRESTART prompts, prefix with `DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a`. Tipically clean for Caddy.

- [ ] **Step 3: Verify default Caddy starts**

```bash
ssh vps-main 'systemctl is-active caddy; ss -ltn | grep -E ":(80|443)\b"'
```

Expected: `active`. AdGuard owns :80 (host net), so Caddy default config will fight for :80 → expect Caddy to **fail bind on :80**. That's fine, we'll override.

- [ ] **Step 4: Deploy Caddyfile**

```bash
scp deploy/backend/Caddyfile vps-main:/etc/caddy/Caddyfile
```

- [ ] **Step 5: Reload Caddy**

```bash
ssh vps-main 'caddy validate --config /etc/caddy/Caddyfile && systemctl reload caddy && systemctl is-active caddy'
```

Expected: `Valid configuration`, then `active`.

- [ ] **Step 6: Verify cert obtained**

Caddy auto-https tries TLS-ALPN-01 since :80 is taken. Wait ~30 sec for first issuance:

```bash
ssh vps-main 'sleep 30; journalctl -u caddy --since "1 min ago" --no-pager | grep -E "obtain|certificate|error" | tail -20'
```

Expected: lines like `certificate obtained successfully` and `serving initial configuration`.

- [ ] **Step 7: Verify TLS handshake from outside**

From your local Windows machine (or any external host):

```bash
curl -v https://wgmonitor.jkaotlic.duckdns.org/healthz 2>&1 | grep -E "(SSL connection|certificate|HTTP/|< )" | head -20
```

Expected: TLS handshake succeeds, certificate is from `R3`/`E1`/`R10` (Let's Encrypt issuer), CN=`wgmonitor.jkaotlic.duckdns.org`. Body returns either 502 (backend not running yet — expected at this step) or `ok` (backend already running from later task).

- [ ] **Step 8: Commit Caddyfile**

```bash
git add deploy/backend/Caddyfile
git commit -m "deploy(backend): Caddyfile for wgmonitor.jkaotlic.duckdns.org"
```

---

## Task 13: systemd unit + backend deploy

**Files:**
- Create: `deploy/backend/wg-monitor-backend.service`

- [ ] **Step 1: Create systemd unit**

`deploy/backend/wg-monitor-backend.service`:

```ini
[Unit]
Description=wg-monitor backend
Documentation=https://github.com/anex/wg-monitor
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=wgmonitor
Group=wgmonitor
ExecStart=/usr/local/bin/wg-monitor-backend -config /etc/wg-monitor/backend.yaml
Restart=on-failure
RestartSec=5

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/wg-monitor
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Build Linux backend binary locally**

```bash
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.Version=0.1.0-stage0" -o bin/linux-amd64/wg-monitor-backend ./cmd/backend
ls -la bin/linux-amd64/wg-monitor-backend
```

Expected: ~7–8 MiB binary.

- [ ] **Step 3: Generate production token and prepare backend.yaml on VPS**

```bash
TOKEN=$(openssl rand -hex 32)
echo "Save this for the agent: $TOKEN"
ssh vps-main 'mkdir -p /etc/wg-monitor /var/lib/wg-monitor && \
  useradd -r -s /usr/sbin/nologin -d /var/lib/wg-monitor wgmonitor 2>/dev/null || true && \
  chown wgmonitor:wgmonitor /var/lib/wg-monitor'
ssh vps-main "cat > /etc/wg-monitor/backend.yaml" <<EOF
listen: 127.0.0.1:8080
log_level: info
agents:
  - nickname: testkeen
    token: $TOKEN
EOF
ssh vps-main 'chmod 640 /etc/wg-monitor/backend.yaml && chown root:wgmonitor /etc/wg-monitor/backend.yaml'
```

Save the printed `$TOKEN` locally — needed for agent config in Task 14.

- [ ] **Step 4: Deploy binary + unit**

```bash
scp bin/linux-amd64/wg-monitor-backend vps-main:/usr/local/bin/wg-monitor-backend
ssh vps-main 'chmod 755 /usr/local/bin/wg-monitor-backend'
scp deploy/backend/wg-monitor-backend.service vps-main:/etc/systemd/system/wg-monitor-backend.service
ssh vps-main 'systemctl daemon-reload && systemctl enable --now wg-monitor-backend'
```

- [ ] **Step 5: Verify service is up**

```bash
ssh vps-main 'systemctl is-active wg-monitor-backend && journalctl -u wg-monitor-backend --since "1 min ago" --no-pager | tail -20'
```

Expected: `active`. Logs show `"msg":"starting","listen":"127.0.0.1:8080","agents":1`.

- [ ] **Step 6: End-to-end TLS smoke from outside**

```bash
curl -sf https://wgmonitor.jkaotlic.duckdns.org/healthz
```

Expected: prints `ok`.

```bash
curl -sf https://wgmonitor.jkaotlic.duckdns.org/v1/report -X POST -d '{}' \
  -H "Authorization: Bearer wrong-token" -w 'HTTP %{http_code}\n'
```

Expected: `HTTP 401`.

```bash
curl -sf -X POST https://wgmonitor.jkaotlic.duckdns.org/v1/report \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"ts":"2026-04-26T12:00:00Z","agent_version":"0.1.0","checks":[{"name":"agent_heartbeat","status":"ok","duration_ms":1}]}' \
  -w 'HTTP %{http_code}\n'
```

Expected: `HTTP 200`. Backend log shows the report.

- [ ] **Step 7: Commit**

```bash
git add deploy/backend/wg-monitor-backend.service
git commit -m "deploy(backend): systemd unit with hardening (nologin user, ProtectSystem)"
```

---

## Task 14: Entware init.d + agent deploy on test Keenetic

**Files:**
- Create: `deploy/agent/S99wg-monitor`

- [ ] **Step 1: Create Entware init.d script**

`deploy/agent/S99wg-monitor`:

```sh
#!/bin/sh
# Entware init.d for wg-monitor agent on Keenetic.
# Place at /opt/etc/init.d/S99wg-monitor and chmod +x.

ENABLED=yes
PROCS=wg-monitor
ARGS="-config /opt/etc/wg-monitor/config.yaml"
PREARGS=""
DESC="wg-monitor agent"
PATH=/opt/sbin:/opt/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
```

The Entware `rc.func` provides standard `start`/`stop`/`restart` semantics — same pattern as other Entware services on your Keenetics.

- [ ] **Step 2: Detect arch on test Keenetic**

Replace `<TEST_KEEN>` with your test router's SSH alias:

```bash
ssh <TEST_KEEN> 'uname -m; cat /opt/etc/openwrt_release 2>/dev/null | head -3'
```

Expected: `mips` or `aarch64`. If `mips` — confirms little-endian variant by checking `file /opt/bin/busybox`:

```bash
ssh <TEST_KEEN> 'file /opt/bin/busybox'
```

If output contains `MIPS, MIPS32 rel2 version 1, SYSV, dynamically linked, ... not stripped` and indicates **LSB** (Least Significant Byte first → little-endian) — use mipsle binary. Otherwise stop and double-check.

- [ ] **Step 3: Pick the right binary**

For mipsel router: `bin/linux-mipsle/wg-monitor`
For aarch64 router: `bin/linux-arm64/wg-monitor`

Verify it's UPX-packed and under 3 MiB (Task 11 already did this; confirm):

```bash
ls -la bin/linux-mipsle/wg-monitor   # adjust path for arm64 if needed
```

Expected: under 3145728 bytes (3 MiB).

- [ ] **Step 4: Prepare directories on Keenetic**

```bash
ssh <TEST_KEEN> 'mkdir -p /opt/bin /opt/etc/wg-monitor /opt/var/wg-monitor /opt/etc/init.d'
```

- [ ] **Step 5: Deploy binary**

```bash
scp bin/linux-mipsle/wg-monitor <TEST_KEEN>:/opt/bin/wg-monitor
ssh <TEST_KEEN> 'chmod 755 /opt/bin/wg-monitor && /opt/bin/wg-monitor -config /dev/null 2>&1 | head -3'
```

Expected last command: prints config-load error mentioning `/dev/null` — proves binary runs on the architecture without a "cannot execute binary" error.

- [ ] **Step 6: Deploy config**

Use the `$TOKEN` you saved in Task 13 Step 3. For Stage 0 we use placeholder `awg_iface=awg0` and `expected_exit_ip=1.2.3.4` — they're required by the parser but not actually checked in Stage 0.

```bash
ssh <TEST_KEEN> "cat > /opt/etc/wg-monitor/config.yaml" <<EOF
backend:
  url: https://wgmonitor.jkaotlic.duckdns.org
  token: $TOKEN

agent:
  nickname: testkeen
  interval_sec: 60
  awg_iface: awg0
  expected_exit_ip: 1.2.3.4
EOF
ssh <TEST_KEEN> 'chmod 600 /opt/etc/wg-monitor/config.yaml'
```

- [ ] **Step 7: Deploy init.d script and start**

```bash
scp deploy/agent/S99wg-monitor <TEST_KEEN>:/opt/etc/init.d/S99wg-monitor
ssh <TEST_KEEN> 'chmod +x /opt/etc/init.d/S99wg-monitor && /opt/etc/init.d/S99wg-monitor start'
```

Expected: prints `Starting wg-monitor: OK` (Entware rc.func style).

- [ ] **Step 8: Verify the agent is running on Keenetic**

```bash
ssh <TEST_KEEN> 'pidof wg-monitor; ps w | grep wg-monitor'
```

Expected: a PID printed, ps line shows `/opt/bin/wg-monitor -config /opt/etc/wg-monitor/config.yaml`.

- [ ] **Step 9: Verify backend receives report**

```bash
ssh vps-main 'journalctl -u wg-monitor-backend --since "2 min ago" --no-pager | grep -E "report" | tail -5'
```

Expected: at least one line per minute with `"nickname":"testkeen"` and `"checks":["agent_heartbeat=ok"]`.

If the agent is on a 60-sec interval, wait 70 sec and re-check.

- [ ] **Step 10: Commit init.d + close out**

```bash
git add deploy/agent/S99wg-monitor
git commit -m "deploy(agent): Entware init.d S99wg-monitor"
```

---

## Task 15: Stage 0 acceptance verification

This task has no code — only verification commands and a final commit recording sign-off.

- [ ] **Step 1: Re-run full test suite**

```bash
make test
```

Expected: all tests PASS.

- [ ] **Step 2: Check binary sizes**

```bash
make pack
```

Expected: each cross-compiled binary < 3 MiB. Print actual sizes.

- [ ] **Step 3: Confirm production endpoints**

```bash
curl -sf https://wgmonitor.jkaotlic.duckdns.org/healthz && echo OK
```

Expected: prints `ok\nOK`.

- [ ] **Step 4: Confirm backend logs show ≥3 reports from test Keenetic in the last 5 minutes**

```bash
ssh vps-main 'journalctl -u wg-monitor-backend --since "5 min ago" --no-pager | grep -c "\"nickname\":\"testkeen\""'
```

Expected: integer ≥ 3 (3 reports at 60s interval = at least 3 in 5 min).

- [ ] **Step 5: Tag the release**

```bash
git tag -a v0.1.0-stage0 -m "Stage 0: bootstrapping complete (heartbeat-only end-to-end)"
git log --oneline | head -20
```

- [ ] **Step 6: Update README status line**

In `README.md` change:

```
- Stage 0 (bootstrapping): in progress
```

to:

```
- Stage 0 (bootstrapping): ✅ complete — agent + backend + Caddy live, heartbeat-only e2e verified 2026-04-XX
- Stage 1 (4 checks + state machine + TG basic): not started
```

(replace `2026-04-XX` with today's date when you finish).

- [ ] **Step 7: Final commit**

```bash
git add README.md
git commit -m "docs: mark Stage 0 complete"
```

---

## Stage 0 acceptance criteria (final checklist)

| Criterion | Verification |
|---|---|
| Agent binary mipsel < 3 MiB | `make size` exit 0 |
| Agent binary aarch64 < 3 MiB | `make size` exit 0 |
| `go test ./...` all pass | `make test` exit 0 |
| Backend reachable via TLS | `curl -sf https://wgmonitor.jkaotlic.duckdns.org/healthz` returns `ok` |
| Wrong token rejected | `curl … -H "Authorization: Bearer wrong" /v1/report` returns 401 |
| Agent on test Keenetic running under Entware init.d | `ssh <TEST_KEEN> pidof wg-monitor` returns PID |
| Agent posts heartbeat every interval | backend journal shows `nickname=testkeen, checks=[agent_heartbeat=ok]` ≥3× in 5 min |
| Caddy auto-renews LE cert | `journalctl -u caddy` shows `certificate obtained successfully` once; subsequent renewal happens 30 days before expiry automatically |

---

## Self-review

**Spec coverage** — Stage 0 deliverables (spec section 14 Etap 0):
- ✅ Repo + Makefile + cross-compile (mipsel, aarch64) → Tasks 1, 11
- ✅ Skeleton agent (heartbeat) → Tasks 7–10
- ✅ Skeleton backend (`/v1/report`, лог в stdout) → Tasks 3–6
- ✅ Бинарник 2-3 MB → Task 11 size guard, Task 15 Step 2
- ✅ Успешный POST с роутера на backend → Task 14 Step 9, Task 15 Step 4

User-stated additions to Stage 0 (from prompt):
- ✅ Caddy install + Caddyfile → Task 12
- ✅ systemd unit for backend → Task 13

Resolved-questions values used:
- ✅ Subdomain `wgmonitor.jkaotlic.duckdns.org` → Tasks 12, 13
- ✅ Nickname regexp `^[a-z][a-z0-9_-]{1,15}$` → Tasks 3 (backend), 7 (agent)
- ✅ `awg_iface` + `expected_exit_ip` per-user, no defaults → Task 7 (agent config rejects empty)
- ⏭ Bot token / group / admin id → deferred to Stage 1 (correct — no TG in Stage 0)

**Placeholder scan** — searched the plan for "TBD", "fill in", "similar to", "appropriate", "etc": one `<TEST_KEEN>` placeholder in Task 14 is intentional (engineer must provide their test router; can't pick for them). Step 6 of Task 14 has `1.2.3.4` and `awg0` — these are intentional Stage-0 placeholders for fields the spec marks as required but Stage 0 doesn't validate against reality. Documented in the step.

**Type consistency** — `wire.Report` and `wire.Check` defined in Task 2, used identically in Tasks 5, 8, 9. Field names (`Timestamp`/`AgentVersion`/`Checks`/`Name`/`Status`/`DurationMs`/`Details`) consistent across producer and consumer. JSON tags (`ts`, `agent_version`, `checks`, `name`, `status`, `duration_ms`, `details`) match spec §4.3. `LoadConfig` signature matches between Task 3 (backend) and Task 7 (agent), differing only in option-pattern in Task 10 Step 4 — refactor is explicit, tests adapted.

**One known fragility** — Task 14 detection of mipsel vs mipsbe relies on `file /opt/bin/busybox` output. On Entware-installed Keenetics this is reliable; if a router has a non-standard busybox path or a stripped binary, fall back to `readelf -h /opt/bin/wg-monitor` after first deploy attempt fails. Not adding more complexity in Stage 0.
