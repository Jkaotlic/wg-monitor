# Dashboard "Deploy to router" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a dashboard "Deploy to router" action that performs a full first-time agent install on a brand-new router via the existing AWG-Manager relay, prompting for AWG auth + router root password per-click (never stored).

**Architecture:** Reuse the Revive relay path (`internal/backend/agent_revive.go` + embedded `internal/awgmrelay/awgm-relay.py`). A new wizard-free relay mode `bootstrap_install` reuses the relay's existing `build_deferred_bootstrap_script`. The backend mints a fresh enrollment token, resolves the target version (latest stable or override), fetches+parses release `checksums.txt` into an `{asset: sha}` map, and drives the relay with transient credentials in a 0600 temp file deleted after the run.

**Tech Stack:** Go (net/http stdlib, SQLite via `internal/backend/db`), embedded Python 3 relay (stdlib only), vanilla JS dashboard served from Go `embed`.

## Global Constraints

- **No secret persistence.** AWG login/password/api-key and router root password are transient only — passed to the relay in a 0600 temp JSON deleted after the run, never written to the DB. No new secret columns. (Same model as `agent_revive.go`.)
- **Logs never include credentials.** Mirror `d.Logger.Info("dashboard revive ok", "nickname", ...)` — nickname + version + result only.
- **Relay is stdlib-only Python 3.** `internal/awgmrelay/awgm-relay.py` must not import third-party packages.
- **Agent download asset names:** `wg-monitor-agent-linux-arm64`, `wg-monitor-agent-linux-mipsle` (see `isAllowedReleaseAsset`, `internal/backend/release_proxy.go:72`).
- **Router-side release base is the backend mirror:** `release_base = <PublicBaseURL>/v1/releases/download` (matches the wizard, `cmd/deploy/awgm_deferred.go:69`).
- **Init script is a single shared source** — `internal/installtmpl/S99wg-monitor`, embedded once, consumed by both `cmd/deploy` and `internal/backend` (no duplicate copy).
- Run `go build ./...` and `gofmt` clean after every Go task.

---

### Task 1: Shared init-script package (`internal/installtmpl`)

Move the S99 Entware init template into a shared package so both the wizard and the backend embed one canonical copy.

**Files:**
- Create: `internal/installtmpl/initscript.go`
- Move: `cmd/deploy/templates/S99wg-monitor` → `internal/installtmpl/S99wg-monitor`
- Create: `internal/installtmpl/initscript_test.go`
- Modify: `cmd/deploy/awgm_deferred.go:51` and `cmd/deploy/awgm_bootstrap.go:45` (replace `ReadStaticTemplate("S99wg-monitor")`)

**Interfaces:**
- Produces: `installtmpl.InitScript() string` — the S99 init script with the trailing newline trimmed (matches the wizard's existing `strings.TrimRight(string(initScript), "\n")`).

- [ ] **Step 1: Move the template file**

```bash
git mv cmd/deploy/templates/S99wg-monitor internal/installtmpl/S99wg-monitor
```

- [ ] **Step 2: Write the failing test**

Create `internal/installtmpl/initscript_test.go`:

```go
package installtmpl

import "testing"

func TestInitScriptHasShebangAndService(t *testing.T) {
	s := InitScript()
	if len(s) == 0 {
		t.Fatal("InitScript() is empty")
	}
	for _, want := range []string{"#!/bin/sh", "wg-monitor"} {
		if !contains(s, want) {
			t.Fatalf("init script missing %q", want)
		}
	}
	if s[len(s)-1] == '\n' {
		t.Fatal("InitScript() must have trailing newline trimmed")
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

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/installtmpl/`
Expected: FAIL — `undefined: InitScript` (package/file not created yet).

- [ ] **Step 4: Write minimal implementation**

Create `internal/installtmpl/initscript.go`:

```go
// Package installtmpl holds install-time templates shared between the deploy
// wizard (cmd/deploy) and the backend's dashboard "Deploy to router" relay.
package installtmpl

import (
	_ "embed"
	"strings"
)

//go:embed S99wg-monitor
var initScript string

// InitScript returns the S99wg-monitor Entware init script with the trailing
// newline trimmed, ready to splice into a relay bootstrap job.
func InitScript() string {
	return strings.TrimRight(initScript, "\n")
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/installtmpl/`
Expected: PASS

- [ ] **Step 6: Repoint the wizard call sites**

In `cmd/deploy/awgm_deferred.go`, replace lines around 51 + 71:

```go
	// was: initScript, err := ReadStaticTemplate("S99wg-monitor")  (+ err check)
	// and: InitScript: strings.TrimRight(string(initScript), "\n"),
	// becomes:
		InitScript:       installtmpl.InitScript(),
```

Delete the now-unused `initScript, err := ReadStaticTemplate(...)` block and its error check in that function. Add the import `"github.com/anex/wg-monitor/internal/installtmpl"`.

In `cmd/deploy/awgm_bootstrap.go`, replace lines around 45 + 56 the same way:

```go
		InitScript:    installtmpl.InitScript(),
```

Delete the `initScript, err := ReadStaticTemplate("S99wg-monitor")` block there and add the import if not already present.

- [ ] **Step 7: Verify no other reader of the old template remains**

Run: `grep -rn "ReadStaticTemplate(\"S99wg-monitor\")" cmd/ internal/`
Expected: no matches. (If any remain, repoint them to `installtmpl.InitScript()`.)

- [ ] **Step 8: Build + run wizard tests**

Run: `go build ./... && go test ./cmd/deploy/ ./internal/installtmpl/`
Expected: PASS (the wizard's existing `awgm_deferred_test.go` / `awgm_bootstrap_test.go` still pass — the init-script bytes are unchanged).

- [ ] **Step 9: Commit**

```bash
git add internal/installtmpl cmd/deploy/awgm_deferred.go cmd/deploy/awgm_bootstrap.go
git commit -m "refactor: share S99 init template via internal/installtmpl"
```

---

### Task 2: Release checksum fetch + parse helper (backend)

The backend needs the per-asset SHA-256 to hand the relay a checksums map.

**Files:**
- Create: `internal/backend/release_checksums.go`
- Create: `internal/backend/release_checksums_test.go`

**Interfaces:**
- Produces: `parseReleaseChecksums(content string) map[string]string` — maps asset filename → lowercase sha256.
- Produces: `var releaseChecksumsFetcher = fetchReleaseChecksums` with `fetchReleaseChecksums(ctx context.Context, version string) (map[string]string, error)` — a package var so the handler test can stub it.

- [ ] **Step 1: Write the failing test**

Create `internal/backend/release_checksums_test.go`:

```go
package backend

import "testing"

func TestParseReleaseChecksums(t *testing.T) {
	content := "ABCDEF0123  wg-monitor-agent-linux-arm64\n" +
		"99aa  wg-monitor-agent-linux-mipsle\n" +
		"\n" +
		"garbage-line-no-second-field\n"
	got := parseReleaseChecksums(content)
	if got["wg-monitor-agent-linux-arm64"] != "abcdef0123" {
		t.Fatalf("arm64 sha = %q, want lowercased abcdef0123", got["wg-monitor-agent-linux-arm64"])
	}
	if got["wg-monitor-agent-linux-mipsle"] != "99aa" {
		t.Fatalf("mipsle sha = %q", got["wg-monitor-agent-linux-mipsle"])
	}
	if _, ok := got["garbage-line-no-second-field"]; ok {
		t.Fatal("malformed line must be skipped")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/ -run TestParseReleaseChecksums`
Expected: FAIL — `undefined: parseReleaseChecksums`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/backend/release_checksums.go`:

```go
package backend

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/releaseorigin"
)

// releaseChecksumsFetcher is a package var so handler tests can stub the
// network fetch. It returns a map of release asset filename -> sha256.
var releaseChecksumsFetcher = fetchReleaseChecksums

// parseReleaseChecksums parses GitHub's "checksums.txt" ("<sha256>␠␠<file>"
// lines) into asset->lowercase-sha. Malformed lines are skipped.
func parseReleaseChecksums(content string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		out[fields[1]] = strings.ToLower(fields[0])
	}
	return out
}

