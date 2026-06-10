# VPS Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a protected, beautiful Tabler-based dashboard inside `wg-monitor-backend` for VPS-side monitoring, safe bot-equivalent actions, and backend/agent deploys.

**Architecture:** Add dashboard config and auth to the backend, then wire `/dashboard/` static assets and `/v1/dashboard/*` JSON endpoints into the existing `backend.NewMux`. The dashboard reuses the current SQLite repositories, command queue, wizard deploy semantics, and backend-update request file instead of creating a parallel operations path.

**Tech Stack:** Go `net/http`, SQLite repositories, existing `wire.Command` queue, embedded static HTML/CSS/JS, Tabler-inspired local assets, vanilla JavaScript.

---

## File Structure

- Modify: `internal/backend/config.go` for `DashboardConfig`, token loading, and fail-closed config validation.
- Modify: `internal/backend/config_test.go` for dashboard config tests.
- Modify: `internal/backend/handler.go` to add dashboard deps and route registration.
- Create: `internal/backend/dashboard_handler.go` for dashboard auth, summary, command dispatch, deploy, command-result polling, and static route mounting.
- Create: `internal/backend/dashboard_handler_test.go` for API and auth tests.
- Create: `internal/backend/dashboard_static.go` for embedded dashboard assets.
- Create: `internal/backend/dashboard_static/index.html` for the Tabler-based shell.
- Create: `internal/backend/dashboard_static/app.css` for the local operator theme.
- Create: `internal/backend/dashboard_static/app.js` for dashboard state, API calls, rendering, and command polling.
- Create: `internal/backend/dashboard_static/vendor/tabler-lite.css` for the local Tabler-compatible subset used by the MVP.
- Create: `internal/backend/dashboard_static/vendor/tabler-icons-lite.css` for local icon classes used by the MVP.
- Modify: `cmd/backend/main.go` to pass the dashboard token into `backend.NewMux`.
- Modify: `cmd/deploy/templates.go` to render dashboard config paths.
- Modify: `cmd/deploy/templates/backend.yaml.tmpl` to include disabled-by-default dashboard config comments.
- Modify: `cmd/deploy/templates_test.go` to prove backend YAML renders dashboard config guidance.
- Modify: `README.md` and `DEPLOY.md` to document enabling the dashboard and token-file safety.

## Task 1: Dashboard Config and Fail-Closed Token Loading

**Files:**
- Modify: `internal/backend/config.go`
- Modify: `internal/backend/config_test.go`

- [ ] **Step 1: Write failing config tests**

Append these tests to `internal/backend/config_test.go`:

```go
func TestLoadConfigDashboardDefaultsDisabled(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "bot-token", "secret-bot-token-xyz")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/state.db
telegram:
  bot_token_file: `+tokPath+`
  chat_id: -1003651873378
  admin_user_id: 136513775
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dashboard.Enabled {
		t.Fatal("dashboard must be disabled by default")
	}
	if cfg.Dashboard.Token != "" {
		t.Fatalf("dashboard token must stay empty when disabled, got %q", cfg.Dashboard.Token)
	}
}

func TestLoadConfigDashboardLoadsTokenWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	botPath := writeFile(t, dir, "bot-token", "secret-bot-token-xyz")
	dashboardPath := writeFile(t, dir, "dashboard-token", "dashboard-secret\n")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/state.db
telegram:
  bot_token_file: `+botPath+`
  chat_id: -1003651873378
  admin_user_id: 136513775
dashboard:
  enabled: true
  token_file: `+dashboardPath+`
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Dashboard.Enabled {
		t.Fatal("dashboard should be enabled")
	}
	if cfg.Dashboard.Token != "dashboard-secret" {
		t.Fatalf("dashboard token=%q", cfg.Dashboard.Token)
	}
}

func TestLoadConfigDashboardFailsClosedWhenEnabledWithoutToken(t *testing.T) {
	dir := t.TempDir()
	botPath := writeFile(t, dir, "bot-token", "secret-bot-token-xyz")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/state.db
telegram:
  bot_token_file: `+botPath+`
  chat_id: -1003651873378
  admin_user_id: 136513775
dashboard:
  enabled: true
  token_file: `+filepath.Join(dir, "missing-dashboard-token")+`
`)
	if _, err := LoadConfig(cfgPath); err == nil || !strings.Contains(err.Error(), "dashboard.token_file") {
		t.Fatalf("expected dashboard token_file error, got %v", err)
	}
}
```

- [ ] **Step 2: Run config tests and verify RED**

Run:

```powershell
go test ./internal/backend -run TestLoadConfigDashboard -count=1
```

Expected: build fails because `Config.Dashboard` and `DashboardConfig` do not exist.

- [ ] **Step 3: Add dashboard config model**

In `internal/backend/config.go`, add the field to `Config`:

```go
	Dashboard         DashboardConfig          `yaml:"dashboard"`
```

Add the struct near `WizardConfig`:

```go
// DashboardConfig wires the optional VPS-side admin dashboard.
// When Enabled is false, dashboard routes are not registered. When Enabled
// is true, TokenFile must exist and contain a non-empty token.
type DashboardConfig struct {
	Enabled   bool   `yaml:"enabled"`
	TokenFile string `yaml:"token_file"`
	Token     string `yaml:"-"`
}
```

In `LoadConfig`, after wizard token loading, add:

```go
	if cfg.Dashboard.Enabled {
		if strings.TrimSpace(cfg.Dashboard.TokenFile) == "" {
			return nil, fmt.Errorf("dashboard.token_file is required when dashboard.enabled=true")
		}
		b, err := os.ReadFile(cfg.Dashboard.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("read dashboard.token_file: %w", err)
		}
		cfg.Dashboard.Token = strings.TrimSpace(string(b))
		if cfg.Dashboard.Token == "" {
			return nil, fmt.Errorf("dashboard.token_file is empty")
		}
	}
