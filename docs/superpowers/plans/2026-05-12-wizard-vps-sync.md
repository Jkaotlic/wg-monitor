# Wizard ⇄ VPS Sync — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the VPS backend the single source of truth for fleet metadata. Wizard pulls the list of routers and their SSH-deploy coordinates on demand, pushes after every successful deploy. Any wizard PC sees the full fleet without manual re-entry.

**Architecture:** Add 5 nullable columns to `users` (`ssh_host`, `ssh_port`, `ssh_user`, `arch`, `last_deployed_version`) via the existing `addColumnIfMissing` migration pattern. Two new endpoints `GET /v1/wizard/agents` + `PUT /v1/wizard/agents/{nickname}` gated by a dedicated `WizardAuthMiddleware` reading a constant-time-compared token from `/etc/wg-monitor/wizard-token.txt` (mirrors `bot-token.txt`). Wizard generates the token at install-backend, caches in `secrets.env`, uploads to VPS. Local `wizard.toml` becomes a cache; merge logic is a pure function in `cmd/deploy/vps_sync.go`.

**Tech Stack:** Go 1.23, SQLite (modernc.org/sqlite), pure-Go SSH (`golang.org/x/crypto/ssh`), existing wg-monitor handler/middleware/repo patterns.

**Spec:** [docs/superpowers/specs/2026-05-12-wizard-vps-sync-design.md](../specs/2026-05-12-wizard-vps-sync-design.md)

---

## File Structure

**Created:**

| File | Purpose |
|---|---|
| `internal/backend/wizard_handler.go` | `WizardAuthMiddleware`, GET/PUT handlers, registered into `NewMux` |
| `internal/backend/wizard_handler_test.go` | Auth happy + 401 + PUT 204/404 (minimal) |
| `cmd/deploy/vps_sync.go` | `VPSClient` (ListAgents/PushAgent), `RemoteAgent`, `MergeAgents` pure-func |
| `cmd/deploy/vps_sync_test.go` | `MergeAgents` unit tests (3-4 cases) |

**Modified:**

| File | Change |
|---|---|
| `internal/backend/db/db.go` | Add `migrateWizardSync(d)` call (5 ALTER COLUMNs) |
| `internal/backend/db/users.go` | Add 5 fields to `User` struct, extend `scanUserFull`/`GetAll`, new methods `UpdateDeployInfo` and `WizardView` |
| `internal/backend/config.go` | Add `WizardTokenFile string` field + load + flag-on-empty |
| `internal/backend/handler.go` | Register wizard routes inside `NewMux` when token loaded |
| `cmd/backend/main.go` | Pass `cfg.WizardToken` into `Deps` |
| `cmd/deploy/templates/backend.yaml.tmpl` | Add `wizard_token_file: /etc/wg-monitor/wizard-token.txt` line |
| `cmd/deploy/actions.go` | `actionInstallBackend` generates + uploads token; `actionAddRouter` / `actionInstallAgent` / `actionUpdateComponents` push to VPS after success |
| `cmd/deploy/menu.go` | Renumber items, add `[10] Синхронизация с VPS`; auto-pull on startup banner |
| `cmd/deploy/state.go` | No structural change — `state.Agents` already holds host/port/user/arch |

---

## Task 1: DB migration — 5 new columns

**Files:**
- Modify: `internal/backend/db/db.go`

- [ ] **Step 1: Add migration function**

Append to `internal/backend/db/db.go` (after `migrateTelegramUserID`):

```go
// migrateWizardSync adds five nullable columns used by the wizard sync
// feature (v0.12.0). All NULL for pre-existing rows; wizard fills them on
// the first push after deploy. Reverse-compatible: older backend versions
// ignore unknown columns (SQLite never drops on schema reload).
func migrateWizardSync(d *sql.DB) error {
	if err := addColumnIfMissing(d, "users", "ssh_host",
		`ALTER TABLE users ADD COLUMN ssh_host TEXT`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "ssh_port",
		`ALTER TABLE users ADD COLUMN ssh_port INTEGER`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "ssh_user",
		`ALTER TABLE users ADD COLUMN ssh_user TEXT`); err != nil {
		return err
	}
	if err := addColumnIfMissing(d, "users", "arch",
		`ALTER TABLE users ADD COLUMN arch TEXT`); err != nil {
		return err
	}
	return addColumnIfMissing(d, "users", "last_deployed_version",
		`ALTER TABLE users ADD COLUMN last_deployed_version TEXT`)
}
```

- [ ] **Step 2: Wire into `Open`**

In `internal/backend/db/db.go`, inside `Open`, add a call after `migrateTelegramUserID`:

```go
if err := migrateTelegramUserID(d); err != nil {
    d.Close()
    return nil, fmt.Errorf("migrate users.telegram_user_id: %w", err)
}
if err := migrateWizardSync(d); err != nil {
    d.Close()
    return nil, fmt.Errorf("migrate users wizard sync: %w", err)
}
```

- [ ] **Step 3: Build sanity**

Run: `go build ./...`
Expected: clean exit.

- [ ] **Step 4: Commit**

