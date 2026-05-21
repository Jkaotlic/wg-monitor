# AWG Manager Bootstrap Deploy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace normal router deploy/re-enroll with AWG Manager over KeenDNS, leaving local router SSH only as a hidden recovery path.

**Architecture:** The deploy wizard creates or rotates backend agent credentials through wizard-auth endpoints, then uses AWG Manager's HTTP API and terminal WebSocket to install the agent inside Entware. Router state stores AWG Manager bootstrap metadata; steady-state updates keep using the existing backend `self_update` pull-flow.

**Tech Stack:** Go, net/http, httptest, Gorilla/websocket for wizard-side terminal client, TOML state, embedded shell templates, existing backend SQLite users repo.

---

## File Structure

- Modify `cmd/deploy/state.go`: add AWG Manager deployment fields to `AgentState`.
- Modify `cmd/deploy/secrets.go`: add AWG Manager credentials to `secrets status`.
- Create `cmd/deploy/awgm_client.go`: small HTTP/WebSocket client for AWG Manager login, terminal lifecycle, terminal command execution.
- Create `cmd/deploy/awgm_bootstrap.go`: render and run the idempotent Entware bootstrap script through AWG Manager.
- Modify `cmd/deploy/templates.go`: expose a shell-safe bootstrap renderer.
- Add `cmd/deploy/templates/awgm-bootstrap.sh.tmpl`: remote Entware install script.
- Modify `cmd/deploy/actions.go`: make `actionAddRouter`, `actionInstallAgent`, token repair, and migration prefer AWG Manager bootstrap instead of local SSH.
- Modify `cmd/deploy/main.go` and `cmd/deploy/menu.go`: remove normal-menu references to local SSH/netfix deployment; keep legacy only behind `WG_LEGACY_ROUTER_SSH=1` or explicit recovery subcommands.
- Modify `cmd/deploy/vps_sync.go`: add enrollment API client and include AWG metadata in future sync payloads where useful.
- Modify `internal/backend/db/users.go`: add token rotate/upsert helpers.
- Modify `internal/backend/wizard_handler.go` and `internal/backend/handler.go`: add wizard enrollment endpoints.
- Add or update tests:
  - `cmd/deploy/state_test.go`
  - `cmd/deploy/secrets_test.go` or `cmd/deploy/secrets_io_test.go`
  - `cmd/deploy/awgm_client_test.go`
  - `cmd/deploy/awgm_bootstrap_test.go`
  - `cmd/deploy/actions_test.go`
  - `internal/backend/db/users_test.go`
  - `internal/backend/wizard_handler_test.go`
  - `cmd/deploy/vps_sync_test.go`

## Task 1: State and Secrets Metadata

**Files:**
- Modify: `cmd/deploy/state.go`
- Modify: `cmd/deploy/state_test.go`
- Modify: `cmd/deploy/secrets.go`

- [ ] **Step 1: Write state round-trip test**

Add to `cmd/deploy/state_test.go`:

```go
func TestAgentStateRoundTripAWGMDeployFields(t *testing.T) {
	st := &State{
		SchemaVersion: CurrentSchemaVersion,
		Agents: []AgentState{{
			Nickname:   "testkeen",
			DeployMode: "awgm",
			AWGMURL:    "https://awg.testkeen.keenetic.pro",
			AWGMAuth:   "router-admin",
		}},
	}
	path := filepath.Join(t.TempDir(), "wizard.toml")
	if err := SaveState(path, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	ag := got.FindAgent("testkeen")
	if ag == nil {
		t.Fatal("agent missing")
	}
	if ag.DeployMode != "awgm" || ag.AWGMURL != "https://awg.testkeen.keenetic.pro" || ag.AWGMAuth != "router-admin" {
		t.Fatalf("AWGM fields lost: %+v", ag)
	}
}
```

- [ ] **Step 2: Run the failing test**

Run: `go test ./cmd/deploy -run TestAgentStateRoundTripAWGMDeployFields -count=1`

Expected: compile failure for missing `DeployMode`, `AWGMURL`, `AWGMAuth`.

- [ ] **Step 3: Add fields**

In `cmd/deploy/state.go`, extend `AgentState`:

```go
DeployMode string `toml:"deploy_mode,omitempty"` // "awgm" default for supported deploy, "legacy_ssh" for break-glass recovery
AWGMURL    string `toml:"awgm_url,omitempty"`    // public AWG Manager base URL, usually KeenDNS web-app URL
AWGMAuth   string `toml:"awgm_auth,omitempty"`   // credential source label only; password lives in SecretStore
```

- [ ] **Step 4: Add secrets status rows**

In `cmd/deploy/secrets.go`, inside the per-agent loop, add:

```go
if strings.EqualFold(ag.DeployMode, "awgm") || ag.AWGMURL != "" {
	add("WG_AWGM_LOGIN_"+suffix, "AWG Manager login for "+ag.Nickname, true)
	add("WG_AWGM_PASS_"+suffix, "AWG Manager password for "+ag.Nickname, true)
}
```

- [ ] **Step 5: Verify**

Run: `go test ./cmd/deploy -run 'TestAgentStateRoundTripAWGMDeployFields|TestSecret' -count=1`

Expected: PASS.

## Task 2: Backend Enrollment Endpoint

**Files:**
- Modify: `internal/backend/db/users.go`
- Modify: `internal/backend/db/users_test.go`
- Modify: `internal/backend/wizard_handler.go`
- Modify: `internal/backend/wizard_handler_test.go`
- Modify: `internal/backend/handler.go`
- Modify: `cmd/deploy/vps_sync.go`
- Modify: `cmd/deploy/vps_sync_test.go`

- [ ] **Step 1: Write DB rotate/upsert tests**

Add tests to `internal/backend/db/users_test.go`:

```go
func TestUpsertEnrollmentCreatesUser(t *testing.T) {
	d := openTestDB(t)
	uid, err := d.Users().UpsertEnrollment("testkeen", "raw-token-1", KindStatic, 406)
	if err != nil {
		t.Fatalf("UpsertEnrollment: %v", err)
	}
	u, err := d.Users().GetByNickname("testkeen")
	if err != nil {
		t.Fatalf("GetByNickname: %v", err)
	}
	if u.ID != uid || u.Kind != KindStatic || u.TelegramThreadID == nil || *u.TelegramThreadID != 406 {
		t.Fatalf("bad user: %+v", u)
	}
	if _, err := d.Users().GetByToken("raw-token-1"); err != nil {
		t.Fatalf("new token rejected: %v", err)
	}
}

func TestUpsertEnrollmentRotatesExistingToken(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Users().InsertWithKind("testkeen", "old-token", "0.0.0.0", "awg0", KindStatic)
	if _, err := d.Users().UpsertEnrollment("testkeen", "new-token", KindMobile, 0); err != nil {
		t.Fatalf("UpsertEnrollment: %v", err)
	}
	if _, err := d.Users().GetByToken("new-token"); err != nil {
		t.Fatalf("new token rejected: %v", err)
	}
	if _, err := d.Users().GetByToken("old-token"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("old token still valid: %v", err)
	}
	u, _ := d.Users().GetByNickname("testkeen")
	if u.Kind != KindMobile {
		t.Fatalf("kind not updated: %s", u.Kind)
	}
}
```

- [ ] **Step 2: Implement `UpsertEnrollment`**

Add to `internal/backend/db/users.go`:

```go
func (u *UsersRepo) UpsertEnrollment(nickname, rawToken, kind string, threadID int64) (int64, error) {
	if !IsValidKind(kind) {
		return 0, fmt.Errorf("users.UpsertEnrollment: invalid kind %q (want static|mobile)", kind)
	}
	tokenHash := hashToken(rawToken)
	res, err := u.d.db.Exec(`
INSERT INTO users(nickname, token_hash, expected_exit_ip, awg_iface, kind, telegram_thread_id)
VALUES (?, ?, '0.0.0.0', 'awg0', ?, NULLIF(?, 0))
ON CONFLICT(nickname) DO UPDATE SET
  token_hash=excluded.token_hash,
  kind=excluded.kind,
  telegram_thread_id=COALESCE(excluded.telegram_thread_id, users.telegram_thread_id)