```

- [ ] **Step 4: Run config tests and verify GREEN**

Run:

```powershell
go test ./internal/backend -run TestLoadConfigDashboard -count=1
```

Expected: all `TestLoadConfigDashboard*` tests pass.

- [ ] **Step 5: Commit config task**

Run:

```powershell
git add internal/backend/config.go internal/backend/config_test.go
git commit -m "feat: add dashboard config"
```

## Task 2: Dashboard Auth and Route Registration

**Files:**
- Modify: `internal/backend/handler.go`
- Create: `internal/backend/dashboard_handler.go`
- Create: `internal/backend/dashboard_handler_test.go`
- Modify: `cmd/backend/main.go`

- [ ] **Step 1: Write failing auth and route tests**

Create `internal/backend/dashboard_handler_test.go` with:

```go
package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDashboardRoutesAbsentWhenTokenEmpty(t *testing.T) {
	h := NewMux(Deps{})
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dashboard route must be absent when token is empty, got %d", rec.Code)
	}
}

func TestDashboardAuthRejectsMissingAndWrongBearer(t *testing.T) {
	h := DashboardAuthMiddleware("expected-token", nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name string
		auth string
	}{
		{name: "missing"},
		{name: "wrong", auth: "Bearer wrong"},
		{name: "empty", auth: "Bearer "},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d", tc.name, rec.Code)
		}
	}
}

func TestDashboardAuthAcceptsRightBearer(t *testing.T) {
	h := DashboardAuthMiddleware("expected-token", nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run auth tests and verify RED**

Run:

```powershell
go test ./internal/backend -run "TestDashboardRoutesAbsent|TestDashboardAuth" -count=1
```

Expected: build fails because `DashboardAuthMiddleware` and `Deps.DashboardToken` do not exist.

- [ ] **Step 3: Add dashboard auth middleware and deps**

In `internal/backend/handler.go`, add to `Deps`:

```go
	DashboardToken    string
```

In `internal/backend/dashboard_handler.go`, add:

```go
package backend

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

func DashboardAuthMiddleware(expected string, logger *slog.Logger) func(http.Handler) http.Handler {
	logReject := func(r *http.Request, reason string) {
		if logger == nil {
			return
		}
		logger.Warn("dashboard auth: rejected", "reason", reason, "remote", r.RemoteAddr, "path", r.URL.Path)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(hdr, prefix) {
				logReject(r, "missing-bearer")
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
				return
			}
			presented := strings.TrimPrefix(hdr, prefix)
			if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
				logReject(r, "token-mismatch")
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

In `internal/backend/handler.go`, register dashboard routes only when the token is present:

```go
	if d.DashboardToken != "" {
		registerDashboardRoutes(mux, d)
	}
```

Add a temporary `registerDashboardRoutes` in `dashboard_handler.go`:

```go
func registerDashboardRoutes(mux *http.ServeMux, d Deps) {
	auth := DashboardAuthMiddleware(d.DashboardToken, d.Logger)
	reqID := requestIDMiddleware()
	mux.Handle("GET /v1/dashboard/summary", reqID(auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"backend":{"status":"ok","version":"` + serverVersion + `","dashboard_enabled":true},"agents":[],"incidents":[]}`))
	}))))
}
```

In `cmd/backend/main.go`, pass the token:

```go
		DashboardToken:    cfg.Dashboard.Token,
```

- [ ] **Step 4: Run auth tests and verify GREEN**

Run:

```powershell
go test ./internal/backend -run "TestDashboardRoutesAbsent|TestDashboardAuth" -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit auth task**

Run:

```powershell
git add internal/backend/handler.go internal/backend/dashboard_handler.go internal/backend/dashboard_handler_test.go cmd/backend/main.go
git commit -m "feat: gate dashboard routes"
```

## Task 3: Dashboard Summary API

**Files:**
- Modify: `internal/backend/dashboard_handler.go`
- Modify: `internal/backend/dashboard_handler_test.go`

- [ ] **Step 1: Write failing summary test**

Append to `internal/backend/dashboard_handler_test.go`:

```go
func TestDashboardSummaryIncludesAgentsAndActiveIncidents(t *testing.T) {
	dbPath := t.TempDir() + "/state.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := d.Users().Insert("alyaba", "tok", "1.2.3.4", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("alyaba", db.DeployInfo{
		Arch: "arm64", PendingVersion: "v0.13.1",
		PendingSince: "2026-06-10T10:00:00Z", DeployMode: "awgm", AWGMURL: "https://awg.example.test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateLastSeenAgentVersion(uid, "v0.13.0"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := d.SQL().Exec(`UPDATE users SET last_seen_at = ? WHERE id = ?`, now, uid); err != nil {
		t.Fatal(err)
	}
	hardSince := now.Add(-10 * time.Minute)
	if err := d.State().Save(uid, "dns", db.IncidentState{
		UserID: uid, CheckName: "dns", CurrentStatus: "hard", HardSince: &hardSince, ConsecutiveFails: 4,
	}); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, DashboardToken: "dash"})
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer dash")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got dashboardSummary
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Backend.Version == "" || !got.Backend.DashboardEnabled {
		t.Fatalf("backend summary missing: %+v", got.Backend)
	}
	if len(got.Agents) != 1 || got.Agents[0].Nickname != "alyaba" {
		t.Fatalf("agents missing: %+v", got.Agents)
	}
	if !got.Agents[0].Online || got.Agents[0].AgentVersion != "v0.13.0" ||
		got.Agents[0].PendingVersion != "v0.13.1" || !got.Agents[0].AWGMURLConfigured ||
		got.Agents[0].ActiveHardCount != 1 {
		t.Fatalf("agent summary wrong: %+v", got.Agents[0])
	}
	if len(got.Incidents) != 1 || got.Incidents[0].Nickname != "alyaba" ||
		got.Incidents[0].CheckName != "dns" || got.Incidents[0].FailCount != 4 {
		t.Fatalf("incidents wrong: %+v", got.Incidents)
	}
}
```

Add imports to the test file:

```go
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)
```

- [ ] **Step 2: Run summary test and verify RED**

Run:

```powershell
go test ./internal/backend -run TestDashboardSummaryIncludesAgentsAndActiveIncidents -count=1
```

Expected: build fails because `dashboardSummary` does not exist or the stub returns no agents.

- [ ] **Step 3: Implement summary types and handler**

In `internal/backend/dashboard_handler.go`, add:

```go
import (
	"encoding/json"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

const dashboardOnlineWindow = 5 * time.Minute

type dashboardSummary struct {
	Backend   dashboardBackend    `json:"backend"`
	Agents    []dashboardAgent    `json:"agents"`
	Incidents []dashboardIncident `json:"incidents"`
}

type dashboardBackend struct {
	Status           string `json:"status"`
	Version          string `json:"version"`
	DashboardEnabled bool   `json:"dashboard_enabled"`
}

type dashboardAgent struct {
	Nickname            string     `json:"nickname"`
	Kind                string     `json:"kind"`
	Online              bool       `json:"online"`
	LastSeenAt          *time.Time `json:"last_seen_at,omitempty"`
	AgentVersion        string     `json:"agent_version"`
	LastDeployedVersion string     `json:"last_deployed_version"`
	PendingVersion      string     `json:"pending_version"`
	PendingSince        string     `json:"pending_since"`
	DeployMode          string     `json:"deploy_mode"`
	AWGMURLConfigured   bool       `json:"awgm_url_configured"`
	HasTopic            bool       `json:"has_topic"`
	ActiveHardCount     int        `json:"active_hard_count"`
}

type dashboardIncident struct {
	Nickname  string    `json:"nickname"`
	CheckName string    `json:"check_name"`
	HardSince time.Time `json:"hard_since"`
	FailCount int       `json:"fail_count"`
}

func dashboardSummaryHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if d.DB == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "db not configured")
			return
		}
		out, err := buildDashboardSummary(d.DB, time.Now().UTC())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		out.Backend = dashboardBackend{Status: "ok", Version: serverVersion, DashboardEnabled: true}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func buildDashboardSummary(d *db.DB, now time.Time) (dashboardSummary, error) {
	users, err := d.Users().GetAll()
	if err != nil {
		return dashboardSummary{}, err
	}
	active, err := d.State().AllActiveHard()
	if err != nil {
		return dashboardSummary{}, err
	}
	nickByID := make(map[int64]string, len(users))
	hardCountByID := make(map[int64]int)
	for _, u := range users {
		nickByID[u.ID] = u.Nickname
	}
	for _, inc := range active {
		hardCountByID[inc.UserID]++
	}
	out := dashboardSummary{Agents: make([]dashboardAgent, 0, len(users)), Incidents: make([]dashboardIncident, 0, len(active))}
	for _, u := range users {
		a := dashboardAgent{
			Nickname: u.Nickname,
			Kind:     u.Kind,
			HasTopic: u.TelegramThreadID != nil,
			Online:   u.LastSeenAt != nil && now.Sub(u.LastSeenAt.UTC()) <= dashboardOnlineWindow,
			LastSeenAt: u.LastSeenAt,
			ActiveHardCount: hardCountByID[u.ID],
		}
		if u.LastDeployedVersion != nil {
			a.LastDeployedVersion = *u.LastDeployedVersion
		}
		if u.PendingVersion != nil {
			a.PendingVersion = *u.PendingVersion
		}
		if u.PendingSince != nil {
			a.PendingSince = *u.PendingSince
		}
		if u.DeployMode != nil {
			a.DeployMode = *u.DeployMode
		}
		if u.AWGMURL != nil && *u.AWGMURL != "" {
			a.AWGMURLConfigured = true
		}
		a.AgentVersion = a.LastDeployedVersion
		out.Agents = append(out.Agents, a)
	}
	for _, inc := range active {
		out.Incidents = append(out.Incidents, dashboardIncident{
			Nickname: nickByID[inc.UserID], CheckName: inc.CheckName, HardSince: inc.HardSince, FailCount: inc.FailCount,
		})
	}
	return out, nil
}
```

In the current DB model, the latest reported agent version is persisted in `users.last_deployed_version` by `UpdateLastSeenAgentVersionResult`, so the dashboard's `agent_version` field mirrors `last_deployed_version`.

- [ ] **Step 4: Wire summary handler**

In `registerDashboardRoutes`, replace the stub with:

```go
	mux.Handle("GET /v1/dashboard/summary", reqID(auth(http.HandlerFunc(dashboardSummaryHandler(d)))))