```powershell
git add internal/backend/db/db.go
git commit -m @'
feat(db): migrate users — ssh_host/port/user/arch + last_deployed_version

Five nullable columns for wizard ⇄ VPS sync. Idempotent ALTER via existing
addColumnIfMissing pattern. NULL for pre-existing rows.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 2: Extend `User` struct + repo methods

**Files:**
- Modify: `internal/backend/db/users.go`

- [ ] **Step 1: Extend the struct**

Replace the existing `User` struct in `internal/backend/db/users.go` (current lines ~22-37) with the version below (5 new fields added, all `*string`/`*int64` to map NULLable columns; existing fields kept verbatim):

```go
type User struct {
	ID               int64
	Nickname         string
	TokenHash        string
	ExpectedExitIP   string
	AWGIface         string
	Kind             string
	TelegramThreadID *int64
	TelegramUserID   *int64
	CreatedAt        time.Time
	LastSeenAt       *time.Time
	// Wizard-sync deploy metadata (v0.12.0). All NULL for routers added
	// before sync existed. Filled by PUT /v1/wizard/agents/{nickname} after
	// a successful wizard deploy. NEVER read by the agent — wizard-only.
	SSHHost             *string
	SSHPort             *int64
	SSHUser             *string
	Arch                *string
	LastDeployedVersion *string
}
```

- [ ] **Step 2: Extend `userColsFull` and `scanUserFull`**

Replace the `userColsFull` constant (line ~74) with:

```go
const userColsFull = `id, nickname, token_hash, expected_exit_ip, awg_iface, kind, telegram_thread_id, telegram_user_id, created_at, last_seen_at, ssh_host, ssh_port, ssh_user, arch, last_deployed_version`
```

Replace `scanUserFull` (lines ~80-101) with:

```go
func scanUserFull(s userScanner) (*User, error) {
	var got User
	var threadID sql.NullInt64
	var tgUserID sql.NullInt64
	var lastSeen sql.NullTime
	var sshHost sql.NullString
	var sshPort sql.NullInt64
	var sshUser sql.NullString
	var arch sql.NullString
	var lastDepVer sql.NullString
	if err := s.Scan(
		&got.ID, &got.Nickname, &got.TokenHash, &got.ExpectedExitIP, &got.AWGIface, &got.Kind,
		&threadID, &tgUserID, &got.CreatedAt, &lastSeen,
		&sshHost, &sshPort, &sshUser, &arch, &lastDepVer,
	); err != nil {
		return nil, err
	}
	if threadID.Valid {
		v := threadID.Int64
		got.TelegramThreadID = &v
	}
	if tgUserID.Valid {
		v := tgUserID.Int64
		got.TelegramUserID = &v
	}
	if lastSeen.Valid {
		v := lastSeen.Time
		got.LastSeenAt = &v
	}
	if sshHost.Valid {
		v := sshHost.String
		got.SSHHost = &v
	}
	if sshPort.Valid {
		v := sshPort.Int64
		got.SSHPort = &v
	}
	if sshUser.Valid {
		v := sshUser.String
		got.SSHUser = &v
	}
	if arch.Valid {
		v := arch.String
		got.Arch = &v
	}
	if lastDepVer.Valid {
		v := lastDepVer.String
		got.LastDeployedVersion = &v
	}
	return &got, nil
}
```

- [ ] **Step 3: Update `GetAll` for the new columns**

Replace `GetAll` (lines ~144-174) with:

```go
func (u *UsersRepo) GetAll() ([]User, error) {
	rows, err := u.d.db.Query(
		`SELECT id, nickname, expected_exit_ip, awg_iface, kind, telegram_thread_id, telegram_user_id, last_seen_at, ssh_host, ssh_port, ssh_user, arch, last_deployed_version FROM users ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var got User
		var threadID sql.NullInt64
		var tgUserID sql.NullInt64
		var lastSeen sql.NullTime
		var sshHost sql.NullString
		var sshPort sql.NullInt64
		var sshUser sql.NullString
		var arch sql.NullString
		var lastDepVer sql.NullString
		if err := rows.Scan(
			&got.ID, &got.Nickname, &got.ExpectedExitIP, &got.AWGIface, &got.Kind,
			&threadID, &tgUserID, &lastSeen,
			&sshHost, &sshPort, &sshUser, &arch, &lastDepVer,
		); err != nil {
			return nil, err
		}
		if threadID.Valid {
			v := threadID.Int64
			got.TelegramThreadID = &v
		}
		if tgUserID.Valid {
			v := tgUserID.Int64
			got.TelegramUserID = &v
		}
		if lastSeen.Valid {
			v := lastSeen.Time
			got.LastSeenAt = &v
		}
		if sshHost.Valid {
			v := sshHost.String
			got.SSHHost = &v
		}
		if sshPort.Valid {
			v := sshPort.Int64
			got.SSHPort = &v
		}
		if sshUser.Valid {
			v := sshUser.String
			got.SSHUser = &v
		}
		if arch.Valid {
			v := arch.String
			got.Arch = &v
		}
		if lastDepVer.Valid {
			v := lastDepVer.String
			got.LastDeployedVersion = &v
		}
		out = append(out, got)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Add the upsert method**

Append to `internal/backend/db/users.go`:

```go
// DeployInfo carries the wizard-side metadata pushed via
// PUT /v1/wizard/agents/{nickname}. All fields required; empty strings or
// zero port are rejected by the handler before reaching here.
type DeployInfo struct {
	SSHHost             string
	SSHPort             int64
	SSHUser             string
	Arch                string
	LastDeployedVersion string
}

// UpdateDeployInfo upserts the wizard-side deploy fields by nickname. Returns
// ErrUserNotFound when no row matches (we do NOT auto-create — agent
// enrollment goes through the existing wg-monitor-cli add-user path).
func (u *UsersRepo) UpdateDeployInfo(nickname string, info DeployInfo) error {
	res, err := u.d.db.Exec(
		`UPDATE users SET ssh_host=?, ssh_port=?, ssh_user=?, arch=?, last_deployed_version=? WHERE nickname=?`,
		info.SSHHost, info.SSHPort, info.SSHUser, info.Arch, info.LastDeployedVersion, nickname,
	)
	if err != nil {
		return fmt.Errorf("users.UpdateDeployInfo: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}
```

- [ ] **Step 5: Build + run existing users tests**

Run: `go build ./...` then `go test ./internal/backend/db/ -run Users`
Expected: existing tests still pass — new fields are additive and NULL-safe.

- [ ] **Step 6: Commit**

```powershell
git add internal/backend/db/users.go
git commit -m @'
feat(db): User struct — ssh_host/port/user/arch + last_deployed_version

Five new nullable fields surfaced into the User struct and GetAll
projection. New UpdateDeployInfo upsert by nickname returns
ErrUserNotFound when no row matches (no auto-create).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 3: Backend config — `wizard_token_file`

**Files:**
- Modify: `internal/backend/config.go`

- [ ] **Step 1: Add the config field**

In `internal/backend/config.go`, after the existing `TelegramConfig` struct (~line 65), add a new struct `WizardConfig`:

```go
// WizardConfig wires the optional /v1/wizard/* endpoints. When TokenFile
// is empty OR points to a missing/empty file, the endpoints are NOT
// registered (fail-closed). To enable: put a 64-hex token (any opaque
// secret really) into the file, mode 0600 root:wgmonitor.
type WizardConfig struct {
	TokenFile string `yaml:"token_file"`
	// Token is loaded from TokenFile at config-load time. Empty → feature off.
	Token string `yaml:"-"`
}
```

- [ ] **Step 2: Plug into `Config`**

In the same file, find the `Config` struct (search for `type Config struct`) and add a `Wizard` field next to the existing `Telegram` field:

```go
Wizard WizardConfig `yaml:"wizard"`
```

- [ ] **Step 3: Load the token in `LoadConfig`**

After the existing bot-token load (around line 132, right after `if cfg.Telegram.BotToken == "" { ... }`), insert:

```go
// Wizard token is optional — empty file/path means the /v1/wizard/* feature
// is disabled. Missing file is NOT an error (the wizard pre-rc1 didn't write
// one; backend upgrades without immediate wizard upgrade should still boot).
if cfg.Wizard.TokenFile != "" {
	if b, err := os.ReadFile(cfg.Wizard.TokenFile); err == nil {
		cfg.Wizard.Token = strings.TrimSpace(string(b))
	}
}
```

- [ ] **Step 4: Build sanity**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```powershell
git add internal/backend/config.go
git commit -m @'
feat(backend): wizard.token_file config — fail-closed if absent

Optional pointer to /etc/wg-monitor/wizard-token.txt. Empty or missing
file = wizard endpoints disabled. Bottle pattern matches existing
telegram.bot_token_file.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 4: Wizard auth middleware + handler skeleton

**Files:**
- Create: `internal/backend/wizard_handler.go`
- Create: `internal/backend/wizard_handler_test.go`

- [ ] **Step 1: Write the failing auth test**

Create `internal/backend/wizard_handler_test.go`:

```go
package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWizardAuth_MissingHeader_401(t *testing.T) {
	h := WizardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
	))
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestWizardAuth_WrongToken_401(t *testing.T) {
	h := WizardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
	))
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestWizardAuth_RightToken_200(t *testing.T) {
	h := WizardAuthMiddleware("expected-token", nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
	))
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run — expect compile failure on missing symbol**

Run: `go test ./internal/backend/ -run TestWizardAuth`
Expected: FAIL — `undefined: WizardAuthMiddleware`.

- [ ] **Step 3: Implement the middleware**

Create `internal/backend/wizard_handler.go`:

```go
package backend

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// WizardAuthMiddleware gates /v1/wizard/* endpoints with a constant-time
// compare against the loaded wizard token. Empty `expected` is a bug —
// callers must check cfg.Wizard.Token != "" BEFORE wiring this middleware
// (the route registration in NewMux enforces that).
func WizardAuthMiddleware(expected string, logger *slog.Logger) func(http.Handler) http.Handler {
	logReject := func(r *http.Request, reason string) {
		if logger == nil {
			return
		}
		logger.Warn("wizard auth: rejected",
			"reason", reason, "remote", r.RemoteAddr, "path", r.URL.Path,
		)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(hdr, prefix) {
				logReject(r, "missing-bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			presented := strings.TrimPrefix(hdr, prefix)
			if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
				logReject(r, "token-mismatch")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

- [ ] **Step 4: Tests pass**

Run: `go test ./internal/backend/ -run TestWizardAuth -v`
Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/backend/wizard_handler.go internal/backend/wizard_handler_test.go
git commit -m @'
feat(backend): WizardAuthMiddleware — constant-time bearer for /v1/wizard/*

Mirror of AuthMiddleware shape but compares against a single configured
token instead of looking up per-user. slog audit on every 401.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 5: GET `/v1/wizard/agents` handler

**Files:**
- Modify: `internal/backend/wizard_handler.go`

- [ ] **Step 1: Add the handler**

Append to `internal/backend/wizard_handler.go`:

```go
import (
	"encoding/json"
	"net/http"
	// (existing imports stay)
)

// wizardAgent is the JSON shape returned to the wizard. NULL DB fields are
// emitted as empty/zero values (omitempty would hide them — we want explicit
// nulls visible so the wizard knows "not yet pushed").
type wizardAgent struct {
	Nickname            string `json:"nickname"`
	Kind                string `json:"kind"`
	ThreadID            int64  `json:"thread_id"`
	SSHHost             string `json:"ssh_host"`
	SSHPort             int64  `json:"ssh_port"`
	SSHUser             string `json:"ssh_user"`
	Arch                string `json:"arch"`
	LastDeployedVersion string `json:"last_deployed_version"`
	HasTopic            bool   `json:"has_topic"`
}

type wizardAgentList struct {
	Agents []wizardAgent `json:"agents"`
}

// wizardListAgentsHandler returns the full fleet as the wizard sees it.
// Read-only; safe to call as often as the wizard wants.
func wizardListAgentsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		users, err := d.DB.Users().GetAll()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		out := wizardAgentList{Agents: make([]wizardAgent, 0, len(users))}
		for _, u := range users {
			a := wizardAgent{
				Nickname: u.Nickname,
				Kind:     u.Kind,
				HasTopic: u.TelegramThreadID != nil,
			}
			if u.TelegramThreadID != nil {
				a.ThreadID = *u.TelegramThreadID
			}
			if u.SSHHost != nil {
				a.SSHHost = *u.SSHHost
			}
			if u.SSHPort != nil {
				a.SSHPort = *u.SSHPort
			}
			if u.SSHUser != nil {
				a.SSHUser = *u.SSHUser
			}
			if u.Arch != nil {
				a.Arch = *u.Arch
			}
			if u.LastDeployedVersion != nil {
				a.LastDeployedVersion = *u.LastDeployedVersion
			}
			out.Agents = append(out.Agents, a)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(out)
	}
}
```

- [ ] **Step 2: Add a one-shot integration test**

Append to `internal/backend/wizard_handler_test.go`:

```go
import (
	"encoding/json"
	"github.com/anex/wg-monitor/internal/backend/db"
	// (existing imports stay)
)

func TestWizardList_Empty(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := wizardListAgentsHandler(Deps{DB: d})
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got wizardAgentList
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 0 {
		t.Fatalf("want 0 agents, got %d", len(got.Agents))
	}
}

func TestWizardList_OneAgent(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("alyaba", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("alyaba", db.DeployInfo{
		SSHHost: "192.168.1.1", SSHPort: 222, SSHUser: "root",
		Arch: "mips", LastDeployedVersion: "v0.10.3",
	}); err != nil {
		t.Fatal(err)
	}
	h := wizardListAgentsHandler(Deps{DB: d})
	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got wizardAgentList
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 1 || got.Agents[0].Nickname != "alyaba" || got.Agents[0].SSHHost != "192.168.1.1" {
		t.Fatalf("unexpected: %+v", got)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/backend/ -run TestWizard -v`
Expected: 5 tests PASS (3 auth + 2 list).

- [ ] **Step 4: Commit**

```powershell
git add internal/backend/wizard_handler.go internal/backend/wizard_handler_test.go
git commit -m @'
feat(backend): GET /v1/wizard/agents — fleet view for the deploy wizard

JSON envelope { agents: [...] } with everything the wizard needs to merge
with its local state.Agents cache. Read-only; NULL DB fields surface as
empty/zero so the wizard can detect "not yet pushed".

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 6: PUT `/v1/wizard/agents/{nickname}` handler

**Files:**
- Modify: `internal/backend/wizard_handler.go`

- [ ] **Step 1: Add the handler**

Append to `internal/backend/wizard_handler.go`:

```go
import (
	"errors"
	"github.com/anex/wg-monitor/internal/backend/db"
	// (existing imports stay)
)

type wizardPutAgentReq struct {
	SSHHost             string `json:"ssh_host"`
	SSHPort             int64  `json:"ssh_port"`
	SSHUser             string `json:"ssh_user"`
	Arch                string `json:"arch"`
	LastDeployedVersion string `json:"last_deployed_version"`
}

// wizardPutAgentHandler upserts deploy metadata into an existing users row.
// Route path is /v1/wizard/agents/{nickname} — Go 1.22+ ServeMux pattern.
func wizardPutAgentHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		nickname := r.PathValue("nickname")
		if nickname == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "nickname required")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req wizardPutAgentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "bad json: "+err.Error())
			return
		}
		if req.SSHHost == "" || req.SSHPort == 0 || req.SSHUser == "" || req.Arch == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON,
				"ssh_host, ssh_port, ssh_user, arch are required")
			return
		}
		err := d.DB.Users().UpdateDeployInfo(nickname, db.DeployInfo{
			SSHHost:             req.SSHHost,
			SSHPort:             req.SSHPort,
			SSHUser:             req.SSHUser,
			Arch:                req.Arch,
			LastDeployedVersion: req.LastDeployedVersion,
		})
		if err != nil {
			if errors.Is(err, db.ErrUserNotFound) {
				writeJSONError(w, http.StatusNotFound, "user_not_found",
					"nickname not registered — run actionAddRouter first")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
```

- [ ] **Step 2: Add tests**

Append to `internal/backend/wizard_handler_test.go`:

```go
import (
	"strings"
	// (existing imports stay)
)

func TestWizardPut_404OnUnknown(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := wizardPutAgentHandler(Deps{DB: d})
	body := `{"ssh_host":"1.2.3.4","ssh_port":22,"ssh_user":"root","arch":"mips","last_deployed_version":"v0.1"}`
	req := httptest.NewRequest("PUT", "/v1/wizard/agents/ghost", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "ghost")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWizardPut_204Updates(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Users().Insert("alyaba", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	h := wizardPutAgentHandler(Deps{DB: d})
	body := `{"ssh_host":"10.0.0.1","ssh_port":222,"ssh_user":"root","arch":"mips","last_deployed_version":"v0.10.3"}`
	req := httptest.NewRequest("PUT", "/v1/wizard/agents/alyaba", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("nickname", "alyaba")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	u, err := d.Users().GetByNickname("alyaba")
	if err != nil || u.SSHHost == nil || *u.SSHHost != "10.0.0.1" {
		t.Fatalf("not persisted: u=%+v err=%v", u, err)
	}
}
```

- [ ] **Step 3: Run**

Run: `go test ./internal/backend/ -run TestWizard -v`
Expected: 7 tests PASS.

- [ ] **Step 4: Commit**

```powershell
git add internal/backend/wizard_handler.go internal/backend/wizard_handler_test.go
git commit -m @'
feat(backend): PUT /v1/wizard/agents/{nickname} — upsert deploy info

404 on unknown nickname (no auto-create — agent registration still
flows through wg-monitor-cli add-user). 204 on success.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 7: Register wizard routes in `NewMux`

**Files:**
- Modify: `internal/backend/handler.go`

- [ ] **Step 1: Extend `Deps`**

In `internal/backend/handler.go`, add a `WizardToken string` field to the `Deps` struct (near `ReportBurst`, around line 196):

```go
// WizardToken enables /v1/wizard/* endpoints when non-empty. Set from
// cfg.Wizard.Token by main. Empty → endpoints not registered (fail-closed).
WizardToken string
```

- [ ] **Step 2: Register the routes**

In `NewMux` (currently ends with the `/v1/cmd*` block around line 244), insert before `return mux`:

```go
// Wizard sync endpoints — feature-flagged on cfg.Wizard.Token being non-empty.
// Single global token, separate from per-agent tokens. Pattern uses Go 1.22+
// {nickname} path variable on the PUT.
if d.WizardToken != "" {
	wizAuth := WizardAuthMiddleware(d.WizardToken, d.Logger)
	mux.Handle("GET /v1/wizard/agents", reqID(wizAuth(wizardListAgentsHandler(d))))
	mux.Handle("PUT /v1/wizard/agents/{nickname}", reqID(wizAuth(wizardPutAgentHandler(d))))
}
```

- [ ] **Step 3: Wire into `main.go`**

In `cmd/backend/main.go`, find where `Deps{...}` is constructed (search for `Deps{`) and add:

```go
WizardToken: cfg.Wizard.Token,
```

(Place it alongside the existing `ReportRatePerSec` / `ReportBurst`.)

- [ ] **Step 4: Build + run all backend tests**

Run: `go build ./... && go test ./internal/backend/... -count=1`
Expected: clean compile, existing tests pass, new TestWizard* pass.

- [ ] **Step 5: Commit**

```powershell
git add internal/backend/handler.go cmd/backend/main.go
git commit -m @'
feat(backend): wire /v1/wizard/* routes into NewMux

Feature-flagged on cfg.Wizard.Token (empty → endpoints not registered,
fail-closed). Uses Go 1.22+ {nickname} path variable on the PUT.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 8: Backend YAML template — `wizard_token_file`

**Files:**
- Modify: `cmd/deploy/templates/backend.yaml.tmpl`

- [ ] **Step 1: Append the wizard block**

Append to `cmd/deploy/templates/backend.yaml.tmpl` (after the existing `ui:` block at the bottom):

```yaml

# Wizard sync token: enables /v1/wizard/agents read + PUT for the deploy
# wizard so any admin PC sees the same fleet picture. Generated by the
# wizard at install-backend time. If the file is missing or empty the
# endpoints are NOT registered (fail-closed).
wizard:
  token_file: /etc/wg-monitor/wizard-token.txt
```

- [ ] **Step 2: Build (template is embedded via go:embed)**

Run: `go build ./cmd/deploy/`
Expected: clean.

- [ ] **Step 3: Commit**

```powershell
git add cmd/deploy/templates/backend.yaml.tmpl
git commit -m @'
feat(deploy): backend.yaml — wizard.token_file pointing to /etc/wg-monitor

Mirror of telegram.bot_token_file pattern. Optional file; missing → wizard
endpoints disabled in the backend.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 9: Wizard bootstrap — generate + upload token in `actionInstallBackend`

**Files:**
- Modify: `cmd/deploy/actions.go`

- [ ] **Step 1: Add the helper**

Insert near the top of `cmd/deploy/actions.go` (after the existing imports):

```go
import (
	"crypto/rand"
	"encoding/hex"
	// (existing imports stay)
)

// ensureWizardToken returns a 64-hex token from the SecretStore, generating
// + persisting one if absent. Cached under key WIZARD_TOKEN.
func ensureWizardToken(secrets *SecretStore) (string, error) {
	if tok := secrets.GetNonInteractive("WIZARD_TOKEN"); tok != "" {
		return tok, nil
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	secrets.Set("WIZARD_TOKEN", tok)
	return tok, nil
}
```

- [ ] **Step 2: Verify `SecretStore.Set` exists**

Run: `grep -n "func.*Set(" cmd/deploy/secrets.go` (via the Grep tool).
Expected: should find a `Set(key, value string)` method. If absent (older SecretStore had only `Get`), add it:

```go
// Set stores key=value in the in-memory secret map AND writes through to
// disk cache (best-effort; disk write errors are logged but not returned).
func (s *SecretStore) Set(key, value string) {
	if s.disk == nil {
		s.disk = make(map[string]string)
	}
	s.disk[key] = value
	if p := secretsCachePath(); p != "" {
		if err := WriteSecretsAtomic(p, s.disk); err != nil {
			fmt.Fprintf(os.Stderr, "warn: secrets cache write failed: %v\n", err)
		}
	}
}
```

(Skip this step if `Set` already exists with equivalent semantics.)

- [ ] **Step 3: Wire bootstrap into `actionInstallBackend`**

In `cmd/deploy/actions.go`, find `actionInstallBackend` (around line 405). After the existing `bot-token.txt` upload block (~line 506, after the `chown root:wgmonitor /etc/wg-monitor/bot-token.txt` line), insert:

```go
PrintStep(6, 14, "wizard-token.txt")
wizTok, err := ensureWizardToken(secrets)
if err != nil {
	return fmt.Errorf("generate wizard token: %w", err)
}
if err := stepUploadFile(s, "/etc/wg-monitor/wizard-token.txt", []byte(wizTok+"\n"), "600"); err != nil {
	return err
}
if _, err := s.MustRun("chown root:wgmonitor /etc/wg-monitor/wizard-token.txt"); err != nil {
	PrintWarn("chown wizard-token.txt: " + err.Error())
}
PrintOK("wizard-token.txt")
```

Renumber the subsequent `PrintStep` calls so the total step count `/14` is consistent. (Original was `/13`. If you prefer to leave them alone, change `/14` → `/13` on the wizard-token step and adjust mentally — visual step count is cosmetic.)

- [ ] **Step 4: Build sanity**

Run: `go build ./cmd/deploy/`
Expected: clean.

- [ ] **Step 5: Commit**

```powershell
git add cmd/deploy/actions.go cmd/deploy/secrets.go
git commit -m @'
feat(deploy): install-backend generates + uploads wizard-token.txt

64-hex from crypto/rand, cached in WIZARD_TOKEN secret + uploaded to
/etc/wg-monitor/wizard-token.txt (mode 600 root:wgmonitor). Skipped
silently if cache already has a token.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 10: VPS client + `MergeAgents` pure function

**Files:**
- Create: `cmd/deploy/vps_sync.go`
- Create: `cmd/deploy/vps_sync_test.go`

- [ ] **Step 1: Write the failing merge tests**

Create `cmd/deploy/vps_sync_test.go`:

```go
package main

import (
	"reflect"
	"testing"
)

func TestMergeAgents_EmptyLocal_AllAdded(t *testing.T) {
	local := []AgentState{}
	remote := []RemoteAgent{{Nickname: "alyaba", SSHHost: "10.0.0.1", SSHPort: 222, SSHUser: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	merged, added, _ := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Nickname != "alyaba" || merged[0].Host != "10.0.0.1" {
		t.Fatalf("merged: %+v", merged)
	}
	if !reflect.DeepEqual(added, []string{"alyaba"}) {
		t.Fatalf("added: %+v", added)
	}
}

func TestMergeAgents_RemoteOverridesLocal(t *testing.T) {
	local := []AgentState{{Nickname: "alyaba", Host: "old", Port: 22, User: "root", Arch: "mips", LastDeployedVersion: "v0.9"}}
	remote := []RemoteAgent{{Nickname: "alyaba", SSHHost: "new", SSHPort: 222, SSHUser: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	merged, added, divergent := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Host != "new" || merged[0].Port != 222 || merged[0].LastDeployedVersion != "v0.10.3" {
		t.Fatalf("merged: %+v", merged)
	}
	if len(added) != 0 {
		t.Fatalf("want 0 added, got %v", added)
	}
	if len(divergent) != 1 || divergent[0] != "alyaba" {
		t.Fatalf("want 1 divergent, got %v", divergent)
	}
}

func TestMergeAgents_RemoteNullPreservesLocalSSH(t *testing.T) {
	// Remote has no SSH (NULLs from DB), local has it. Local wins for SSH
	// because remote NULLs are "unknown" not "delete".
	local := []AgentState{{Nickname: "alyaba", Host: "192.168.1.1", Port: 222, User: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	remote := []RemoteAgent{{Nickname: "alyaba"}} // all empty
	merged, _, _ := MergeAgents(local, remote)
	if merged[0].Host != "192.168.1.1" || merged[0].Port != 222 {
		t.Fatalf("local SSH lost: %+v", merged[0])
	}
}

func TestMergeAgents_LocalOnlyKept(t *testing.T) {
	local := []AgentState{{Nickname: "ghost", Host: "1.1.1.1", Port: 22, User: "root", Arch: "mips"}}
	remote := []RemoteAgent{}
	merged, _, _ := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Nickname != "ghost" {
		t.Fatalf("local-only dropped: %+v", merged)
	}
}
```

- [ ] **Step 2: Run — expect compile failure**

Run: `go test ./cmd/deploy/ -run TestMerge`
Expected: FAIL — `undefined: RemoteAgent` / `MergeAgents`.

- [ ] **Step 3: Implement `vps_sync.go`**

Create `cmd/deploy/vps_sync.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RemoteAgent mirrors the GET /v1/wizard/agents JSON shape.
type RemoteAgent struct {
	Nickname            string `json:"nickname"`
	Kind                string `json:"kind"`
	ThreadID            int64  `json:"thread_id"`
	SSHHost             string `json:"ssh_host"`
	SSHPort             int64  `json:"ssh_port"`
	SSHUser             string `json:"ssh_user"`
	Arch                string `json:"arch"`
	LastDeployedVersion string `json:"last_deployed_version"`
	HasTopic            bool   `json:"has_topic"`
}

type wizardAgentListWire struct {
	Agents []RemoteAgent `json:"agents"`
}

// VPSClient is a tiny HTTP client for the /v1/wizard/* endpoints.
type VPSClient struct {
	BaseURL string // e.g. "https://mon.example.com"
	Token   string
	HTTP    *http.Client
}

// NewVPSClient assembles a client from wizard state. Returns nil if the
// state is incomplete (no domain or no token) — callers should treat nil
// as "sync disabled, skip silently".
func NewVPSClient(domain, token string) *VPSClient {
	if domain == "" || token == "" {
		return nil
	}
	base := domain
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	return &VPSClient{
		BaseURL: strings.TrimRight(base, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *VPSClient) ListAgents(ctx context.Context) ([]RemoteAgent, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/v1/wizard/agents", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET /v1/wizard/agents: HTTP %d", resp.StatusCode)
	}
	var out wizardAgentListWire
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

func (c *VPSClient) PushAgent(ctx context.Context, a RemoteAgent) error {
	body, err := json.Marshal(struct {
		SSHHost             string `json:"ssh_host"`
		SSHPort             int64  `json:"ssh_port"`
		SSHUser             string `json:"ssh_user"`
		Arch                string `json:"arch"`
		LastDeployedVersion string `json:"last_deployed_version"`
	}{a.SSHHost, a.SSHPort, a.SSHUser, a.Arch, a.LastDeployedVersion})
	if err != nil {
		return err
	}
	u := c.BaseURL + "/v1/wizard/agents/" + url.PathEscape(a.Nickname)
	req, err := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		return fmt.Errorf("PUT /v1/wizard/agents/%s: HTTP %d", a.Nickname, resp.StatusCode)
	}
	return nil
}

// MergeAgents reconciles local state.Agents with the remote view.
// Rules:
//   - remote-only entries → appended to merged with whatever SSH info remote
//     has (may be empty if VPS never received a push for them)
//   - both-present → merged inherits remote.LastDeployedVersion and remote
//     ThreadID; SSH fields come from remote IF remote has them non-empty,
//     else local SSH wins (remote NULL = "unknown", not "delete")
//   - local-only entries → kept as-is (warn separately upstream)
//   - SSH-divergence (both have non-empty SSH but differ) → divergent slice
//     surfaced for logging; merged takes remote
func MergeAgents(local []AgentState, remote []RemoteAgent) (merged []AgentState, added []string, divergent []string) {
	byNick := make(map[string]int, len(local))
	for i, a := range local {
		byNick[a.Nickname] = i
	}
	merged = append([]AgentState(nil), local...) // copy
	for _, r := range remote {
		idx, ok := byNick[r.Nickname]
		if !ok {
			merged = append(merged, AgentState{
				Nickname:            r.Nickname,
				Host:                r.SSHHost,
				Port:                int(r.SSHPort),
				User:                r.SSHUser,
				Arch:                r.Arch,
				Kind:                r.Kind,
				ThreadID:            int(r.ThreadID),
				LastDeployedVersion: r.LastDeployedVersion,
			})
			added = append(added, r.Nickname)
			continue
		}
		a := &merged[idx]
		// remote-authoritative fields
		if r.ThreadID != 0 {
			a.ThreadID = int(r.ThreadID)
		}
		if r.Kind != "" {
			a.Kind = r.Kind
		}
		if r.LastDeployedVersion != "" {
			a.LastDeployedVersion = r.LastDeployedVersion
		}
		// SSH: remote wins iff remote has value; else preserve local.
		// Track divergence (both non-empty AND differ) for visibility.
		if r.SSHHost != "" {
			if a.Host != "" && a.Host != r.SSHHost {
				divergent = append(divergent, r.Nickname)
			}
			a.Host = r.SSHHost
		}
		if r.SSHPort != 0 {
			a.Port = int(r.SSHPort)
		}
		if r.SSHUser != "" {
			a.User = r.SSHUser
		}
		if r.Arch != "" {
			a.Arch = r.Arch
		}
	}
	return
}

// AgentStateToRemote converts a wizard-local AgentState to the RemoteAgent
// payload the PUT endpoint expects. Empty Arch falls back to amd64 as a
// last-resort default — the wizard already prompts during install so this
// rarely triggers.
func AgentStateToRemote(a AgentState) RemoteAgent {
	arch := a.Arch
	if arch == "" {
		arch = "amd64"
	}
	return RemoteAgent{
		Nickname:            a.Nickname,
		SSHHost:             a.Host,
		SSHPort:             int64(a.Port),
		SSHUser:             a.User,
		Arch:                arch,
		LastDeployedVersion: a.LastDeployedVersion,
	}
}
```

- [ ] **Step 4: Tests pass**

Run: `go test ./cmd/deploy/ -run TestMerge -v`
Expected: 4 tests PASS.

- [ ] **Step 5: Commit**

```powershell
git add cmd/deploy/vps_sync.go cmd/deploy/vps_sync_test.go
git commit -m @'
feat(deploy): VPSClient + MergeAgents — sync primitives

Pure HTTP client for /v1/wizard/* + a pure merge function. Remote wins
for thread_id / last_deployed_version; SSH-fields prefer remote unless
remote is empty (NULL means "unknown", not "delete").

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 11: Menu `[10] Синхронизация с VPS`

**Files:**
- Modify: `cmd/deploy/menu.go`

- [ ] **Step 1: Add the menu entry**

In `cmd/deploy/menu.go`, locate `RunMenu` (line ~12). After the `case "9":` block, insert:

```go
case "10":
    runActionAndSave(state, statePath, secrets, func() error {
        return actionSyncVPS(state, secrets)
    })
```

- [ ] **Step 2: Update `printMenuItems`**

In `cmd/deploy/menu.go`, find `printMenuItems` (~line 79). Before the line `fmt.Println("  [Q] Выход")`, insert:

```go
fmt.Println("  [10] Синхронизация с VPS  " + Colorize("(подтянуть список роутеров с бэкенда)", ColorDim))
```

- [ ] **Step 3: Implement `actionSyncVPS`**

Append to `cmd/deploy/actions.go`:

```go
import (
	"context"
	// (existing imports stay)
)

// actionSyncVPS pulls the fleet list from /v1/wizard/agents and merges into
// state.Agents. Best-effort: prints what changed; never deletes local-only
// entries (warns instead).
func actionSyncVPS(state *State, secrets *SecretStore) error {
	if state.Backend.Domain == "" {
		return fmt.Errorf("backend.domain пустой — сначала [1] install-backend")
	}
	tok, _ := secrets.Get("WIZARD_TOKEN", "Wizard sync token (из /etc/wg-monitor/wizard-token.txt на VPS)", nil)
	if tok == "" {
		return fmt.Errorf("WIZARD_TOKEN не задан")
	}
	c := NewVPSClient(state.Backend.Domain, tok)
	if c == nil {
		return fmt.Errorf("VPSClient init failed (empty domain or token)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	remote, err := c.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("VPS unreachable or auth failed: %w", err)
	}
	merged, added, divergent := MergeAgents(state.Agents, remote)
	state.Agents = merged
	PrintOK(fmt.Sprintf("Получено с VPS: %d роутеров", len(remote)))
	if len(added) > 0 {
		PrintInfo("Добавлено локально:")
		for _, n := range added {
			PrintInfo("  + " + n)
		}
	}
	if len(divergent) > 0 {
		PrintWarn("SSH-координаты разошлись (VPS-значение применено):")
		for _, n := range divergent {
			PrintWarn("  ~ " + n)
		}
	}
	// Local-only detection: nicknames present locally but not in remote.
	remoteSet := make(map[string]struct{}, len(remote))
	for _, r := range remote {
		remoteSet[r.Nickname] = struct{}{}
	}
	var localOnly []string
	for _, a := range state.Agents {
		if _, ok := remoteSet[a.Nickname]; !ok {
			localOnly = append(localOnly, a.Nickname)
		}
	}
	if len(localOnly) > 0 {
		PrintWarn("Локально есть, на VPS нет (возможно удалены через CLI):")
		for _, n := range localOnly {
			PrintWarn("  ? " + n)
		}
	}
	return nil
}
```

- [ ] **Step 4: Build sanity**

Run: `go build ./cmd/deploy/`
Expected: clean.

- [ ] **Step 5: Commit**

```powershell
git add cmd/deploy/menu.go cmd/deploy/actions.go
git commit -m @'
feat(deploy): menu [10] Синхронизация с VPS

Pulls /v1/wizard/agents, merges into state.Agents, prints added /
divergent / local-only lists. Best-effort; surfaces auth / network
errors as readable messages.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 12: Push after deploy in `actionInstallAgent` + `actionUpdateAgent`

**Files:**
- Modify: `cmd/deploy/actions.go`

`actionAddRouter` ends with `return actionInstallAgent(...)` so it's covered transitively. `actionUpdateComponents` ends with `actionUpdateAgent` / `actionUpdateBackend` for each chosen target ([cmd/deploy/update_components.go:259-261](cmd/deploy/update_components.go#L259-L261)) — also covered. We only need to wire push into `actionInstallAgent` and `actionUpdateAgent`.

- [ ] **Step 1: Add a tiny best-effort push helper**

Append to `cmd/deploy/actions.go`:

```go
// pushToVPSBestEffort PUTs deploy info for the given agent. Logs but does
// NOT return errors — push is best-effort and must never break the deploy
// flow (e.g. when offline or token rotation pending).
func pushToVPSBestEffort(state *State, secrets *SecretStore, a AgentState) {
	if state.Backend.Domain == "" {
		return
	}
	tok := secrets.GetNonInteractive("WIZARD_TOKEN")
	if tok == "" {
		return
	}
	c := NewVPSClient(state.Backend.Domain, tok)
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.PushAgent(ctx, AgentStateToRemote(a)); err != nil {
		PrintWarn(fmt.Sprintf("VPS sync push failed for %s: %v (deploy itself succeeded)", a.Nickname, err))
		return
	}
	PrintOK("VPS sync: " + a.Nickname)
}
```

- [ ] **Step 2: Wire into `actionInstallAgent`**

In `cmd/deploy/actions.go`, find `actionInstallAgent` (~line 208). Locate its final `return nil` (after agent.yaml upload + service start). Insert just before that `return nil`:

```go
// Best-effort sync push so other wizard PCs see this deploy.
if a := state.FindAgent(nickname); a != nil {
	pushToVPSBestEffort(state, secrets, *a)
}
```

- [ ] **Step 3: Wire into `actionUpdateAgent`**

In `cmd/deploy/actions.go`, find `actionUpdateAgent` (~line 90). After the agent binary upload + service restart succeed (look for the function's final success path — typically right before its `return nil`), insert:

```go
pushToVPSBestEffort(state, secrets, *ag)
```

(`ag` is the `*AgentState` resolved earlier in the function around line 95–110.)

- [ ] **Step 4: Build sanity + manual smoke**

Run: `go build ./cmd/deploy/`
Expected: clean.

Manual smoke (do this on a real VPS, not in CI):
1. Wizard PC1: `actionInstallBackend` → token generated + uploaded.
2. Wizard PC1: `actionAddRouter alyaba` → push fires (via the `actionInstallAgent` tail); VPS now has SSH info for alyaba.
3. Wizard PC2 (fresh wizard.toml, same domain, copy `WIZARD_TOKEN` from PC1 `secrets.env`): `[10] Sync` → alyaba appears with full SSH info.

- [ ] **Step 5: Commit**

```powershell
git add cmd/deploy/actions.go
git commit -m @'
feat(deploy): push deploy info to VPS after install-agent / update-agent

Best-effort PUT /v1/wizard/agents/{nickname} so any other wizard PC sees
the fleet after the next sync. actionAddRouter and actionUpdateComponents
inherit this via the actionInstallAgent / actionUpdateAgent tails.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Task 13: Auto-pull on wizard startup (polish, optional but cheap)

**Files:**
- Modify: `cmd/deploy/menu.go`

- [ ] **Step 1: Pull at top of `RunMenu`**

In `cmd/deploy/menu.go`, at the very top of `RunMenu` (after `PrintBanner()`), insert:

```go
// Best-effort: pull fresh fleet picture at startup so the menu reflects
// what's actually on VPS. Silent on first-run / offline / missing token.
if state.Backend.Domain != "" {
	if tok := secrets.GetNonInteractive("WIZARD_TOKEN"); tok != "" {
		if c := NewVPSClient(state.Backend.Domain, tok); c != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if remote, err := c.ListAgents(ctx); err == nil {
				merged, added, _ := MergeAgents(state.Agents, remote)
				state.Agents = merged
				if len(added) > 0 {
					PrintInfo(fmt.Sprintf("VPS sync на старте: добавлено %d новых роутеров (%s)", len(added), strings.Join(added, ", ")))
					_ = SaveState(statePath, state)
				}
			} else {
				PrintWarn("⚠ VPS unreachable на старте — работаю с локальным кэшем")
			}
			cancel()
		}
	}
}
```

You may need to add `"context"`, `"strings"`, `"time"` to the menu.go imports.

- [ ] **Step 2: Build sanity**

Run: `go build ./cmd/deploy/`
Expected: clean.

- [ ] **Step 3: Commit**

```powershell
git add cmd/deploy/menu.go
git commit -m @'
feat(deploy): auto-pull fleet from VPS on wizard startup

Best-effort 5s-timeout call to /v1/wizard/agents at RunMenu top.
Silent on offline / no-token / first-run. Surfaces new routers via
a one-line banner and re-saves wizard.toml.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
'@
```

---

## Self-Review (run after Task 13)

After all tasks land, do a final pass:

- [ ] `go build ./...` clean
- [ ] `go test ./internal/backend/... ./cmd/deploy/... -count=1` green
- [ ] Manual two-PC smoke from Task 12 Step 5 passes — `alyaba` from PC1 surfaces on PC2 after `[10] Sync` (or autopull on PC2 startup)
- [ ] `/v1/wizard/agents` returns 401 without bearer (curl smoke from outside VPS)
- [ ] `/v1/wizard/agents` returns 401 with WRONG bearer
- [ ] If `/etc/wg-monitor/wizard-token.txt` is `chmod 000`'d the backend still boots (token simply doesn't load → endpoints not registered)
- [ ] README router-operators-style one-liner added: «Wizard ⇄ VPS sync: бэк = источник правды о флоте, любой ПК видит весь список после [10] Sync»

---

## Out-of-Scope (call out, do NOT implement)

- Token rotation UX (`[11] Rotate WIZARD_TOKEN`) — separate task.
- Multi-tenant tokens (one per admin PC) — current single global token is fine.
- Switching VPS SSH from password to ed25519 keys — orthogonal hardening, separate PR.
- Conflict-detection / optimistic concurrency on PUT — last-write-wins is per spec.
- An offline-first / queue-on-failure design for push — best-effort is per spec.
- Removing the local `wizard.toml` cache entirely — keep it for offline boot.