`, nickname, tokenHash, kind, threadID)
	if err != nil {
		return 0, fmt.Errorf("users.UpsertEnrollment: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil && id != 0 {
		return id, nil
	}
	got, err := u.GetByNickname(nickname)
	if err != nil {
		return 0, err
	}
	return got.ID, nil
}
```

- [ ] **Step 3: Write handler tests**

Add to `internal/backend/wizard_handler_test.go`:

```go
func TestWizardEnrollmentCreatesRawTokenAndUser(t *testing.T) {
	d := newWizardTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/wizard/enrollments", strings.NewReader(`{"nickname":"testkeen","kind":"static","thread_id":406}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	wizardEnrollmentHandler(d).ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got wizardEnrollmentResp
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got.Nickname != "testkeen" || got.RawToken == "" || got.BackendURL == "" {
		t.Fatalf("bad response: %+v", got)
	}
	if _, err := d.DB.Users().GetByToken(got.RawToken); err != nil {
		t.Fatalf("token not registered: %v", err)
	}
}
```

- [ ] **Step 4: Implement handler and route**

Add request/response structs and `wizardEnrollmentHandler` in `internal/backend/wizard_handler.go`. Generate a 32-byte hex raw token using `rand.Read`, default kind to `static`, call `Users().UpsertEnrollment`, and return:

```json
{
  "nickname": "testkeen",
  "backend_url": "https://<host>",
  "raw_token": "<64 hex chars>"
}
```

Register it in `internal/backend/handler.go`:

```go
mux.Handle("POST /v1/wizard/enrollments", reqID(wizAuth(wizardEnrollmentHandler(d))))
```

- [ ] **Step 5: Add VPS client method**

In `cmd/deploy/vps_sync.go`, add:

```go
type EnrollmentRequest struct {
	Nickname string `json:"nickname"`
	Kind     string `json:"kind"`
	ThreadID int64  `json:"thread_id,omitempty"`
}

type EnrollmentResponse struct {
	Nickname   string `json:"nickname"`
	BackendURL string `json:"backend_url"`
	RawToken   string `json:"raw_token"`
}

func (c *VPSClient) CreateEnrollment(ctx context.Context, req EnrollmentRequest) (*EnrollmentResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/wizard/enrollments", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("POST /v1/wizard/enrollments: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out EnrollmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.RawToken == "" || out.BackendURL == "" {
		return nil, fmt.Errorf("backend returned incomplete enrollment")
	}
	return &out, nil
}
```

- [ ] **Step 6: Verify**

Run: `go test ./internal/backend/... ./cmd/deploy -run 'Enrollment|UpsertEnrollment' -count=1`

Expected: PASS.

## Task 3: AWG Manager Client

**Files:**
- Create: `cmd/deploy/awgm_client.go`
- Create: `cmd/deploy/awgm_client_test.go`

- [ ] **Step 1: Write client tests**

Create `cmd/deploy/awgm_client_test.go` with:

```go
func TestAWGMClientLoginStoresSessionCookie(t *testing.T) {
	var sawCookie bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "awg_session", Value: "s1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"login":"admin"}`))
		case "/api/system/info":
			if ck, err := r.Cookie("awg_session"); err == nil && ck.Value == "s1" {
				sawCookie = true
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"goArch":"arm64","routerIP":"192.168.1.1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewAWGMClient(srv.URL, "admin", "secret")
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.SystemInfo(context.Background); err != nil {
		t.Fatalf("SystemInfo: %v", err)
	}
	if !sawCookie {
		t.Fatal("session cookie not sent")
	}
}

func TestAWGMClientTerminalBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/terminal/status" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true,"sessionActive":true}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := NewAWGMClient(srv.URL, "", "")
	st, err := c.TerminalStatus(context.Background())
	if err != nil {
		t.Fatalf("TerminalStatus: %v", err)
	}
	if !st.SessionActive {
		t.Fatalf("expected busy terminal: %+v", st)
	}
}
```

- [ ] **Step 2: Implement HTTP client**

Implement `NewAWGMClient`, `Login`, `Health`, `SystemInfo`, `TerminalInstall`, `TerminalStart`, `TerminalStatus`, and `TerminalStop` in `cmd/deploy/awgm_client.go`.

Use cookie auth exactly like existing `internal/agent/awgmgr.Client`: `POST /api/auth/login` with JSON `{login,password}` and store `awg_session`.

- [ ] **Step 3: Implement terminal WebSocket runner**

Add:

```go
type TerminalRunResult struct {
	Output string
}