```

- [ ] **Step 5: Run summary test and verify GREEN**

Run:

```powershell
go test ./internal/backend -run TestDashboardSummaryIncludesAgentsAndActiveIncidents -count=1
```

Expected: test passes with `agent_version` mirroring `last_deployed_version`.

- [ ] **Step 6: Commit summary task**

Run:

```powershell
git add internal/backend/dashboard_handler.go internal/backend/dashboard_handler_test.go
git commit -m "feat: add dashboard summary api"
```

## Task 4: Dashboard Command, Deploy, and Result APIs

**Files:**
- Modify: `internal/backend/dashboard_handler.go`
- Modify: `internal/backend/dashboard_handler_test.go`

- [ ] **Step 1: Write failing command allowlist tests**

Append tests:

```go
func TestDashboardCommandAllowsSafeAction(t *testing.T) {
	d, uid := newDashboardTestDB(t, "alyaba")
	q := cmd.New()
	h := NewMux(Deps{DB: d, CommandSink: q, DashboardToken: "dash"})
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/alyaba/commands",
		strings.NewReader(`{"action":"route_status","args":{"check_name":"_dashboard"}}`))
	req.Header.Set("Authorization", "Bearer dash")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	cmd, ok := q.Dequeue(context.Background(), uid, 0)
	if !ok || cmd.Action != "route_status" {
		t.Fatalf("queued command=%+v ok=%v", cmd, ok)
	}
}

func TestDashboardCommandRejectsDangerousAction(t *testing.T) {
	d, _ := newDashboardTestDB(t, "alyaba")
	q := cmd.New()
	h := NewMux(Deps{DB: d, CommandSink: q, DashboardToken: "dash"})
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/alyaba/commands",
		strings.NewReader(`{"action":"self_update","args":{"version":"v0.13.1"}}`))
	req.Header.Set("Authorization", "Bearer dash")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardServiceRestartAllowsOnlyAwgManager(t *testing.T) {
	d, _ := newDashboardTestDB(t, "alyaba")
	q := cmd.New()
	h := NewMux(Deps{DB: d, CommandSink: q, DashboardToken: "dash"})
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/alyaba/commands",
		strings.NewReader(`{"action":"service_restart","args":{"name":"router"}}`))
	req.Header.Set("Authorization", "Bearer dash")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