// fetchReleaseChecksums downloads and parses checksums.txt for a release tag
// from the same upstream the release proxy mirrors.
func fetchReleaseChecksums(ctx context.Context, version string) (map[string]string, error) {
	v, err := releaseorigin.ValidateReleaseTag(strings.TrimSpace(version))
	if err != nil {
		return nil, fmt.Errorf("invalid release tag: %w", err)
	}
	u := strings.TrimRight(releaseDownloadBase, "/") + "/" + url.PathEscape(v) + "/checksums.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch checksums: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	sums := parseReleaseChecksums(string(body))
	if len(sums) == 0 {
		return nil, fmt.Errorf("checksums.txt for %s parsed to zero entries", v)
	}
	return sums, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/backend/ -run TestParseReleaseChecksums`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/backend/release_checksums.go internal/backend/release_checksums_test.go
git commit -m "feat(backend): release checksums fetch+parse helper for router install"
```

---

### Task 3: Relay `bootstrap_install` mode (Python, wizard-free full install)

Add a relay mode that does a first-time install using a backend-supplied token, backend URL, and checksums map — no local-wizard calls, no two-phase commit.

**Files:**
- Modify: `internal/awgmrelay/awgm-relay.py` (add `run_install_bootstrap`, dispatch in `main()`)
- Create: `internal/awgmrelay/test_install_mode.py`

**Interfaces:**
- Produces: relay job key `"mode": "bootstrap_install"` consuming cfg keys: `base_url, api_key|login+password, terminal_user, terminal_password, nickname, target_version, backend_url, raw_token, release_base, init_script, checksums` (asset→sha map). Reuses existing `build_deferred_bootstrap_script(cfg, backend_url, raw_token, arch)` and `build_agent_config`.

- [ ] **Step 1: Write the failing test**

Create `internal/awgmrelay/test_install_mode.py`:

```python
import importlib.util, os, sys, types, unittest

HERE = os.path.dirname(os.path.abspath(__file__))

def load_relay():
    spec = importlib.util.spec_from_file_location("awgm_relay", os.path.join(HERE, "awgm-relay.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

class InstallModeTest(unittest.TestCase):
    def test_run_install_bootstrap_builds_full_install_script(self):
        relay = load_relay()
        captured = {}

        # Stub network/terminal so only orchestration + script-build runs.
        relay.opener = lambda: (object(), object())
        relay.login_if_needed = lambda op, cfg: None

        def fake_request(op, cfg, method, api_path, body=None):
            if api_path == "/api/system/info":
                return {"data": {"goArch": "arm64"}}
            return {"data": {}}
        relay.request = fake_request
        relay.ensure_terminal = lambda op, cfg: None
        relay.ws_connect = lambda cfg, jar: object()
        relay.ws_send = lambda sock, opcode, payload: None
        relay.send_resize = lambda sock, cols=120, rows=40: None
        relay.login_terminal = lambda sock, cfg: None

        def fake_run_bootstrap(sock, cfg):
            captured["script"] = cfg.get("bootstrap_script") or ""
        relay.run_bootstrap = fake_run_bootstrap

        cfg = {
            "mode": "bootstrap_install",
            "base_url": "https://awg.example",
            "nickname": "bronya",
            "target_version": "v0.13.8",
            "backend_url": "https://wgmon.example",
            "raw_token": "tok-deadbeef",
            "release_base": "https://wgmon.example/v1/releases/download",
            "init_script": "#!/bin/sh\necho init",
            "terminal_user": "root",
            "terminal_password": "rootpw",
            "checksums": {
                "wg-monitor-agent-linux-arm64": "aa11bb22",
                "wg-monitor-agent-linux-mipsle": "cc33dd44",
            },
        }
        relay.run_install_bootstrap(cfg)

        s = captured["script"]
        self.assertIn("NICKNAME='bronya'", s)
        self.assertIn("v0.13.8/wg-monitor-agent-linux-arm64", s)
        self.assertIn("EXPECTED_SHA='aa11bb22'", s)
        self.assertIn("token: 'tok-deadbeef'", s)
        self.assertIn("url: 'https://wgmon.example'", s)

if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python3 -m unittest internal.awgmrelay.test_install_mode` (or `cd internal/awgmrelay && python3 -m unittest test_install_mode`)
Expected: FAIL — `AttributeError: module ... has no attribute 'run_install_bootstrap'`.

- [ ] **Step 3: Add `run_install_bootstrap` to the relay**

In `internal/awgmrelay/awgm-relay.py`, add this function next to `run_deferred_bootstrap` (it reuses `build_deferred_bootstrap_script`, `build_agent_config`, `normalize_arch`, and the existing terminal helpers):

```python
def run_install_bootstrap(cfg):
    global TLS_CONFIG
    TLS_CONFIG = cfg
    nick = cfg.get("nickname") or ""
    backend_url = cfg.get("backend_url") or ""
    raw_token = cfg.get("raw_token") or ""
    if not nick or not backend_url or not raw_token:
        raise RelayError("bootstrap_install requires nickname, backend_url, raw_token")
    checksums = cfg.get("checksums") or {}
    if not isinstance(checksums, dict) or not checksums:
        raise RelayError("bootstrap_install requires a non-empty checksums map")
    op, jar = opener()
    login_if_needed(op, cfg)
    env = request(op, cfg, "GET", "/api/system/info")
    data = env.get("data") or {}
    raw_arch = (data.get("goArch") or "").strip()
    if not raw_arch:
        raise RelayError("AWG Manager system_info did not report goArch")
    arch = normalize_arch(raw_arch)
    asset = "wg-monitor-agent-linux-" + arch
    expected_sha = (checksums.get(asset) or "").strip()
    if not expected_sha:
        raise RelayError("no checksum for asset %s in provided checksums map" % asset)
    cfg["expected_sha"] = expected_sha
    script = build_deferred_bootstrap_script(cfg, backend_url, raw_token, arch)
    ensure_terminal(op, cfg)
    sock = None
    try:
        sock = ws_connect(cfg, jar)
        ws_send(sock, 0x1, '{"AuthToken":""}')
        send_resize(sock)
        login_terminal(sock, cfg)
        run_bootstrap(sock, {**cfg, "bootstrap_script": script})
    finally:
        if sock is not None:
            try:
                sock.close()
            except Exception:
                pass
        try:
            request(op, cfg, "POST", "/api/terminal/stop")
        except Exception as e:
            print("WARN terminal stop failed: %s" % e, file=sys.stderr)
    print("install bootstrap complete for %s at %s (%s)" % (nick, cfg.get("target_version") or "", arch))
```

- [ ] **Step 4: Dispatch the new mode in `main()`**

In `internal/awgmrelay/awgm-relay.py`, in `main()` right after the existing `if cfg.get("mode") == "deferred_bootstrap":` block (around line 688), add:

```python
    if cfg.get("mode") == "bootstrap_install":
        run_install_bootstrap(cfg)
        return
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd internal/awgmrelay && python3 -m unittest test_install_mode`
Expected: PASS

- [ ] **Step 6: Sanity-check the relay still parses**

Run: `python3 -c "import ast; ast.parse(open('internal/awgmrelay/awgm-relay.py').read()); print('ok')"`
Expected: `ok`

- [ ] **Step 7: Commit**

```bash
git add internal/awgmrelay/awgm-relay.py internal/awgmrelay/test_install_mode.py
git commit -m "feat(relay): bootstrap_install mode — wizard-free full install via terminal"
```

---

### Task 4: Backend install-job runner + handler (`agent_deploy_router.go`)

Mirror `agent_revive.go`: a dedicated install job, a stubbable runner, and the `POST /v1/dashboard/agents/{nickname}/deploy-router` handler.

**Files:**
- Create: `internal/backend/agent_deploy_router.go`
- Create: `internal/backend/agent_deploy_router_test.go`
- Modify: `internal/backend/dashboard_handler.go` (register the route in the same `mux.Handle(...)` block as `/revive`, around line 88)
- Modify: `internal/backend/agent_revive.go` (extract the shared temp-file relay runner — see Step 3)

**Interfaces:**
- Consumes: `installtmpl.InitScript()` (Task 1); `releaseChecksumsFetcher` (Task 2); relay `mode: "bootstrap_install"` (Task 3); existing `createAgentEnrollment`, `lookupDashboardLatestVersion`, `validateDashboardAWGMURL`, `runRelayProcess` (new shared helper).
- Produces: `var runAWGMInstallJob = defaultRunAWGMInstallJob` with `func(ctx context.Context, relayPath string, job awgmInstallJob) (string, error)`; type `awgmInstallJob`; `dashboardDeployRouterHandler(d Deps) http.HandlerFunc`.

- [ ] **Step 1: Write the failing test**

Create `internal/backend/agent_deploy_router_test.go`:

```go
package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func newDeployRouterMux(t *testing.T, awgmURL string) (*db.DB, http.Handler) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Users().UpsertEnrollment("bronya", "tok-bronya-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if awgmURL != "" {
		if err := d.Users().UpdateDeployInfo("bronya", db.DeployInfo{AWGMURL: awgmURL}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewMux(Deps{DB: d, DashboardToken: "secret", PublicBaseURL: "https://wgmon.example"})
	return d, h
}

func postDeployRouter(t *testing.T, h http.Handler, nick, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/"+nick+"/deploy-router", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDeployRouterBuildsInstallJobWithTransientCreds(t *testing.T) {
	d, h := newDeployRouterMux(t, "https://awg.example")

	origVer := lookupDashboardLatestVersion
	lookupDashboardLatestVersion = func(_ context.Context) (string, error) { return "v0.13.8", nil }
	t.Cleanup(func() { lookupDashboardLatestVersion = origVer })

	origSums := releaseChecksumsFetcher
	releaseChecksumsFetcher = func(_ context.Context, version string) (map[string]string, error) {
		return map[string]string{"wg-monitor-agent-linux-arm64": "aa11"}, nil
	}
	t.Cleanup(func() { releaseChecksumsFetcher = origSums })

	beforeHash := tokenHashOf(t, d, "bronya")

	var captured awgmInstallJob
	origRun := runAWGMInstallJob
	runAWGMInstallJob = func(_ context.Context, _ string, job awgmInstallJob) (string, error) {
		captured = job
		return "install bootstrap complete for bronya at v0.13.8 (arm64)\n", nil
	}
	t.Cleanup(func() { runAWGMInstallJob = origRun })

	rec := postDeployRouter(t, h, "bronya",
		`{"root_password":"rootpw","awgm_api_key":"key-123"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if captured.Mode != "bootstrap_install" {
		t.Errorf("Mode = %q", captured.Mode)
	}
	if captured.TerminalPassword != "rootpw" || captured.TerminalUser != "root" {
		t.Errorf("terminal creds = %q/%q", captured.TerminalUser, captured.TerminalPassword)
	}
	if captured.APIKey != "key-123" {
		t.Errorf("api key = %q", captured.APIKey)
	}
	if captured.TargetVersion != "v0.13.8" {
		t.Errorf("version = %q", captured.TargetVersion)
	}
	if captured.BackendURL != "https://wgmon.example" {
		t.Errorf("backend url = %q", captured.BackendURL)
	}
	if captured.ReleaseBase != "https://wgmon.example/v1/releases/download" {
		t.Errorf("release base = %q", captured.ReleaseBase)
	}
	if captured.RawToken == "" || captured.RawToken == "tok-bronya-000000000000000000" {
		t.Errorf("expected a freshly re-minted raw token, got %q", captured.RawToken)
	}
	if captured.Checksums["wg-monitor-agent-linux-arm64"] != "aa11" {
		t.Errorf("checksums not passed: %v", captured.Checksums)
	}
	if captured.InitScript == "" {
		t.Error("init script missing")
	}
	if h := tokenHashOf(t, d, "bronya"); h == beforeHash {
		t.Error("expected token to be re-minted (hash should change)")
	}
}

func TestDeployRouterRequiresRootPassword(t *testing.T) {
	_, h := newDeployRouterMux(t, "https://awg.example")
	rec := postDeployRouter(t, h, "bronya", `{"awgm_api_key":"k"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDeployRouterRequiresStoredAWGMURL(t *testing.T) {
	_, h := newDeployRouterMux(t, "") // no awgm_url
	rec := postDeployRouter(t, h, "bronya", `{"root_password":"rootpw"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDeployRouterRelayFailureReturns502(t *testing.T) {
	_, h := newDeployRouterMux(t, "https://awg.example")
	lookupDashboardLatestVersion = func(_ context.Context) (string, error) { return "v0.13.8", nil }
	releaseChecksumsFetcher = func(_ context.Context, _ string) (map[string]string, error) {
		return map[string]string{"wg-monitor-agent-linux-arm64": "aa11"}, nil
	}
	origRun := runAWGMInstallJob
	runAWGMInstallJob = func(_ context.Context, _ string, _ awgmInstallJob) (string, error) {
		return "checksum mismatch\n", context.DeadlineExceeded
	}
	t.Cleanup(func() { runAWGMInstallJob = origRun })

	rec := postDeployRouter(t, h, "bronya", `{"root_password":"rootpw"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.OK || !strings.Contains(resp.Output, "checksum mismatch") {
		t.Fatalf("bad 502 body: %s", rec.Body.String())
	}
}

// tokenHashOf reads the stored token_hash for a nickname.
func tokenHashOf(t *testing.T, d *db.DB, nick string) string {
	t.Helper()
	u, err := d.Users().GetByNickname(nick)
	if err != nil {
		t.Fatal(err)
	}
	return u.TokenHash
}
```

> Note: if `GetByNickname`'s returned struct field is not `TokenHash`, adjust `tokenHashOf` to the actual field name (check `internal/backend/db/users.go` around lines 25-57). If no token-hash field is exported on the row, replace the re-mint assertion with: call the deploy twice and assert `captured.RawToken` differs between calls.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/ -run TestDeployRouter`
Expected: FAIL — `undefined: awgmInstallJob`, `undefined: runAWGMInstallJob`.

- [ ] **Step 3: Extract the shared relay runner in `agent_revive.go`**

In `internal/backend/agent_revive.go`, refactor `defaultRunAWGMRelayJob` to delegate the temp-file + exec plumbing to a shared helper so the install runner can reuse it. Replace the body of `defaultRunAWGMRelayJob` (lines ~97-131) with:

```go
func defaultRunAWGMRelayJob(ctx context.Context, relayPath string, job awgmReviveJob) (string, error) {
	body, err := json.Marshal(job)
	if err != nil {
		return "", err
	}
	return runRelayProcess(ctx, relayPath, body)
}

// runRelayProcess provisions the embedded relay (or a wizard override), writes
// the marshalled job to a 0600 temp file, runs python3 against it, and removes
// the file so transient credentials never persist. Shared by revive + install.
func runRelayProcess(ctx context.Context, relayPath string, jobJSON []byte) (string, error) {
	if _, err := exec.LookPath("python3"); err != nil {
		return "", fmt.Errorf("python3 not found on the backend host — the relay needs it (install python3)")
	}
	scriptPath, cleanupScript, err := resolveRelayScript(relayPath)
	if err != nil {
		return "", fmt.Errorf("provision awgm relay: %w", err)
	}
	defer cleanupScript()
	f, err := os.CreateTemp("", "wg-monitor-relayjob-*.json")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", err
	}
	if _, err := f.Write(jobJSON); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "python3", scriptPath, tmp).CombinedOutput()
	return string(out), err
}
```

Run: `go test ./internal/backend/ -run TestDashboardRevive` — Expected: PASS (revive behavior unchanged).

- [ ] **Step 4: Write the install job + runner + handler**

Create `internal/backend/agent_deploy_router.go`:

```go
package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/installtmpl"
)

// awgmInstallJob drives the relay's bootstrap_install mode: a first-time agent
// install over the AWG Manager terminal. Credentials are transient (0600 temp
// file, deleted after the run) — nothing here is persisted.
type awgmInstallJob struct {
	BaseURL          string            `json:"base_url"`
	APIKey           string            `json:"api_key,omitempty"`
	Login            string            `json:"login,omitempty"`
	Password         string            `json:"password,omitempty"`
	TerminalUser     string            `json:"terminal_user"`
	TerminalPassword string            `json:"terminal_password"`
	Mode             string            `json:"mode"`
	Nickname         string            `json:"nickname"`
	TargetVersion    string            `json:"target_version"`
	BackendURL       string            `json:"backend_url"`
	RawToken         string            `json:"raw_token"`
	ReleaseBase      string            `json:"release_base"`
	InitScript       string            `json:"init_script"`
	Checksums        map[string]string `json:"checksums"`
}

// runAWGMInstallJob runs the install relay. Package var so tests can stub it.
var runAWGMInstallJob = defaultRunAWGMInstallJob

func defaultRunAWGMInstallJob(ctx context.Context, relayPath string, job awgmInstallJob) (string, error) {
	body, err := json.Marshal(job)
	if err != nil {
		return "", err
	}
	return runRelayProcess(ctx, relayPath, body)
}

type dashboardDeployRouterReq struct {
	RootPassword string `json:"root_password"`
	AWGMLogin    string `json:"awgm_login"`
	AWGMPassword string `json:"awgm_password"`
	AWGMAPIKey   string `json:"awgm_api_key"`
	Version      string `json:"version"`
}

// dashboardDeployRouterHandler performs a first-time agent install on a router
// from the dashboard: it re-mints the enrollment token, resolves the target
// version + release checksums, and drives the relay's bootstrap_install mode
// with operator-supplied, never-stored credentials.
func dashboardDeployRouterHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if d.DB == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "db_not_configured", "db not configured")
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
		var req dashboardDeployRouterReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.RootPassword = strings.TrimSpace(req.RootPassword)
		if req.RootPassword == "" {
			writeJSONError(w, http.StatusBadRequest, "root_password_required",
				"router root password is required (used once for the terminal login, never stored)")
			return
		}

		user, err := d.DB.Users().GetByNickname(nickname)
		if err != nil {
			if errors.Is(err, db.ErrUserNotFound) {
				writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		awgmURL := strings.TrimSpace(stringValue(user.AWGMURL))
		if err := validateDashboardAWGMURL(awgmURL); err != nil || awgmURL == "" {
			writeJSONError(w, http.StatusBadRequest, "no_awgm_url",
				"agent has no AWG Manager URL — set awgm_url first (Edit settings)")
			return
		}

		version := strings.TrimSpace(req.Version)
		if version == "" {
			version, err = lookupDashboardLatestVersion(r.Context())
			if err != nil {
				writeJSONError(w, http.StatusBadGateway, "latest_version_failed", err.Error())
				return
			}
		}
		sums, err := releaseChecksumsFetcher(r.Context(), version)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "checksums_failed", err.Error())
			return
		}

		backendURL := strings.TrimRight(strings.TrimSpace(d.PublicBaseURL), "/")
		if backendURL == "" {
			writeJSONError(w, http.StatusInternalServerError, "no_public_base_url",
				"backend PublicBaseURL is not configured")
			return
		}

		// Re-mint the enrollment token: the DB only keeps the hash, and a fresh
		// config.yaml needs the raw token. Idempotent (UpsertEnrollment upserts).
		enrollment, _, err := createAgentEnrollment(d.DB, nickname, stringValue(user.Kind), int64Value(user.ThreadID))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}

		relayPath := d.AWGMRelayPath
		if relayPath == "" {
			relayPath = defaultAWGMRelayPath
		}
		job := awgmInstallJob{
			BaseURL:          awgmURL,
			APIKey:           strings.TrimSpace(req.AWGMAPIKey),
			Login:            strings.TrimSpace(req.AWGMLogin),
			Password:         req.AWGMPassword,
			TerminalUser:     "root",
			TerminalPassword: req.RootPassword,
			Mode:             "bootstrap_install",
			Nickname:         nickname,
			TargetVersion:    version,
			BackendURL:       backendURL,
			RawToken:         enrollment.RawToken,
			ReleaseBase:      backendURL + "/v1/releases/download",
			InitScript:       installtmpl.InitScript(),
			Checksums:        sums,
		}
		output, runErr := runAWGMInstallJob(r.Context(), relayPath, job)
		if runErr != nil {
			if d.Logger != nil {
				d.Logger.Warn("dashboard deploy-router failed", "nickname", nickname, "version", version, "err", runErr)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(struct {
				OK      bool   `json:"ok"`
				Error   string `json:"error"`
				Output  string `json:"output"`
				Version string `json:"version"`
			}{OK: false, Error: runErr.Error(), Output: output, Version: version})
			return
		}

		// Record the successful install (merge-safe: preserve existing fields).
		info := db.DeployInfo{
			Kind:                stringValue(user.Kind),
			ThreadID:            int64Value(user.ThreadID),
			SSHHost:             stringValue(user.SSHHost),
			SSHPort:             int64Value(user.SSHPort),
			SSHUser:             stringValue(user.SSHUser),
			Arch:                stringValue(user.Arch),
			Ring:                stringValue(user.Ring),
			DeployMode:          "awgm",
			AWGMURL:             awgmURL,
			AWGMAuth:            stringValue(user.AWGMAuth),
			ExpectedMAC:         stringValue(user.ExpectedMAC),
			LastDeployedVersion: version,
			LastDeploy:          time.Now().UTC().Format(time.RFC3339),
		}
		if err := d.DB.Users().UpdateDeployInfo(nickname, info); err != nil && d.Logger != nil {
			d.Logger.Warn("deploy-router: UpdateDeployInfo failed", "nickname", nickname, "err", err)
		}
		if d.Logger != nil {
			d.Logger.Info("dashboard deploy-router ok", "nickname", nickname, "version", version)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			OK      bool   `json:"ok"`
			Output  string `json:"output"`
			Version string `json:"version"`
		}{OK: true, Output: output, Version: version})
	}
}
```

> Note: verify the `user.*` field names + types against `internal/backend/db/users.go` (the row struct around lines 25-57). `stringValue`/`int64Value` already dereference `*string`/`*int64` (used in `dashboardDeployInfoFromEnrollmentReq`). If `Kind`/`ThreadID` are plain (non-pointer) on the row, drop the `stringValue`/`int64Value` wrapper for those two. Confirm `db.DeployInfo` has exactly these fields (it is built in `dashboardDeployInfoFromEnrollmentReq`, `dashboard_handler.go:1077`).

- [ ] **Step 5: Register the route**

In `internal/backend/dashboard_handler.go`, in the same block as the `/revive` route (around line 88), add:

```go
	mux.Handle("POST /v1/dashboard/agents/{nickname}/deploy-router", requestIDMiddleware()(dashAuth(dashboardDeployRouterHandler(d))))
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/backend/ -run 'TestDeployRouter|TestDashboardRevive'`
Expected: PASS (all install-handler cases + revive regression).

- [ ] **Step 7: Build + vet + full backend test**

Run: `go build ./... && go vet ./internal/backend/ && go test ./internal/backend/`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/backend/agent_deploy_router.go internal/backend/agent_deploy_router_test.go internal/backend/agent_revive.go internal/backend/dashboard_handler.go
git commit -m "feat(backend): dashboard deploy-router handler — remote first-time install via relay"
```

---

### Task 5: Frontend "Deploy to router" modal + action

Add the UI: a new modal (clone of Revive), a recovery-section button, element refs, and wiring. No JS test harness exists for `dashboard_static/` — verify via `go build` (embed compiles) + manual UI check.

**Files:**
- Modify: `internal/backend/dashboard_static/index.html` (new `deployRouterModal`; new button in the recovery section)
- Modify: `internal/backend/dashboard_static/app.js` (els refs, open/close/submit, result render, event wiring)

**Interfaces:**
- Consumes: `POST /v1/dashboard/agents/{nickname}/deploy-router` (Task 4); `state.summary.latest_version` (already on the summary, `dashboard_handler.go:431`); the selected agent's `awgm_url` / `awgm_auth`.

- [ ] **Step 1: Add the modal markup**

In `internal/backend/dashboard_static/index.html`, after the `reviveModal` block (ends ~line 424), add:

```html
  <div id="deployRouterModal" class="modal hidden" role="dialog" aria-modal="true" aria-labelledby="deployRouterTitle">
    <div class="modal-panel">
      <div class="modal-head">
        <div>
          <div class="eyebrow">deploy agent to a new router</div>
          <h2 id="deployRouterTitle">Deploy to router</h2>
        </div>
        <button class="icon-btn" type="button" data-close-deploy-router><span class="ti ti-x"></span></button>
      </div>
      <p class="modal-note">Ставит агента на роутер через <strong>awg-manager terminal</strong> (публичный DDNS): качает бинарь, пишет <code>config.yaml</code> с новым токеном, ставит init-сервис и запускает. Креды используются один раз и <strong>не сохраняются</strong>. AWG Manager URL берётся из настроек агента (меняется в Edit).</p>
      <label class="form-field">
        <span>Router root password</span>
        <input id="deployRouterRootPass" type="password" autocomplete="off" placeholder="root пароль роутера">
      </label>
      <label class="form-field">
        <span>Target version (пусто = latest stable)</span>
        <input id="deployRouterVersion" type="text" autocomplete="off" placeholder="v0.13.8">
      </label>
      <details class="revive-advanced">
        <summary>AWG Manager auth (если awg-manager под логином/api-key)</summary>
        <div class="form-grid form-grid-three">
          <label class="form-field"><span>awgm login</span><input id="deployRouterAwgmLogin" type="text" autocomplete="off"></label>
          <label class="form-field"><span>awgm password</span><input id="deployRouterAwgmPass" type="password" autocomplete="off"></label>
          <label class="form-field"><span>awgm api-key</span><input id="deployRouterAwgmKey" type="text" autocomplete="off"></label>
        </div>
      </details>
      <div id="deployRouterError" class="form-error"></div>
      <div class="modal-actions">
        <button class="btn btn-ghost" type="button" data-close-deploy-router>Cancel</button>
        <button id="deployRouterConfirmBtn" class="btn btn-warn" type="button" data-state="idle"><span class="ti ti-rocket"></span>Deploy</button>
      </div>
    </div>
  </div>
```

- [ ] **Step 2: Add the recovery-section button**

In `internal/backend/dashboard_static/app.js`, in `drawerTabRecovery` next to the Revive button (the `data-revive` button, ~line 671), add right after it:

```js
            <button class="action-btn warn" type="button" title="Поставить агента на новый роутер через awg-manager terminal (качает бинарь, пишет config, запускает)" data-deploy-router="${escapeAttr(selected.nickname)}"><span class="ti ti-rocket"></span>Deploy to router</button>
```

- [ ] **Step 3: Add element refs**

In `internal/backend/dashboard_static/app.js`, in the `els` object next to the revive refs (~line 99-106), add:

```js
    deployRouterModal: document.getElementById("deployRouterModal"),
    deployRouterTitle: document.getElementById("deployRouterTitle"),
    deployRouterRootPass: document.getElementById("deployRouterRootPass"),
    deployRouterVersion: document.getElementById("deployRouterVersion"),
    deployRouterAwgmLogin: document.getElementById("deployRouterAwgmLogin"),
    deployRouterAwgmPass: document.getElementById("deployRouterAwgmPass"),
    deployRouterAwgmKey: document.getElementById("deployRouterAwgmKey"),
    deployRouterError: document.getElementById("deployRouterError"),
    deployRouterConfirmBtn: document.getElementById("deployRouterConfirmBtn"),
```

Also add `deployRouterNick: null` to the `state` object (~line 2-15).

- [ ] **Step 4: Add open/close/submit/result functions**

In `internal/backend/dashboard_static/app.js`, next to `submitRevive`/`showReviveResult` (~line 1103-1173), add:

```js
  function openDeployRouter(nickname) {
    state.deployRouterNick = nickname;
    els.deployRouterTitle.textContent = "Deploy to router / " + nickname;
    els.deployRouterRootPass.value = "";
    els.deployRouterVersion.value = latestVersion();
    els.deployRouterAwgmLogin.value = "";
    els.deployRouterAwgmPass.value = "";
    els.deployRouterAwgmKey.value = "";
    els.deployRouterError.textContent = "";
    setButtonState(els.deployRouterConfirmBtn, "idle");
    els.deployRouterModal.classList.remove("hidden");
    els.deployRouterRootPass.focus();
  }

  function closeDeployRouter() {
    els.deployRouterModal.classList.add("hidden");
    state.deployRouterNick = null;
    setButtonState(els.deployRouterConfirmBtn, "idle");
  }

  async function submitDeployRouter() {
    const nickname = state.deployRouterNick;
    if (!nickname) return;
    if (!els.deployRouterRootPass.value) {
      els.deployRouterError.textContent = "Нужен root-пароль роутера";
      return;
    }
    els.deployRouterError.textContent = "";
    setButtonState(els.deployRouterConfirmBtn, "waiting");
    const payload = {
      root_password: els.deployRouterRootPass.value,
      version: els.deployRouterVersion.value.trim(),
      awgm_login: els.deployRouterAwgmLogin.value.trim(),
      awgm_password: els.deployRouterAwgmPass.value,
      awgm_api_key: els.deployRouterAwgmKey.value.trim()
    };
    try {
      const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/deploy-router`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
      closeDeployRouter();
      toast("deploy отправлен — агент ставится на роутер");
      showDeployRouterResult(nickname, res, null);
      refresh();
    } catch (err) {
      if (err.status === 502) {
        closeDeployRouter();
        showDeployRouterResult(nickname, null, err);
      } else {
        els.deployRouterError.textContent = err.message;
        setButtonState(els.deployRouterConfirmBtn, "error");
      }
    }
  }

  function showDeployRouterResult(nickname, res, err) {
    els.drawerTitle.textContent = "Deploy to router / " + nickname;
    const ok = res && res.ok;
    const output = (res && res.output) || (err && err.body && err.body.output) || "";
    const version = (res && res.version) || (err && err.body && err.body.version) || "";
    const sections = [
      `<div class="result-section${ok ? "" : " result-error"}"><strong>${ok ? "Deploy отправлен" : "Deploy не удался"}</strong><p>${escapeHTML(version)}</p>${err && err.message ? `<p>${escapeHTML(err.message)}</p>` : ""}</div>`
    ];
    if (output) {
      sections.push(`<div class="result-section"><strong>AWG Manager terminal</strong><pre class="raw-output">${escapeHTML(output)}</pre></div>`);
    }
    setResultHTML(sections.join(""));
    els.resultDrawer.classList.remove("hidden");
  }
```

> `latestVersion()` already exists (used by `openDeploy`, app.js:940). `api`, `toast`, `setButtonState`, `setResultHTML`, `refresh`, `escapeHTML` are existing helpers.

- [ ] **Step 5: Wire events**

In `internal/backend/dashboard_static/app.js`:

(a) In the delegated click handler next to `if (button.dataset.revive) openRevive(...)` (~line 1529), add:

```js
      if (button.dataset.deployRouter) openDeployRouter(button.dataset.deployRouter);
```

(b) Next to the `data-close-revive` wiring (~line 1561), add:

```js
  document.querySelectorAll("[data-close-deploy-router]").forEach((button) => button.addEventListener("click", closeDeployRouter));
```

(c) Next to the `reviveConfirmBtn` listener (~line 1575), add:

```js
  els.deployRouterConfirmBtn.addEventListener("click", () => submitDeployRouter().catch((err) => {
    setButtonState(els.deployRouterConfirmBtn, "error");
    els.deployRouterError.textContent = err.message;
  }));
```

(d) In the Esc-key handler (~line 1591, the chain of `if (!els.reviveModal.classList.contains("hidden")) ...`), add a sibling branch:

```js
      if (!els.deployRouterModal.classList.contains("hidden")) { closeDeployRouter(); return; }
```

- [ ] **Step 6: Build (embed compiles the static) + manual check**

Run: `go build ./...`
Expected: PASS.

Manual: run the backend locally with the dashboard enabled, open `/dashboard/`, select an agent that has an `awgm_url`, open the drawer's **Recovery** tab → click **Deploy to router** → confirm the modal opens, the version prefills with latest, and (with a stubbed/non-reachable router) submitting surfaces the relay output/error in the result drawer. Confirm Esc closes the modal.

- [ ] **Step 7: Commit**

```bash
git add internal/backend/dashboard_static/index.html internal/backend/dashboard_static/app.js
git commit -m "feat(dashboard): Deploy to router modal + recovery action"
```

---

### Task 6: Docs + full verification

**Files:**
- Modify: `DEPLOY.md` (note the dashboard path under "Add A Router")

- [ ] **Step 1: Document the dashboard path**

In `DEPLOY.md`, under "## Add A Router" (after line ~91), append:

```markdown
### From the dashboard (no wizard machine needed)

For a router that already has AWG Manager reachable on its public domain, you can
install the agent straight from the dashboard: open the agent's drawer →
**Recovery** → **Deploy to router**. Enter the AWG Manager auth (api-key or
login/password) and the router root password. The backend re-mints the enrollment
token, resolves the latest stable version (or a version you type), and drives the
AWG Manager terminal to download the agent, write `config.yaml`, install the init
service, and start it. Credentials are used once and never stored. The router must
already be enrolled (Add agent) with its `awgm_url` set.
```

- [ ] **Step 2: Full build + test sweep**

Run: `go build ./... && go test ./internal/... ./cmd/deploy/`
Expected: PASS.

Run: `cd internal/awgmrelay && python3 -m unittest test_install_mode`
Expected: PASS.

- [ ] **Step 3: gofmt check**

Run: `gofmt -l internal/backend internal/installtmpl`
Expected: no files listed.

- [ ] **Step 4: Commit**

```bash
git add DEPLOY.md
git commit -m "docs: dashboard Deploy-to-router path under Add A Router"
```

---

## Self-Review

**Spec coverage:**
- Frontend "Deploy to router" modal + creds + version override → Task 5. ✓
- Backend handler: re-mint token, resolve version, fetch checksums, drive relay → Task 4. ✓
- Relay `bootstrap_install` mode reusing `build_deferred_bootstrap_script` → Task 3. ✓
- Checksum fetch/parse helper → Task 2. ✓
- Shared S99 init template (no duplicate) → Task 1. ✓
- Transient creds / 0600 temp file / never stored → Task 4 (shared `runRelayProcess`) + Task 3 (relay). ✓
- Two-step flow (enrollment untouched; separate action) → Task 5 (new button/modal, enrollment modal unchanged). ✓
- Arch auto-detect via system_info → Task 3 (`run_install_bootstrap`). ✓
- Latest-stable + override → Task 4 (`req.Version` else `lookupDashboardLatestVersion`). ✓
- Security boundary preserved (no secret columns) → no DB migration in any task. ✓
- Edge cases: missing awgm_url (400), missing root (400), relay failure (502), checksum/arch failures (relay errors surfaced) → Task 4 tests + Task 3 raises. ✓

**Placeholder scan:** No TBD/TODO; all steps carry real code/commands. Two `> Note:` callouts flag field-name verification against `db/users.go` — these are explicit verification instructions with a concrete fallback, not placeholders.

**Type consistency:** `awgmInstallJob` JSON keys (`base_url, api_key, login, password, terminal_user, terminal_password, mode, nickname, target_version, backend_url, raw_token, release_base, init_script, checksums`) match the cfg keys read by `run_install_bootstrap` and by the existing `build_deferred_bootstrap_script`/`build_agent_config`. `runAWGMInstallJob` / `releaseChecksumsFetcher` / `lookupDashboardLatestVersion` are the exact symbols stubbed in the Task 4 tests. `installtmpl.InitScript()` is produced in Task 1 and consumed in Task 4. `runRelayProcess` is produced in Task 4 Step 3 and reused by `defaultRunAWGMInstallJob`.

## Open verification points for the implementer

1. `internal/backend/db/users.go` row struct (~lines 25-57): confirm the exact field names/types used in Task 4 Step 4 (`user.AWGMURL`, `user.Kind`, `user.ThreadID`, `user.SSHHost/SSHPort/SSHUser`, `user.Arch`, `user.Ring`, `user.AWGMAuth`, `user.ExpectedMAC`, `user.TokenHash`) and whether each is a pointer (wrap with `stringValue`/`int64Value`) or a plain value (use directly).
2. `db.DeployInfo` fields (built in `dashboard_handler.go:1077`): confirm the field set used in the success-path `UpdateDeployInfo`.
3. Confirm `python3` availability assumption in CI for the Task 3 unittest; if CI lacks python3, mark that test as locally-run and rely on `ast.parse` (Task 3 Step 6) in CI.