func (c *AWGMClient) RunTerminalScript(ctx context.Context, script string) (TerminalRunResult, error)
```

Use `github.com/gorilla/websocket` and subprotocol `tty`. Send terminal input frames as text messages prefixed with `"0"` and the shell text. The script should be sent as:

```sh
cat >/tmp/wg-monitor-bootstrap.sh <<'WG_MONITOR_BOOTSTRAP'
...
WG_MONITOR_BOOTSTRAP
sh /tmp/wg-monitor-bootstrap.sh
echo __WG_MONITOR_DONE__$?
```

Read messages until `__WG_MONITOR_DONE__0` or non-zero marker appears.

- [ ] **Step 4: Verify**

Run: `go test ./cmd/deploy -run AWGMClient -count=1`

Expected: PASS.

## Task 4: Bootstrap Script Renderer

**Files:**
- Add: `cmd/deploy/templates/awgm-bootstrap.sh.tmpl`
- Add: `cmd/deploy/awgm_bootstrap.go`
- Add: `cmd/deploy/awgm_bootstrap_test.go`

- [ ] **Step 1: Write renderer tests**

Create `cmd/deploy/awgm_bootstrap_test.go`:

```go
func TestRenderAWGMBootstrapScriptContainsInstallPaths(t *testing.T) {
	script, err := RenderAWGMBootstrapScript(AWGMBootstrapParams{
		Nickname:     "testkeen",
		BackendURL:   "https://wg.example.test",
		RawToken:     strings.Repeat("a", 64),
		Version:      "v0.13.0-rc10",
		DownloadURL:  "https://example.test/wg-monitor-linux-arm64",
		ChecksumURL:  "https://example.test/checksums.txt",
		ChecksumName: "wg-monitor-linux-arm64",
	})
	if err != nil {
		t.Fatalf("RenderAWGMBootstrapScript: %v", err)
	}
	for _, want := range []string{"/opt/etc/wg-monitor/config.yaml", "/opt/bin/wg-monitor", "/opt/etc/init.d/S99wg-monitor", "agent:", "nickname: testkeen"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "echo "+strings.Repeat("a", 64)) {
		t.Fatal("script prints raw token")
	}
}
```

- [ ] **Step 2: Add bootstrap template**

Create `cmd/deploy/templates/awgm-bootstrap.sh.tmpl` with an idempotent POSIX shell script that:

- exits if `/opt` is absent
- refuses overwrite when existing `/opt/etc/wg-monitor/config.yaml` has a different `agent.nickname`
- downloads to `/opt/tmp/wg-monitor.new`
- verifies checksum from `checksums.txt`
- writes `config.yaml.tmp`, then renames
- writes `S99wg-monitor.tmp`, chmods, then renames
- restarts via `/opt/etc/init.d/S99wg-monitor restart || /opt/etc/init.d/S99wg-monitor start`

- [ ] **Step 3: Add renderer function**

In `cmd/deploy/awgm_bootstrap.go`, add:

```go
type AWGMBootstrapParams struct {
	Nickname     string
	BackendURL   string
	RawToken     string
	Version      string
	DownloadURL  string
	ChecksumURL  string
	ChecksumName string
}