```

Add helper:

```go
func newDashboardTestDB(t *testing.T, nickname string) (*db.DB, int64) {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	uid, err := d.Users().Insert(nickname, "tok", "1.2.3.4", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	return d, uid
}
```

- [ ] **Step 2: Run command tests and verify RED**

Run:

```powershell
go test ./internal/backend -run "TestDashboardCommand|TestDashboardServiceRestart" -count=1
```

Expected: 404 or unsupported route failures.

- [ ] **Step 3: Implement command endpoint**

Add request type and allowlist:

```go
type dashboardCommandReq struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args"`
}

var dashboardCommandAllowlist = map[string]bool{
	"diag_now":         true,
	"force_recheck":    true,
	"check_via_tunnel": true,
	"check_direct":     true,
	"pingcheck_now":    true,
	"pingcheck_status": true,
	"router_doctor":    true,
	"route_status":     true,
	"tunnels_status":   true,
	"service_restart":  true,
}
```

Implement:

```go
func dashboardCommandHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if d.CommandSink == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "command sink not configured")
			return
		}
		nickname := r.PathValue("nickname")
		if !requireJSONContentType(w, r) {
			return
		}
		var req dashboardCommandReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.Action = strings.TrimSpace(req.Action)
		if !dashboardCommandAllowlist[req.Action] || !wire.IsValidCommandAction(req.Action) {
			writeJSONError(w, http.StatusBadRequest, "unsupported_command", "action is not allowed for dashboard command dispatch")
			return
		}
		if req.Action == "service_restart" {
			name, _ := req.Args["name"].(string)
			if strings.TrimSpace(name) != "awgmgr" {
				writeJSONError(w, http.StatusBadRequest, "unsupported_maintenance", "service_restart is limited to awgmgr")
				return
			}
		}
		enqueueWizardAgentCommand(w, d, nickname, req.Action, req.Args)
	}
}
```

Register:

```go
	mux.Handle("POST /v1/dashboard/agents/{nickname}/commands", reqID(auth(http.HandlerFunc(dashboardCommandHandler(d)))))
```

- [ ] **Step 4: Add deploy and result endpoint tests**

Add tests mirroring wizard behavior:

```go
func TestDashboardAgentDeployEnqueuesSelfUpdate(t *testing.T) {
	d, uid := newDashboardTestDB(t, "alyaba")
	q := cmd.New()
	h := NewMux(Deps{DB: d, CommandSink: q, DashboardToken: "dash"})
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/alyaba/deploy",
		strings.NewReader(`{"target_version":"v0.13.1"}`))
	req.Header.Set("Authorization", "Bearer dash")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "wgmonitor.example.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	got, ok := q.Dequeue(context.Background(), uid, 0)
	if !ok || got.Action != "self_update" || got.Args["version"] != "v0.13.1" {
		t.Fatalf("queued=%+v ok=%v", got, ok)
	}
	if got.Args["repo_base"] != "https://wgmonitor.example.test/v1/releases/download" {
		t.Fatalf("repo_base=%v", got.Args["repo_base"])
	}
}

func TestDashboardBackendDeployWritesPendingUpdate(t *testing.T) {
	dir := t.TempDir()
	pending := filepath.Join(dir, "backend-update.json")
	h := NewMux(Deps{DashboardToken: "dash", BackendUpdatePath: pending})
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/backend/deploy",
		strings.NewReader(`{"target_version":"v0.13.1"}`))
	req.Header.Set("Authorization", "Bearer dash")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "wgmonitor.example.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(pending)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"target_version": "v0.13.1"`) ||
		!strings.Contains(string(body), `"repo_base": "https://wgmonitor.example.test/v1/releases/download"`) {
		t.Fatalf("pending body wrong:\n%s", string(body))
	}
}
```

- [ ] **Step 5: Implement deploy/result wrappers**

Register wrappers that reuse existing wizard handlers:

```go
	mux.Handle("POST /v1/dashboard/agents/{nickname}/deploy", reqID(auth(http.HandlerFunc(wizardDeployHandler(d)))))
	mux.Handle("POST /v1/dashboard/backend/deploy", reqID(auth(http.HandlerFunc(wizardBackendDeployHandler(d)))))
	mux.Handle("GET /v1/dashboard/cmd/{cmd_id}", reqID(auth(http.HandlerFunc(wizardCmdResultHandler(d)))))
```

Because these handlers read `r.PathValue("nickname")` and `r.PathValue("cmd_id")`, the dashboard patterns must use the same variable names as wizard routes.

- [ ] **Step 6: Run dashboard API tests and verify GREEN**

Run:

```powershell
go test ./internal/backend -run "TestDashboard(Command|ServiceRestart|AgentDeploy|BackendDeploy)" -count=1
```

Expected: tests pass.

- [ ] **Step 7: Commit API task**

Run:

```powershell
git add internal/backend/dashboard_handler.go internal/backend/dashboard_handler_test.go
git commit -m "feat: add dashboard operations api"
```

## Task 5: Embedded Tabler-Based Static Dashboard

**Files:**
- Create: `internal/backend/dashboard_static.go`
- Create: `internal/backend/dashboard_static/index.html`
- Create: `internal/backend/dashboard_static/app.css`
- Create: `internal/backend/dashboard_static/app.js`
- Create: `internal/backend/dashboard_static/vendor/tabler-lite.css`
- Create: `internal/backend/dashboard_static/vendor/tabler-icons-lite.css`
- Modify: `internal/backend/dashboard_handler.go`
- Modify: `internal/backend/dashboard_handler_test.go`

- [ ] **Step 1: Write failing static route test**

Append:

```go
func TestDashboardStaticIndexServedWhenEnabled(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "dash"})
	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"wg-monitor", "tabler-lite.css", "app.js", "Fleet"} {
		if !strings.Contains(body, want) {
			t.Fatalf("index missing %q\n%s", want, body)
		}
	}
}

func TestDashboardStaticAssetsServedWhenEnabled(t *testing.T) {
	h := NewMux(Deps{DashboardToken: "dash"})
	req := httptest.NewRequest(http.MethodGet, "/dashboard/assets/app.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "--wg-ok") {
		t.Fatalf("app css missing operator theme variables")
	}
}
```

- [ ] **Step 2: Run static route tests and verify RED**

Run:

```powershell
go test ./internal/backend -run TestDashboardStatic -count=1
```

Expected: 404 because static dashboard routes are not registered.

- [ ] **Step 3: Add embedded FS**

Create `internal/backend/dashboard_static.go`:

```go
package backend

import "embed"

//go:embed dashboard_static
var dashboardStaticFS embed.FS
```

- [ ] **Step 4: Register static routes**

In `registerDashboardRoutes`, add before API routes:

```go
	mux.Handle("GET /dashboard/", http.HandlerFunc(dashboardIndexHandler))
	mux.Handle("GET /dashboard/assets/", http.StripPrefix("/dashboard/assets/", http.FileServer(http.FS(dashboardAssetsFS()))))
```

Add helpers:

```go
func dashboardIndexHandler(w http.ResponseWriter, r *http.Request) {
	body, err := dashboardStaticFS.ReadFile("dashboard_static/index.html")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func dashboardAssetsFS() fs.FS {
	sub, err := fs.Sub(dashboardStaticFS, "dashboard_static")
	if err != nil {
		return dashboardStaticFS
	}
	return sub
}
```

Add `io/fs` to imports.

- [ ] **Step 5: Add the Tabler-based HTML shell**

Create `internal/backend/dashboard_static/index.html` with local asset links:

```html
<!doctype html>
<html lang="en" data-bs-theme="dark">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>wg-monitor dashboard</title>
    <link rel="stylesheet" href="/dashboard/assets/vendor/tabler-lite.css" />
    <link rel="stylesheet" href="/dashboard/assets/vendor/tabler-icons-lite.css" />
    <link rel="stylesheet" href="/dashboard/assets/app.css" />
  </head>
  <body>
    <div class="page wg-shell">
      <header class="navbar navbar-expand-md d-print-none wg-navbar">
        <div class="container-xl">
          <div class="navbar-brand wg-brand">
            <span class="wg-brand-mark">WG</span>
            <span>wg-monitor</span>
          </div>
          <div class="navbar-nav flex-row order-md-last ms-auto">
            <span id="backend-version" class="badge bg-blue-lt text-blue-lt-fg">backend unknown</span>
            <span id="connection-state" class="badge bg-yellow-lt text-yellow-lt-fg">locked</span>
          </div>
        </div>
      </header>
      <div class="page-wrapper">
        <div class="page-body">
          <div class="container-xl">
            <div id="login-panel" class="wg-login">
              <div class="card wg-login-card">
                <div class="card-body">
                  <h1 class="h2">Dashboard access</h1>
                  <p class="text-secondary">Enter the dashboard token from the VPS token file.</p>
                  <div class="input-group">
                    <input id="token-input" type="password" class="form-control" autocomplete="current-password" placeholder="Dashboard token" />
                    <button id="save-token" class="btn btn-primary" type="button"><i class="ti ti-lock-open"></i>Unlock</button>
                  </div>
                  <div id="login-error" class="text-danger mt-3 d-none"></div>
                </div>
              </div>
            </div>
            <main id="dashboard-app" class="d-none">
              <div class="wg-toolbar">
                <div>
                  <h1 class="page-title">Fleet control</h1>
                  <div id="last-refresh" class="text-secondary">not refreshed yet</div>
                </div>
                <div class="btn-list">
                  <button class="btn btn-outline-light" id="refresh-now"><i class="ti ti-refresh"></i>Refresh</button>
                  <button class="btn btn-primary" id="backend-deploy"><i class="ti ti-cloud-upload"></i>Deploy backend</button>
                </div>
              </div>
              <div class="wg-filter-row" id="fleet-filters">
                <button class="btn btn-sm btn-primary" data-filter="all">All</button>
                <button class="btn btn-sm btn-outline-light" data-filter="offline">Offline</button>
                <button class="btn btn-sm btn-outline-light" data-filter="hard">Incidents</button>
                <button class="btn btn-sm btn-outline-light" data-filter="pending">Pending</button>
              </div>
              <div class="row row-deck row-cards">
                <section class="col-12 col-xl-8">
                  <div class="card wg-panel">
                    <div class="table-responsive">
                      <table class="table table-vcenter card-table wg-fleet-table">
                        <thead><tr><th>Router</th><th>Status</th><th>Version</th><th>Deploy</th><th>Last seen</th><th></th></tr></thead>
                        <tbody id="fleet-body"></tbody>
                      </table>
                    </div>
                  </div>
                </section>
                <aside class="col-12 col-xl-4">
                  <div class="card wg-panel">
                    <div class="card-body">
                      <h2 id="drawer-title" class="h3">Select router</h2>
                      <div id="router-drawer" class="wg-drawer-empty text-secondary">Choose a router from the fleet table.</div>
                    </div>
                  </div>
                  <div class="card wg-panel mt-3">
                    <div class="card-body">
                      <h2 class="h3">Command results</h2>
                      <div id="command-log" class="wg-command-log text-secondary">No commands in this browser session.</div>
                    </div>
                  </div>
                </aside>
              </div>
            </main>
          </div>
        </div>
      </div>
    </div>
    <div id="toast-root" class="toast-container position-fixed top-0 end-0 p-3"></div>
    <script src="/dashboard/assets/app.js"></script>
  </body>
</html>
```

- [ ] **Step 6: Add local Tabler-compatible CSS and icons**

Create `vendor/tabler-lite.css` with the subset classes used above:

```css
:root{--tblr-primary:#206bc4;--tblr-success:#2fb344;--tblr-warning:#f59f00;--tblr-danger:#d63939;--tblr-info:#4299e1;--tblr-body-bg:#f6f8fb;--tblr-body-color:#182433;--tblr-border-color:#dadfe5;--tblr-card-bg:#fff}
[data-bs-theme=dark]{--tblr-body-bg:#10141c;--tblr-body-color:#e6edf6;--tblr-border-color:#263241;--tblr-card-bg:#151b24}
*{box-sizing:border-box}body{margin:0;background:var(--tblr-body-bg);color:var(--tblr-body-color);font:14px/1.45 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}.page{min-height:100vh}.container-xl{width:min(1320px,100%);margin:0 auto;padding:0 24px}.navbar{min-height:64px;border-bottom:1px solid var(--tblr-border-color);display:flex;align-items:center}.navbar .container-xl{display:flex;align-items:center;gap:16px}.navbar-brand{font-weight:700;display:flex;align-items:center;gap:10px}.ms-auto{margin-left:auto}.page-body{padding:24px 0}.row{display:flex;flex-wrap:wrap;margin:-8px}.row>*{padding:8px}.col-12{width:100%}.col-xl-8,.col-xl-4{width:100%}@media(min-width:1200px){.col-xl-8{width:66.666%}.col-xl-4{width:33.333%}}.card{background:var(--tblr-card-bg);border:1px solid var(--tblr-border-color);border-radius:8px;box-shadow:0 10px 30px rgba(0,0,0,.14)}.card-body{padding:18px}.table-responsive{overflow:auto}.table{width:100%;border-collapse:collapse}.table th,.table td{padding:12px;border-bottom:1px solid var(--tblr-border-color);text-align:left;vertical-align:middle}.table th{font-size:12px;text-transform:uppercase;letter-spacing:.04em;color:#7c8b9c}.badge{display:inline-flex;align-items:center;gap:6px;border-radius:999px;padding:3px 9px;font-size:12px;font-weight:700}.bg-success-lt{background:rgba(47,179,68,.14);color:#5dd675}.bg-warning-lt{background:rgba(245,159,0,.16);color:#ffc861}.bg-danger-lt{background:rgba(214,57,57,.16);color:#ff7b7b}.bg-blue-lt{background:rgba(32,107,196,.18);color:#7eb6ff}.bg-yellow-lt{background:rgba(245,159,0,.18);color:#ffd27a}.btn{border:1px solid var(--tblr-border-color);border-radius:7px;background:transparent;color:var(--tblr-body-color);display:inline-flex;align-items:center;gap:7px;padding:8px 12px;cursor:pointer;font-weight:650}.btn:hover{filter:brightness(1.08)}.btn-primary{background:var(--tblr-primary);border-color:var(--tblr-primary);color:white}.btn-outline-light{border-color:#3a4658;color:#d8e2ee}.btn-sm{padding:5px 9px;font-size:12px}.btn-list{display:flex;gap:8px;flex-wrap:wrap}.input-group{display:flex}.form-control{width:100%;border:1px solid var(--tblr-border-color);border-radius:7px 0 0 7px;background:#0d121a;color:var(--tblr-body-color);padding:10px 12px}.input-group .btn{border-radius:0 7px 7px 0}.text-secondary{color:#8492a6}.text-danger{color:#ff7b7b}.d-none{display:none!important}.h2{font-size:24px}.h3{font-size:18px}.page-title{font-size:28px;margin:0}.mt-3{margin-top:16px}.position-fixed{position:fixed}.top-0{top:0}.end-0{right:0}.p-3{padding:16px}.toast-container{z-index:20}.toast{background:var(--tblr-card-bg);border:1px solid var(--tblr-border-color);border-radius:8px;padding:12px;margin-bottom:8px;box-shadow:0 10px 30px rgba(0,0,0,.25)}
```

Create `vendor/tabler-icons-lite.css`:

```css
.ti{display:inline-block;width:1.1em;height:1.1em;line-height:1;vertical-align:-.15em}
.ti::before{font-style:normal}
.ti-refresh::before{content:"↻"}.ti-cloud-upload::before{content:"⇧"}.ti-lock-open::before{content:"⎋"}.ti-stethoscope::before{content:"⌁"}.ti-route::before{content:"⇄"}.ti-activity::before{content:"▧"}.ti-bolt::before{content:"ϟ"}.ti-player-play::before{content:"▶"}.ti-server::before{content:"▦"}.ti-shield-check::before{content:"✓"}
```

- [ ] **Step 7: Add operator theme CSS**

Create `app.css`:

```css
:root{--wg-ok:#4ade80;--wg-warn:#facc15;--wg-bad:#fb7185;--wg-info:#38bdf8;--wg-rail:#334155}
.wg-shell{background:radial-gradient(circle at top left,rgba(56,189,248,.12),transparent 34rem),linear-gradient(180deg,#0c111a,#121826 55%,#0c111a)}
.wg-navbar{background:rgba(12,17,26,.82);backdrop-filter:blur(14px)}
.wg-brand-mark{display:inline-grid;place-items:center;width:34px;height:34px;border-radius:8px;background:linear-gradient(135deg,#38bdf8,#4ade80);color:#06101c;font-weight:900}
.wg-login{min-height:calc(100vh - 120px);display:grid;place-items:center}.wg-login-card{width:min(560px,100%)}
.wg-toolbar{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:18px}.wg-filter-row{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:14px}
.wg-panel{background:rgba(21,27,36,.92)}.wg-fleet-table tr{cursor:pointer}.wg-fleet-table tbody tr:hover{background:rgba(56,189,248,.08)}
.wg-router-main{display:flex;align-items:center;gap:10px}.wg-status-dot{width:9px;height:36px;border-radius:99px;background:var(--wg-rail)}.wg-status-dot.ok{background:var(--wg-ok)}.wg-status-dot.warn{background:var(--wg-warn)}.wg-status-dot.bad{background:var(--wg-bad)}
.wg-action-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}.wg-action-grid .btn{justify-content:center}.wg-command-log{display:grid;gap:8px;max-height:420px;overflow:auto}.wg-command-entry{border:1px solid var(--tblr-border-color);border-radius:8px;padding:10px;background:rgba(0,0,0,.12)}.wg-command-output{white-space:pre-wrap;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px;margin-top:8px;color:#cbd5e1}
@media(max-width:720px){.container-xl{padding:0 14px}.wg-toolbar{align-items:flex-start;flex-direction:column}.wg-action-grid{grid-template-columns:1fr}.table th:nth-child(4),.table td:nth-child(4){display:none}}
```

- [ ] **Step 8: Add JavaScript app**

Create `app.js` with:

```javascript
const state = { token: localStorage.getItem("wgDashboardToken") || "", summary: null, selected: null, filter: "all", commands: [] };
const $ = (id) => document.getElementById(id);

function authHeaders() { return state.token ? { Authorization: `Bearer ${state.token}` } : {}; }
function show(el, yes) { el.classList.toggle("d-none", !yes); }
function toast(text, tone = "info") {
  const node = document.createElement("div");
  node.className = "toast";
  node.textContent = text;
  $("toast-root").appendChild(node);
  setTimeout(() => node.remove(), 4200);
}
async function api(path, opts = {}) {
  const headers = { ...authHeaders(), ...(opts.headers || {}) };
  const res = await fetch(path, { ...opts, headers });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status}: ${text || res.statusText}`);
  }
  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}
function statusBadge(agent) {
  if (agent.active_hard_count > 0) return `<span class="badge bg-danger-lt">hard ${agent.active_hard_count}</span>`;
  if (agent.online) return `<span class="badge bg-success-lt">online</span>`;
  return `<span class="badge bg-warning-lt">offline</span>`;
}
function rowVisible(agent) {
  if (state.filter === "offline") return !agent.online;
  if (state.filter === "hard") return agent.active_hard_count > 0;
  if (state.filter === "pending") return Boolean(agent.pending_version);
  return true;
}
function renderFleet() {
  const body = $("fleet-body");
  const agents = (state.summary?.agents || []).filter(rowVisible);
  body.innerHTML = agents.map((a) => {
    const rail = a.active_hard_count > 0 ? "bad" : a.online ? "ok" : "warn";
    return `<tr data-nick="${a.nickname}">
      <td><div class="wg-router-main"><span class="wg-status-dot ${rail}"></span><div><strong>${a.nickname}</strong><div class="text-secondary">${a.kind || "static"}</div></div></div></td>
      <td>${statusBadge(a)}</td>
      <td><span class="badge bg-blue-lt">${a.agent_version || a.last_deployed_version || "unknown"}</span></td>
      <td>${a.pending_version ? `<span class="badge bg-warning-lt">${a.pending_version}</span>` : `<span class="text-secondary">${a.deploy_mode || "n/a"}</span>`}</td>
      <td class="text-secondary">${a.last_seen_at ? new Date(a.last_seen_at).toLocaleString() : "never"}</td>
      <td><button class="btn btn-sm btn-outline-light" data-select="${a.nickname}">Open</button></td>
    </tr>`;
  }).join("") || `<tr><td colspan="6" class="text-secondary">No routers match this filter.</td></tr>`;
  body.querySelectorAll("[data-select]").forEach((btn) => btn.addEventListener("click", () => selectRouter(btn.dataset.select)));
}
function selectRouter(nick) {
  state.selected = (state.summary?.agents || []).find((a) => a.nickname === nick) || null;
  renderDrawer();
}
function renderDrawer() {
  const a = state.selected;
  $("drawer-title").textContent = a ? a.nickname : "Select router";
  if (!a) {
    $("router-drawer").className = "wg-drawer-empty text-secondary";
    $("router-drawer").textContent = "Choose a router from the fleet table.";
    return;
  }
  $("router-drawer").className = "";
  $("router-drawer").innerHTML = `<div class="mb-3">${statusBadge(a)} ${a.awgm_url_configured ? '<span class="badge bg-blue-lt">AWG URL</span>' : ''}</div>
    <div class="wg-action-grid">
      <button class="btn btn-primary" data-action="diag_now"><i class="ti ti-stethoscope"></i>Diagnostics</button>
      <button class="btn btn-outline-light" data-action="force_recheck"><i class="ti ti-refresh"></i>Recheck</button>
      <button class="btn btn-outline-light" data-action="route_status"><i class="ti ti-route"></i>Routes</button>
      <button class="btn btn-outline-light" data-action="tunnels_status"><i class="ti ti-activity"></i>Tunnels</button>
      <button class="btn btn-outline-light" data-action="pingcheck_status"><i class="ti ti-activity"></i>PingCheck</button>
      <button class="btn btn-outline-light" data-action="check_via_tunnel"><i class="ti ti-bolt"></i>Via</button>
      <button class="btn btn-outline-light" data-action="check_direct"><i class="ti ti-bolt"></i>Direct</button>
      <button class="btn btn-outline-light" data-action="service_restart" data-name="awgmgr"><i class="ti ti-server"></i>AWG restart</button>
    </div>
    <div class="input-group mt-3"><input id="agent-version" class="form-control" placeholder="v0.13.0" /><button id="deploy-agent" class="btn btn-primary"><i class="ti ti-cloud-upload"></i>Deploy agent</button></div>`;
  $("router-drawer").querySelectorAll("[data-action]").forEach((btn) => btn.addEventListener("click", () => runCommand(btn.dataset.action, btn.dataset.name)));
  $("deploy-agent").addEventListener("click", deployAgent);
}
function renderCommands() {
  $("command-log").innerHTML = state.commands.map((c) => `<div class="wg-command-entry"><strong>${c.nickname}</strong> ${c.action} <span class="badge bg-blue-lt">${c.status}</span><div class="wg-command-output">${c.output || ""}</div></div>`).join("") || "No commands in this browser session.";
}
async function refreshSummary() {
  state.summary = await api("/v1/dashboard/summary");
  $("backend-version").textContent = `backend ${state.summary.backend.version}`;
  $("connection-state").textContent = "connected";
  $("last-refresh").textContent = `refreshed ${new Date().toLocaleTimeString()}`;
  renderFleet();
  if (state.selected) selectRouter(state.selected.nickname);
}
async function runCommand(action, serviceName) {
  const nickname = state.selected?.nickname;
  if (!nickname) return;
  const args = action === "service_restart" ? { name: serviceName } : { check_name: "_dashboard" };
  const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/commands`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action, args }) });
  const entry = { nickname, action, status: "queued", output: "", cmd_id: res.cmd_id };
  state.commands.unshift(entry);
  renderCommands();
  pollCommand(entry);
}
async function pollCommand(entry) {
  try {
    const res = await api(`/v1/dashboard/cmd/${encodeURIComponent(entry.cmd_id)}?nickname=${encodeURIComponent(entry.nickname)}&wait_sec=30`);
    entry.status = res.status;
    entry.output = res.output || "";
    renderCommands();
  } catch (err) {
    entry.status = "waiting";
    renderCommands();
    setTimeout(() => pollCommand(entry), 3000);
  }
}
async function deployAgent() {
  const version = $("agent-version").value.trim();
  if (!version || !state.selected) return toast("Enter target version", "warn");
  const res = await api(`/v1/dashboard/agents/${encodeURIComponent(state.selected.nickname)}/deploy`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ target_version: version }) });
  state.commands.unshift({ nickname: state.selected.nickname, action: "self_update", status: "queued", output: "", cmd_id: res.cmd_id });
  renderCommands();
}
async function unlock() {
  state.token = $("token-input").value.trim();
  localStorage.setItem("wgDashboardToken", state.token);
  try {
    await refreshSummary();
    show($("login-panel"), false);
    show($("dashboard-app"), true);
  } catch (err) {
    $("login-error").textContent = err.message;
    show($("login-error"), true);
  }
}
$("save-token").addEventListener("click", unlock);
$("refresh-now").addEventListener("click", () => refreshSummary().catch((err) => toast(err.message)));
$("fleet-filters").querySelectorAll("[data-filter]").forEach((btn) => btn.addEventListener("click", () => { state.filter = btn.dataset.filter; renderFleet(); }));
if (state.token) unlock();
setInterval(() => state.token && !$("dashboard-app").classList.contains("d-none") && refreshSummary().catch(() => {}), 30000);
```

- [ ] **Step 9: Run static tests and verify GREEN**

Run:

```powershell
go test ./internal/backend -run TestDashboardStatic -count=1
```

Expected: static tests pass.

- [ ] **Step 10: Commit static UI task**

Run:

```powershell
git add internal/backend/dashboard_static.go internal/backend/dashboard_handler.go internal/backend/dashboard_handler_test.go internal/backend/dashboard_static
git commit -m "feat: add embedded dashboard ui"
```

## Task 6: Deploy Template and Docs

**Files:**
- Modify: `cmd/deploy/templates.go`
- Modify: `cmd/deploy/templates/backend.yaml.tmpl`
- Modify: `cmd/deploy/templates_test.go`
- Modify: `README.md`
- Modify: `DEPLOY.md`

- [ ] **Step 1: Write failing template test**

In `cmd/deploy/templates_test.go`, extend `TestRenderBackendYAML` expected strings with:

```go
		`dashboard:`,
		`enabled: false`,
		`token_file: /etc/wg-monitor/dashboard-token.txt`,
```

- [ ] **Step 2: Run template test and verify RED**

Run:

```powershell
go test ./cmd/deploy -run TestRenderBackendYAML -count=1
```

Expected: test fails because backend YAML does not mention dashboard config.

- [ ] **Step 3: Add dashboard config to backend template**

Append to `cmd/deploy/templates/backend.yaml.tmpl` after the wizard block:

```yaml

# VPS-side web dashboard. Disabled by default. To enable it, create
# /etc/wg-monitor/dashboard-token.txt with a strong random token, restrict
# file permissions, set enabled: true, and restart wg-monitor-backend.
dashboard:
  enabled: false
  token_file: /etc/wg-monitor/dashboard-token.txt
```

- [ ] **Step 4: Run template test and verify GREEN**

Run:

```powershell
go test ./cmd/deploy -run TestRenderBackendYAML -count=1
```

Expected: template test passes.

- [ ] **Step 5: Document dashboard enablement**

Add a `Dashboard` section to `README.md` and `DEPLOY.md`:

```markdown
## VPS Dashboard

The backend can serve an admin-only web dashboard at `/dashboard/`.
It is disabled by default.

To enable it on the VPS:

1. Create `/etc/wg-monitor/dashboard-token.txt` with a strong random token.
2. Restrict the file to the backend user or group.
3. Set `dashboard.enabled: true` in `backend.yaml`.
4. Restart `wg-monitor-backend`.
5. Open `https://<backend-domain>/dashboard/` and paste the token.

The dashboard reuses the backend command queue and deploy endpoints. It does
not expose bot tokens, wizard tokens, agent tokens, AWG Manager credentials,
or backup passphrases.
```

- [ ] **Step 6: Commit template/docs task**

Run:

```powershell
git add cmd/deploy/templates/backend.yaml.tmpl cmd/deploy/templates_test.go README.md DEPLOY.md
git commit -m "docs: document vps dashboard enablement"
```

## Task 7: Browser Verification and Final Checks

**Files:**
- No production edits required unless verification finds a UI bug.

- [ ] **Step 1: Run focused tests**

Run:

```powershell
go test ./internal/backend ./cmd/backend ./cmd/deploy -count=1
```

Expected: all tests pass.

- [ ] **Step 2: Run full tests**

Run:

```powershell
go test ./... -count=1
```

Expected: all tests pass.

- [ ] **Step 3: Run diff whitespace check**

Run:

```powershell
git diff --check
```

Expected: no output.

- [ ] **Step 4: Build backend**

Run:

```powershell
go build ./cmd/backend
```

Expected: build succeeds and produces `backend.exe` on Windows.

- [ ] **Step 5: Browser smoke test**

Run a local backend test server or a focused `httptest`-backed preview if available, then open `/dashboard/` in the Browser plugin.

Verify visually:

- Login panel is centered and polished.
- Header shows wg-monitor branding.
- Fleet table is visible and responsive.
- Buttons have icons and fit their containers.
- The router drawer does not overlap the table.
- Mobile-width viewport keeps text readable.
- No external CDN requests are required for CSS, JS, icons, or fonts.

- [ ] **Step 6: Commit verification fixes**

If browser verification requires CSS/JS changes, commit them:

```powershell
git add internal/backend/dashboard_static
git commit -m "fix: polish dashboard ui"
```

If no changes are required, do not create an empty commit.

## Self-Review

Spec coverage:

- Protected dashboard routes: Task 1 and Task 2.
- Summary data: Task 3.
- Safe command actions: Task 4.
- Agent/backend deploy: Task 4.
- Command result polling: Task 4.
- Beautiful Tabler-based static UI: Task 5.
- No CDN runtime dependency: Task 5.
- Deploy template and docs: Task 6.
- Browser visual verification: Task 7.

Placeholder scan:

- This plan intentionally avoids unresolved placeholder text. Agent version display is concrete for the current schema: dashboard `agent_version` mirrors `users.last_deployed_version`, which is updated by heartbeat ingestion.

Type consistency:

- Dashboard config uses `DashboardConfig` and `Config.Dashboard`.
- Backend deps use `Deps.DashboardToken`.
- Summary types use `dashboardSummary`, `dashboardBackend`, `dashboardAgent`, and `dashboardIncident`.
- Command/deploy handlers reuse existing `wire.Command`, `CommandSink`, `wizardDeployHandler`, `wizardBackendDeployHandler`, and `wizardCmdResultHandler`.