func RenderAWGMBootstrapScript(p AWGMBootstrapParams) (string, error)
```

Render `agent.yaml.tmpl` and `S99wg-monitor`, shell-quote their content into the script params.

- [ ] **Step 4: Verify**

Run: `go test ./cmd/deploy -run AWGMBootstrap -count=1`

Expected: PASS.

## Task 5: Wizard Actions Use AWG Manager by Default

**Files:**
- Modify: `cmd/deploy/actions.go`
- Modify: `cmd/deploy/actions_test.go`
- Modify: `cmd/deploy/main.go`
- Modify: `cmd/deploy/menu.go`

- [ ] **Step 1: Extract AWG Manager deploy helper**

Add to `cmd/deploy/actions.go`:

```go
func actionInstallAgentAWGM(state *State, secrets *SecretStore, dl *Downloader, nickname string) error
```

Behavior:

- resolve `AgentState`
- require `WIZARD_TOKEN`
- call `VPSClient.CreateEnrollment`
- save `WG_AGENT_TOKEN_<NICK>`
- prompt/cache `WG_AWGM_LOGIN_<NICK>` and `WG_AWGM_PASS_<NICK>`
- call AWG Manager login/health/system info
- render bootstrap script for the latest or requested version
- install/start terminal, run script, stop terminal in `defer`
- poll `/v1/wizard/agents` for fresh `last_seen_at`
- set `DeployMode = "awgm"`, `AWGMURL`, `AWGMAuth`, `LastDeployedVersion`

- [ ] **Step 2: Make normal install path AWGM**

Change `actionInstallAgent` to:

```go
if os.Getenv("WG_LEGACY_ROUTER_SSH") != "1" {
	return actionInstallAgentAWGM(state, secrets, dl, nickname)
}
return actionInstallAgentLegacySSH(state, secrets, dl, nickname)
```

Rename the existing local SSH implementation to `actionInstallAgentLegacySSH`.

- [ ] **Step 3: Make add-router collect AWGM URL**

In `actionAddRouter`, after nickname/kind state setup, ask:

```go
ag.AWGMURL = strings.TrimRight(Ask("AWG Manager URL (KeenDNS, https://...)", ag.AWGMURL), "/")
ag.DeployMode = "awgm"
ag.AWGMAuth = "router-admin"
```

Then call `actionInstallAgentAWGM`.

- [ ] **Step 4: Hide old menu text**

Update menu labels:

- `Добавить/установить роутер` -> `Добавить роутер через AWG Manager/KeenDNS`
- `Netfix маршрута` -> move out of normal list or mark as `legacy recovery`
- `Восстановить токен агента` -> `Re-enroll через AWG Manager`

Keep CLI `netfix` and `uninstall-agent --host` available.

- [ ] **Step 5: Verify**

Run: `go test ./cmd/deploy -run 'AgentState|AWGM|AddRouter|UpdateAgentRejects|Menu' -count=1`

Expected: PASS.

## Task 6: Migration and Token Repair Pivot

**Files:**
- Modify: `cmd/deploy/actions.go`
- Modify: `cmd/deploy/actions_test.go`

- [ ] **Step 1: Re-enroll missing-token migrations**

In `migrateBackendAgents`, when `plan.ReEnroll` is true or token is missing, call `actionInstallAgentAWGM` for that agent instead of `migrateAgentBackendConfig`.

- [ ] **Step 2: Keep old config rewrite only behind legacy flag**

Wrap `migrateAgentBackendConfig` call:

```go
if os.Getenv("WG_LEGACY_ROUTER_SSH") != "1" {
	return actionInstallAgentAWGM(state, secrets, nil, ag.Nickname)
}
return migrateAgentBackendConfig(...)
```

- [ ] **Step 3: Repair-agent-token becomes AWGM re-enroll**

Make `actionRepairAgentToken` call AWGM re-enroll by default. Existing SSH config rewrite remains under `WG_LEGACY_ROUTER_SSH=1`.

- [ ] **Step 4: Verify**

Run: `go test ./cmd/deploy -run 'Migrate|Repair|AWGM' -count=1`

Expected: PASS.

## Task 7: Full Verification

**Files:**
- No new files unless previous tasks reveal a compile-only issue.

- [ ] **Step 1: Run targeted deploy/backend tests**

Run: `go test ./cmd/deploy ./internal/backend/... ./internal/agent/... ./pkg/wire/...`

Expected: PASS.

- [ ] **Step 2: Run broad tests**

Run: `go test ./...`

Expected: PASS, or document unrelated existing failures with exact package/test names.

- [ ] **Step 3: Build deploy wizard**

Run: `go build -o deploy.exe ./cmd/deploy`

Expected: `deploy.exe` builds.

- [ ] **Step 4: Run dry CLI help check**

Run: `.\deploy.exe --version` and `.\deploy.exe help`

Expected: version prints and help shows AWG Manager/KeenDNS as the normal deploy path.

- [ ] **Step 5: Live smoke only after credentials are available**

When the operator provides one AWG Manager URL and credentials, run one re-enroll and verify through `/v1/wizard/agents`.
