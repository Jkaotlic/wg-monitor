# wg-monitor Stage 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Stage-1 MVP described in `docs/superpowers/specs/2026-04-25-wg-monitor-design.md` §14.2: 4 real checks in agent, SQLite + 3-fail HARD / 2-OK RECOVERY state machine + heartbeat watcher in backend, Telegram bot delivering HARD/RECOVERY alerts to per-user topics, and a minimal `wg-monitor-cli add-user` command — until the operator can run `wg-quick down awg0` on MyRouter and see a HARD alert in Telegram followed by a RECOVERY when the interface comes back.

**Architecture:** Extend the Stage 0 skeleton, never rewrite it. Agent grows a `internal/agent/checks` package with one `Check` interface and four concrete checks; the existing `Reporter` runs them in parallel each tick. Backend gains a SQLite store (`internal/backend/db`, pure-Go driver `modernc.org/sqlite`), a state machine (`internal/backend/state`), a Telegram client (`internal/backend/tg`, pure `net/http`), an alert dispatcher (`internal/backend/alerts`), and a heartbeat watcher goroutine. The existing `agents:` config block in `backend.yaml` is replaced by SQLite — agents are migrated by a one-shot CLI command (`wg-monitor-cli add-user`).

**Tech Stack:** Go 1.26.2, `gopkg.in/yaml.v3` (already in go.mod), `modernc.org/sqlite` (NEW — pure-Go, no cgo), `golang.org/x/crypto/bcrypt` (NEW — token hashing), `log/slog`, `net/http`. No `mattn/go-sqlite3`, no `go-telegram-bot-api/v5`, no `tgbotapi/v5` — both rejected as overkill for the Stage 1 surface area (decided by the planner; revisit at Stage 2 when callbacks arrive). Linux-only syscall (`SO_BINDTODEVICE`) gated by `//go:build linux` build tags so `go test ./...` keeps passing on the Windows dev box.

**Out of scope for Stage 1 (deferred to later stages):**
- Inline buttons (Stage 2)
- `/v1/cmd` long-poll & command channel, `ack_token`, lock files (Stage 3)
- Upgrade pipeline & weekly summary (Stage 4)
- `install.sh` & `/agent/<arch>/latest` self-update endpoints (Stage 5) — agent is deployed via `deploy/agent/deploy_keenetic.py` (Paramiko) for now
- `list-users` / `rotate-token` / `remove-user` CLI commands (deferred until Stage 2 polish)
- `pending_commands` and `upgrade_runs` tables (created later when needed; Stage 1 ships with users / events / incident_state / daily_soft_flaps only)

---

## File Structure

**New packages:**
- `internal/agent/checks/` — Check interface + 4 implementations + injectable Deps
  - `checks.go` — `Check` interface, `Deps` struct, `Result` helper
  - `awg_handshake.go` (+ `_test.go`) — parses `wg show <iface> latest-handshakes`
  - `awg_routing.go` (+ `_test.go`) — HTTP GET via iface-bound dialer to `https://api.ipify.org`
  - `awg_marker.go` (+ `_test.go`) — HTTP GET via iface-bound dialer to marker URL with 3 retries
  - `dns_doh.go` (+ `_test.go`) — DoT lookup against 3 providers via subprocess `dig +tls`
  - `dialer_linux.go` — `bindToDevice` using `SO_BINDTODEVICE`
  - `dialer_other.go` — stub returning error on non-Linux (keeps `go test` green on Windows dev)
  - `runner.go` — `Runner` interface + `OSExec` impl (mockable subprocess)
- `internal/backend/db/` — SQLite store
  - `db.go` (+ `_test.go`) — `Open(path)`, `Close()`, embedded migrations
  - `users.go` (+ `_test.go`) — `Insert`, `GetByTokenHash`, `GetAll`, `UpdateLastSeen`, `UpdateThreadID`, `GetByNickname`
  - `events.go` (+ `_test.go`) — `Insert`, `LatestPerUser`, `PruneBefore`
  - `state.go` (+ `_test.go`) — `incident_state` & `daily_soft_flaps` queries
- `internal/backend/state/` — pure FSM
  - `fsm.go` (+ `_test.go`) — `Apply(prev, incoming) Transition` (one function, no I/O)
- `internal/backend/tg/` — Telegram bot HTTP client
  - `client.go` (+ `_test.go`) — `SendMessage`, `CreateForumTopic` against `httptest.Server`
- `internal/backend/alerts/` — formatter + dispatcher
  - `format.go` (+ `_test.go`) — `FormatHard`, `FormatRecovery`, `FormatRouterOffline`
  - `dispatcher.go` (+ `_test.go`) — translates FSM transitions into TG calls + DB updates
- `internal/backend/heartbeat/` — watcher goroutine
  - `watcher.go` (+ `_test.go`) — periodic scan of `MAX(events.ts)` per user
- `cmd/wg-monitor-cli/` — onboarding CLI
  - `main.go` (+ `add_user_test.go`) — single subcommand `add-user`

**Modified files:**
- `internal/agent/config.go` — append `Checks` section (awg.handshake_max_age_sec, awg.marker_url, dns.providers, dns.test_domain, dns.fail_threshold) + validation; `awg_iface` and `expected_exit_ip` move from `agent` block to `checks.awg`
- `internal/agent/reporter.go` — replace single-heartbeat loop with parallel `errgroup`-style fan-out across `[]checks.Check`, fixed per-check timeout (10 s), `agent_heartbeat=ok` becomes the report's "alive" signal but the wire stays the same
- `internal/agent/config_test.go` — extend for the new YAML schema
- `internal/agent/reporter_test.go` — extend with a fake `[]Check` slice
- `internal/backend/config.go` — drop `Agents` slice; add `DBPath`, `Telegram` (BotTokenFile, ChatID, AdminUserID), `Heartbeat` (StaleAfterSec, ScanIntervalSec), `State` (FailThreshold int, RecoveryThreshold int, RealertEverySec int)
- `internal/backend/handler.go` — `/v1/report` now resolves user by token hash via `db.Users.GetByTokenHash`, persists each check via `db.Events.Insert`, runs FSM per check, fires dispatcher
- `internal/backend/handler_test.go` — replace token-map fake with sqlite test DB + fake TG client
- `cmd/backend/main.go` — wire DB, FSM, dispatcher, heartbeat watcher; remove old `tokenToNickname` map
- `Makefile` — add `build-cli` target + cli to default `build-host`; add `cgo-check` (must stay `CGO_ENABLED=0`)
- `go.mod` — add `modernc.org/sqlite` and `golang.org/x/crypto`
- `deploy/backend/wg-monitor-backend.service` — append `StateDirectory=wg-monitor` (gives /var/lib/wg-monitor owned by service user), add `Environment=WGMON_DB=/var/lib/wg-monitor/state.db`

**Files NOT touched in Stage 1:** `pkg/wire/types.go` (frozen), `internal/agent/client.go` (HTTP client unchanged), `internal/backend/auth.go` (Bearer middleware unchanged — only the lookup map source changes), `deploy/backend/Caddyfile` (already correct), `deploy/agent/deploy_keenetic.py` (Stage 0 deploy still works for the new agent binary).

---

## Phase A — Agent: 4 real checks (Tasks 1-7)

Phase A is independent of Phase B and could in principle be parallelised in a worktree, but the recommended order keeps mental load low: build the agent end-to-end first, deploy it once, then start changing the backend wire format expectations.

### Task 1: `checks` package skeleton — interface, Deps, Runner

**Files:**
- Create: `internal/agent/checks/checks.go`
- Create: `internal/agent/checks/runner.go`
- Create: `internal/agent/checks/checks_test.go`
- Create: `internal/agent/checks/runner_test.go`

- [ ] **Step 1: Write failing test for Result helper**

```go
// internal/agent/checks/checks_test.go
package checks

import (
	"testing"
	"time"
)

func TestResultOK(t *testing.T) {
	start := time.Now().Add(-50 * time.Millisecond)
	r := OK("awg_handshake", start, map[string]any{"handshake_age_sec": 47})
	if r.Name != "awg_handshake" || r.Status != "ok" {
		t.Fatalf("bad name/status: %+v", r)
	}
	if r.DurationMs < 40 || r.DurationMs > 5000 {
		t.Fatalf("duration looks wrong: %d", r.DurationMs)
	}
	if r.Details["handshake_age_sec"] != 47 {
		t.Fatalf("details lost: %+v", r.Details)
	}
}

func TestResultFail(t *testing.T) {
	start := time.Now()
	r := Fail("awg_routing", start, "exit ip mismatch", map[string]any{"got": "1.2.3.4"})
	if r.Status != "fail" || r.Details["error"] != "exit ip mismatch" {
		t.Fatalf("unexpected: %+v", r)
	}
	if r.Details["got"] != "1.2.3.4" {
		t.Fatalf("extra details lost: %+v", r.Details)
	}
}
```

- [ ] **Step 2: Run, expect fail (undefined)**

```
cd /c/Users/Anex/Projects/wg-monitor
export PATH="$PATH:/c/Program Files/Go/bin"
go test ./internal/agent/checks/...
```
Expected: `undefined: OK` / `undefined: Fail`.

- [ ] **Step 3: Implement `checks.go`**

```go
// internal/agent/checks/checks.go
// Package checks defines the per-tick health checks the agent runs.
// Each Check is pure with respect to its Deps — that lets us mock subprocess
// and HTTP calls in tests instead of shelling out.
package checks

import (
	"context"
	"net/http"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type Check interface {
	Name() string
	Run(ctx context.Context, d Deps) wire.Check
}

// Deps is the set of injectable side effects every check may use.
// Concrete checks pick what they need; tests pass mocks.
type Deps struct {
	Runner     Runner       // subprocess executor (wg, dig)
	HTTPClient *http.Client // pre-configured with iface-bound dialer
}

func OK(name string, start time.Time, details map[string]any) wire.Check {
	if details == nil {
		details = map[string]any{}
	}
	return wire.Check{
		Name:       name,
		Status:     "ok",
		DurationMs: time.Since(start).Milliseconds(),
		Details:    details,
	}
}

func Fail(name string, start time.Time, errMsg string, details map[string]any) wire.Check {
	if details == nil {
		details = map[string]any{}
	}
	details["error"] = errMsg
	return wire.Check{
		Name:       name,
		Status:     "fail",
		DurationMs: time.Since(start).Milliseconds(),
		Details:    details,
	}
}
```

- [ ] **Step 4: Write failing test for Runner**

```go
// internal/agent/checks/runner_test.go
package checks

import (
	"context"
	"strings"
	"testing"
)

func TestOSExecRunsTrue(t *testing.T) {
	r := OSExec{}
	out, err := r.Run(context.Background(), "go", "version")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "go") {
		t.Fatalf("want 'go' in output, got %q", out)
	}
}

func TestRunnerFunc(t *testing.T) {
	r := RunnerFunc(func(ctx context.Context, name string, args ...string) (string, error) {
		return name + " " + strings.Join(args, "|"), nil
	})
	got, _ := r.Run(context.Background(), "wg", "show", "awg0")
	if got != "wg show|awg0" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 5: Run, expect fail**

`go test ./internal/agent/checks/... -run Runner` → undefined.

- [ ] **Step 6: Implement `runner.go`**

```go
// internal/agent/checks/runner.go
package checks

import (
	"context"
	"os/exec"
)

// Runner abstracts subprocess execution so checks are unit-testable
// without a real wg/dig binary on the dev box.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type OSExec struct{}

func (OSExec) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

type RunnerFunc func(ctx context.Context, name string, args ...string) (string, error)

func (f RunnerFunc) Run(ctx context.Context, name string, args ...string) (string, error) {
	return f(ctx, name, args...)
}
```

- [ ] **Step 7: Run all tests, expect PASS**

`go test ./internal/agent/checks/...`

- [ ] **Step 8: Commit**

```bash
git add internal/agent/checks/
git commit -m "feat(agent/checks): Check interface, Deps, OK/Fail helpers, Runner abstraction"
```

---

### Task 2: `awg_handshake` check

**Files:**
- Create: `internal/agent/checks/awg_handshake.go`
- Create: `internal/agent/checks/awg_handshake_test.go`

**Background:** `wg show <iface> latest-handshakes` prints lines like `<pubkey>\t<unix_ts>` (one per peer). The check passes if **any** peer's `now - unix_ts <= max_age_sec`. A `0` timestamp means "never", which is FAIL.

- [ ] **Step 1: Write failing test (table-driven, mocked Runner)**

```go
// internal/agent/checks/awg_handshake_test.go
package checks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAwgHandshake(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name     string
		stdout   string
		runErr   error
		want     string
		wantErr  string
	}{
		{
			name:   "fresh single peer",
			stdout: "abcdEF=\t" + itoa(now-30) + "\n",
			want:   "ok",
		},
		{
			name:   "stale single peer",
			stdout: "abcdEF=\t" + itoa(now-3600) + "\n",
			want:   "fail",
		},
		{
			name:   "never handshaked (0)",
			stdout: "abcdEF=\t0\n",
			want:   "fail",
		},
		{
			name:   "two peers one fresh",
			stdout: "aa==\t0\nbb==\t" + itoa(now-10) + "\n",
			want:   "ok",
		},
		{
			name:    "wg binary missing",
			runErr:  errors.New("exec: \"wg\": executable file not found"),
			want:    "fail",
			wantErr: "wg show failed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Deps{Runner: RunnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
				if name != "wg" {
					t.Fatalf("unexpected exec %s", name)
				}
				return c.stdout, c.runErr
			})}
			chk := AwgHandshake{Iface: "awg0", MaxAge: 180 * time.Second}
			got := chk.Run(context.Background(), d)
			if got.Status != c.want {
				t.Fatalf("status=%s want=%s details=%v", got.Status, c.want, got.Details)
			}
			if c.wantErr != "" && got.Details["error"] != c.wantErr {
				t.Fatalf("error=%v want=%q", got.Details["error"], c.wantErr)
			}
		})
	}
}

func itoa(i int64) string { return time.Unix(i, 0).Format("") + "" /*fallback*/ }
```

> **Note on `itoa`:** the helper above is wrong on purpose — Step 3 will replace it with `strconv.FormatInt`. We're showing real test code, not pseudo-code.

- [ ] **Step 2: Fix the test helper (replace `itoa` with `strconv.FormatInt`)**

Replace the `itoa` definition at the bottom of the test file with:

```go
import "strconv"

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
```

(Add `"strconv"` to the import block.)

- [ ] **Step 3: Run, expect fail (undefined `AwgHandshake`)**

`go test ./internal/agent/checks/... -run TestAwgHandshake`

- [ ] **Step 4: Implement `awg_handshake.go`**

```go
// internal/agent/checks/awg_handshake.go
package checks

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type AwgHandshake struct {
	Iface  string
	MaxAge time.Duration
}

func (AwgHandshake) Name() string { return "awg_handshake" }

func (c AwgHandshake) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	out, err := d.Runner.Run(ctx, "wg", "show", c.Iface, "latest-handshakes")
	if err != nil {
		return Fail(c.Name(), start, "wg show failed", map[string]any{"stderr": strings.TrimSpace(out)})
	}
	now := time.Now().Unix()
	freshest := int64(-1)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ts, perr := strconv.ParseInt(fields[1], 10, 64)
		if perr != nil || ts == 0 {
			continue
		}
		age := now - ts
		if freshest == -1 || age < freshest {
			freshest = age
		}
	}
	if freshest == -1 {
		return Fail(c.Name(), start, "no peer ever handshook", nil)
	}
	if time.Duration(freshest)*time.Second > c.MaxAge {
		return Fail(c.Name(), start, "stale", map[string]any{"handshake_age_sec": freshest})
	}
	return OK(c.Name(), start, map[string]any{"handshake_age_sec": freshest})
}
```

- [ ] **Step 5: Run tests, expect PASS**

`go test ./internal/agent/checks/...`

- [ ] **Step 6: Commit**

```bash
git add internal/agent/checks/awg_handshake.go internal/agent/checks/awg_handshake_test.go
git commit -m "feat(agent/checks): awg_handshake parses 'wg show latest-handshakes', flags >MaxAge"
```

---

### Task 3: `awg_routing` check + iface-bound dialer

**Files:**
- Create: `internal/agent/checks/dialer_linux.go`
- Create: `internal/agent/checks/dialer_other.go`
- Create: `internal/agent/checks/awg_routing.go`
- Create: `internal/agent/checks/awg_routing_test.go`

- [ ] **Step 1: Write the dialer build-tag stubs**

```go
// internal/agent/checks/dialer_linux.go
//go:build linux

package checks

import (
	"net"
	"syscall"
)

// IfaceDialer returns a *net.Dialer that binds outgoing sockets to the named
// interface via SO_BINDTODEVICE. Requires CAP_NET_RAW or root (the agent runs
// under root via Entware init.d, so this is fine).
func IfaceDialer(iface string) *net.Dialer {
	return &net.Dialer{
		Control: func(_, _ string, c syscall.RawConn) error {
			var setErr error
			ctlErr := c.Control(func(fd uintptr) {
				setErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface)
			})
			if setErr != nil {
				return setErr
			}
			return ctlErr
		},
	}
}
```

```go
// internal/agent/checks/dialer_other.go
//go:build !linux

package checks

import (
	"errors"
	"net"
)

// IfaceDialer is a non-Linux stub so go test ./... passes on Windows/macOS dev boxes.
// It returns a dialer whose Control rejects every connect — production code does not
// reach here because the agent only ships for linux/{mipsle,arm64}.
func IfaceDialer(iface string) *net.Dialer {
	return &net.Dialer{
		Control: func(_, _ string, _ interface{ Control(func(uintptr)) error }) error {
			return errors.New("SO_BINDTODEVICE only supported on linux")
		},
	}
}
```

> The non-Linux Control signature must match `func(string, string, syscall.RawConn) error`. To avoid importing `syscall` on non-Linux we leave the field nil and rely on tests injecting a custom `*http.Client`. **Simpler alternative we use instead** — drop the Control on non-Linux entirely:

Replace `dialer_other.go` with:

```go
//go:build !linux

package checks

import "net"

// IfaceDialer is a no-op on non-Linux. The agent only ships for linux/*, but we
// want go test ./... to compile on Windows. Tests of awg_routing pass an
// httptest.Server and never go through this dialer.
func IfaceDialer(_ string) *net.Dialer { return &net.Dialer{} }
```

- [ ] **Step 2: Write failing test for awg_routing (httptest server, no real iface binding)**

```go
// internal/agent/checks/awg_routing_test.go
package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAwgRoutingMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("89.125.101.122"))
	}))
	defer srv.Close()

	chk := AwgRouting{Iface: "ignored", URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
	if got.Details["got_ip"] != "89.125.101.122" {
		t.Fatalf("details: %+v", got.Details)
	}
}

func TestAwgRoutingMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("1.2.3.4"))
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" || got.Details["got_ip"] != "1.2.3.4" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestAwgRoutingHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" {
		t.Fatalf("expected fail on 502, got %+v", got)
	}
}
```

- [ ] **Step 3: Run, expect fail (undefined)**

`go test ./internal/agent/checks/... -run TestAwgRouting`

- [ ] **Step 4: Implement `awg_routing.go`**

```go
// internal/agent/checks/awg_routing.go
package checks

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type AwgRouting struct {
	Iface    string // informational; binding happens in the HTTPClient's dialer
	URL      string // e.g. https://api.ipify.org
	Expected string // expected egress IPv4
}

func (AwgRouting) Name() string { return "awg_routing" }

func (c AwgRouting) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, c.URL, nil)
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return Fail(c.Name(), start, "http error", map[string]any{"err": err.Error()})
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Fail(c.Name(), start, "non-2xx", map[string]any{"http_code": resp.StatusCode})
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	got := strings.TrimSpace(string(body))
	if got != c.Expected {
		return Fail(c.Name(), start, "exit ip mismatch", map[string]any{"got_ip": got, "expected_ip": c.Expected})
	}
	return OK(c.Name(), start, map[string]any{"got_ip": got})
}
```

- [ ] **Step 5: Run tests, expect PASS**

`go test ./internal/agent/checks/...`

- [ ] **Step 6: Commit**

```bash
git add internal/agent/checks/dialer_linux.go internal/agent/checks/dialer_other.go internal/agent/checks/awg_routing.go internal/agent/checks/awg_routing_test.go
git commit -m "feat(agent/checks): awg_routing + iface-bound dialer (SO_BINDTODEVICE on linux)"
```

---

### Task 4: `awg_marker` check (3-retry exponential backoff)

**Files:**
- Create: `internal/agent/checks/awg_marker.go`
- Create: `internal/agent/checks/awg_marker_test.go`

- [ ] **Step 1: Write failing test (httptest counter that fails N times then 200)**

```go
// internal/agent/checks/awg_marker_test.go
package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAwgMarkerSucceedsOnFirstTry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	chk := AwgMarker{URL: srv.URL, MaxRetries: 3, BaseBackoff: time.Millisecond}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "ok" || calls.Load() != 1 {
		t.Fatalf("status=%s calls=%d details=%v", got.Status, calls.Load(), got.Details)
	}
}

func TestAwgMarkerRecoversAfterTwoFails(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	chk := AwgMarker{URL: srv.URL, MaxRetries: 3, BaseBackoff: time.Millisecond}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "ok" || calls.Load() != 3 {
		t.Fatalf("status=%s calls=%d", got.Status, calls.Load())
	}
	if got.Details["attempts"].(int) != 3 {
		t.Fatalf("attempts=%v", got.Details["attempts"])
	}
}

func TestAwgMarkerExhausts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	chk := AwgMarker{URL: srv.URL, MaxRetries: 3, BaseBackoff: time.Millisecond}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" || calls.Load() != 3 {
		t.Fatalf("status=%s calls=%d", got.Status, calls.Load())
	}
}
```

- [ ] **Step 2: Run, expect fail (undefined)**

`go test ./internal/agent/checks/... -run TestAwgMarker`

- [ ] **Step 3: Implement `awg_marker.go`**

```go
// internal/agent/checks/awg_marker.go
package checks

import (
	"context"
	"net/http"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type AwgMarker struct {
	Iface       string
	URL         string
	MaxRetries  int           // total attempts (not retries on top of 1); spec says 3
	BaseBackoff time.Duration // first backoff; doubles each retry
}

func (AwgMarker) Name() string { return "awg_marker" }

func (c AwgMarker) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	maxRetries := c.MaxRetries
	if maxRetries < 1 {
		maxRetries = 3
	}
	backoff := c.BaseBackoff
	if backoff <= 0 {
		backoff = 250 * time.Millisecond
	}
	var lastCode int
	var lastErr string
	for attempt := 1; attempt <= maxRetries; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, c.URL, nil)
		resp, err := d.HTTPClient.Do(req)
		cancel()
		if err == nil && resp.StatusCode/100 == 2 {
			resp.Body.Close()
			return OK(c.Name(), start, map[string]any{"attempts": attempt, "http_code": resp.StatusCode})
		}
		if resp != nil {
			lastCode = resp.StatusCode
			resp.Body.Close()
		}
		if err != nil {
			lastErr = err.Error()
		}
		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return Fail(c.Name(), start, "ctx cancelled", map[string]any{"attempts": attempt})
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
	return Fail(c.Name(), start, "all retries failed", map[string]any{
		"attempts": maxRetries, "last_http_code": lastCode, "last_err": lastErr,
	})
}
```

- [ ] **Step 4: Run tests, expect PASS**

`go test ./internal/agent/checks/...`

- [ ] **Step 5: Commit**

```bash
git add internal/agent/checks/awg_marker.go internal/agent/checks/awg_marker_test.go
git commit -m "feat(agent/checks): awg_marker with 3-retry exponential backoff"
```

---

### Task 5: `dns_doh` check (3 providers, fail ≥2)

**Files:**
- Create: `internal/agent/checks/dns_doh.go`
- Create: `internal/agent/checks/dns_doh_test.go`

**Background:** `dig +tls @<host> <domain>` returns 0 on a successful answer, non-zero on timeout / handshake failure. Spec wording is "DoH" but the actual concrete tool is DoT — bind-utils `dig` supports `+tls` for DoT (port 853). The check name stays `dns_doh` to honor the spec contract; we add a TODO in the comment.

- [ ] **Step 1: Write failing test using mocked Runner**

```go
// internal/agent/checks/dns_doh_test.go
package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDNSDoH_AllOK(t *testing.T) {
	d := Deps{Runner: RunnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		if name != "dig" {
			t.Fatalf("expected dig, got %s", name)
		}
		return ";; ANSWER SECTION:\nexample.com. 60 IN A 93.184.216.34\n", nil
	})}
	chk := DNSDoH{
		Providers:     []DNSProvider{{Name: "cf", Host: "1.1.1.1"}, {Name: "g", Host: "8.8.8.8"}, {Name: "q9", Host: "9.9.9.9"}},
		TestDomain:    "example.com",
		FailThreshold: 2,
	}
	got := chk.Run(context.Background(), d)
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestDNSDoH_TwoFail_TriggersFail(t *testing.T) {
	calls := 0
	d := Deps{Runner: RunnerFunc(func(_ context.Context, _ string, _ ...string) (string, error) {
		calls++
		if calls <= 2 {
			return "", errors.New("connection timed out")
		}
		return ";; ANSWER SECTION:\nexample.com. 60 IN A 1.2.3.4\n", nil
	})}
	chk := DNSDoH{
		Providers:     []DNSProvider{{Name: "cf", Host: "1.1.1.1"}, {Name: "g", Host: "8.8.8.8"}, {Name: "q9", Host: "9.9.9.9"}},
		TestDomain:    "example.com",
		FailThreshold: 2,
	}
	got := chk.Run(context.Background(), d)
	if got.Status != "fail" {
		t.Fatalf("expected fail, got %+v", got)
	}
	failed, _ := got.Details["failed_providers"].([]string)
	if len(failed) != 2 {
		t.Fatalf("failed_providers=%v", failed)
	}
}

func TestDNSDoH_OneFail_StillOK(t *testing.T) {
	calls := 0
	d := Deps{Runner: RunnerFunc(func(_ context.Context, _ string, _ ...string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("timeout")
		}
		return "ANSWER SECTION", nil
	})}
	chk := DNSDoH{
		Providers:     []DNSProvider{{Name: "cf", Host: "1.1.1.1"}, {Name: "g", Host: "8.8.8.8"}, {Name: "q9", Host: "9.9.9.9"}},
		TestDomain:    "example.com",
		FailThreshold: 2,
	}
	got := chk.Run(context.Background(), d)
	if got.Status != "ok" {
		t.Fatalf("expected ok with 1/3 fail, got %+v", got)
	}
}

func TestDNSDoHRejectsEmptyAnswer(t *testing.T) {
	d := Deps{Runner: RunnerFunc(func(_ context.Context, _ string, _ ...string) (string, error) {
		return "no answer here", nil // no "ANSWER SECTION" → counted as fail
	})}
	chk := DNSDoH{
		Providers:     []DNSProvider{{Name: "cf", Host: "1.1.1.1"}, {Name: "g", Host: "8.8.8.8"}, {Name: "q9", Host: "9.9.9.9"}},
		TestDomain:    "example.com",
		FailThreshold: 2,
	}
	got := chk.Run(context.Background(), d)
	if got.Status != "fail" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got.Details["error"].(string), "providers failed") {
		t.Fatalf("err msg: %v", got.Details["error"])
	}
}
```

- [ ] **Step 2: Run, expect fail (undefined)**

`go test ./internal/agent/checks/... -run TestDNSDoH`

- [ ] **Step 3: Implement `dns_doh.go`**

```go
// internal/agent/checks/dns_doh.go
package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// TODO(stage-2+): rename "dns_doh" to "dns_dot" — the implementation uses DoT
// (port 853 via dig +tls). Keeping the spec name for now to avoid migration noise.

type DNSProvider struct {
	Name string
	Host string
}

type DNSDoH struct {
	Providers     []DNSProvider
	TestDomain    string
	FailThreshold int
}

func (DNSDoH) Name() string { return "dns_doh" }

func (c DNSDoH) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	var failed []string
	for _, p := range c.Providers {
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		out, err := d.Runner.Run(cctx, "dig", "+tls", "+short", "+timeout=3", "@"+p.Host, c.TestDomain)
		cancel()
		if err != nil || !looksLikeAnAnswer(out) {
			failed = append(failed, p.Name)
		}
	}
	if len(failed) >= c.FailThreshold {
		return Fail(c.Name(), start,
			fmt.Sprintf("%d providers failed", len(failed)),
			map[string]any{"failed_providers": failed, "checked": len(c.Providers)})
	}
	return OK(c.Name(), start, map[string]any{"failed_providers": failed, "checked": len(c.Providers)})
}

func looksLikeAnAnswer(out string) bool {
	o := strings.TrimSpace(out)
	if o == "" {
		return false
	}
	// dig +short prints just the IPs; dig without +short prints a section header.
	// We accept either: any non-empty trimmed output that contains a dot OR
	// the literal "ANSWER SECTION" header.
	return strings.Contains(o, "ANSWER SECTION") || strings.ContainsAny(o, "0123456789")
}
```

- [ ] **Step 4: Run tests, expect PASS**

`go test ./internal/agent/checks/...`

- [ ] **Step 5: Commit**

```bash
git add internal/agent/checks/dns_doh.go internal/agent/checks/dns_doh_test.go
git commit -m "feat(agent/checks): dns_doh runs dig +tls against 3 providers, FailThreshold-based"
```

---

### Task 6: Extend agent Config with `checks:` section

**Files:**
- Modify: `internal/agent/config.go`
- Modify: `internal/agent/config_test.go`

**Schema change:** `awg_iface` and `expected_exit_ip` move from `agent:` block to `checks.awg:`. The Stage 0 `local-values.yaml` will need a one-time edit during deploy (covered in Task 17).

- [ ] **Step 1: Add a failing test for the new schema**

Add to `internal/agent/config_test.go`:

```go
func TestLoadConfigWithChecksSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
backend:
  url: https://wgmonitor.jkaotlic.duckdns.org
  token: 0123456789abcdef0123456789abcdef0123456789abcdef
agent:
  nickname: testkeen
  interval_sec: 30
checks:
  awg:
    interface: awg0
    handshake_max_age_sec: 180
    expected_exit_ip: 89.125.101.122
    marker_url: https://www.youtube.com/-/manifest
  dns:
    test_domain: example.com
    fail_threshold: 2
    providers:
      - { name: cloudflare, host: 1.1.1.1 }
      - { name: google,     host: 8.8.8.8 }
      - { name: quad9,      host: 9.9.9.9 }
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Checks.AWG.Interface != "awg0" || cfg.Checks.AWG.ExpectedExitIP != "89.125.101.122" {
		t.Fatalf("awg parse: %+v", cfg.Checks.AWG)
	}
	if len(cfg.Checks.DNS.Providers) != 3 || cfg.Checks.DNS.FailThreshold != 2 {
		t.Fatalf("dns parse: %+v", cfg.Checks.DNS)
	}
	if cfg.Checks.AWG.HandshakeMaxAge() != 180*time.Second {
		t.Fatalf("max age: %v", cfg.Checks.AWG.HandshakeMaxAge())
	}
}

func TestLoadConfigRejectsMissingChecksAWG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := `
backend: { url: https://x.example, token: 0123456789abcdef0123456789abcdef0123456789abcdef }
agent: { nickname: testkeen }
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatalf("expected error on missing checks.awg")
	}
}
```

(Add `"path/filepath"`, `"os"`, `"time"` imports if not already present.)

- [ ] **Step 2: Run, expect fail (Checks field missing)**

`go test ./internal/agent/... -run TestLoadConfigWithChecksSection`

- [ ] **Step 3: Modify `internal/agent/config.go`**

Replace the existing `Config`, `AgentConfig`, and validation block with:

```go
type Config struct {
	Backend BackendConfig `yaml:"backend"`
	Agent   AgentConfig   `yaml:"agent"`
	Checks  ChecksConfig  `yaml:"checks"`
}

type BackendConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

type AgentConfig struct {
	Nickname    string `yaml:"nickname"`
	IntervalSec int    `yaml:"interval_sec"`
}

func (a AgentConfig) Interval() time.Duration {
	if a.IntervalSec <= 0 {
		return 60 * time.Second
	}
	return time.Duration(a.IntervalSec) * time.Second
}

type ChecksConfig struct {
	AWG AWGCheckConfig `yaml:"awg"`
	DNS DNSCheckConfig `yaml:"dns"`
}

type AWGCheckConfig struct {
	Interface             string `yaml:"interface"`
	HandshakeMaxAgeSec    int    `yaml:"handshake_max_age_sec"`
	ExpectedExitIP        string `yaml:"expected_exit_ip"`
	MarkerURL             string `yaml:"marker_url"`
	RoutingProbeURL       string `yaml:"routing_probe_url"` // default https://api.ipify.org
}

func (a AWGCheckConfig) HandshakeMaxAge() time.Duration {
	if a.HandshakeMaxAgeSec <= 0 {
		return 180 * time.Second
	}
	return time.Duration(a.HandshakeMaxAgeSec) * time.Second
}

func (a AWGCheckConfig) RoutingURL() string {
	if a.RoutingProbeURL != "" {
		return a.RoutingProbeURL
	}
	return "https://api.ipify.org"
}

type DNSProviderConfig struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
}

type DNSCheckConfig struct {
	Providers     []DNSProviderConfig `yaml:"providers"`
	TestDomain    string              `yaml:"test_domain"`
	FailThreshold int                 `yaml:"fail_threshold"`
}
```

Then in `LoadConfig`, after the existing token / nickname validation, replace the old `awg_iface` / `expected_exit_ip` checks with:

```go
	if cfg.Checks.AWG.Interface == "" {
		return nil, fmt.Errorf("checks.awg.interface is required (no default — per-user, see spec Q4)")
	}
	if cfg.Checks.AWG.ExpectedExitIP == "" {
		return nil, fmt.Errorf("checks.awg.expected_exit_ip is required (no default — per-user, see spec Q4)")
	}
	if cfg.Checks.AWG.MarkerURL == "" {
		return nil, fmt.Errorf("checks.awg.marker_url is required")
	}
	if cfg.Checks.DNS.TestDomain == "" {
		cfg.Checks.DNS.TestDomain = "example.com"
	}
	if cfg.Checks.DNS.FailThreshold <= 0 {
		cfg.Checks.DNS.FailThreshold = 2
	}
	if len(cfg.Checks.DNS.Providers) == 0 {
		cfg.Checks.DNS.Providers = []DNSProviderConfig{
			{Name: "cloudflare", Host: "1.1.1.1"},
			{Name: "google", Host: "8.8.8.8"},
			{Name: "quad9", Host: "9.9.9.9"},
		}
	}
```

- [ ] **Step 4: Update existing `config_test.go` cases**

The pre-existing tests reference `cfg.Agent.AwgIface` / `cfg.Agent.ExpectedExitIP`. Grep them out:

```bash
grep -n "AwgIface\|ExpectedExitIP" internal/agent/config_test.go
```

For each match, move the assertion to the new location (`cfg.Checks.AWG.Interface`, `cfg.Checks.AWG.ExpectedExitIP`) and update the YAML body accordingly.

- [ ] **Step 5: Run all agent tests, expect PASS**

`go test ./internal/agent/...`

- [ ] **Step 6: Commit**

```bash
git add internal/agent/config.go internal/agent/config_test.go
git commit -m "refactor(agent/config): hoist awg_iface/expected_exit_ip into checks.awg, add dns + marker"
```

---

### Task 7: Wire checks into Reporter (parallel fan-out)

**Files:**
- Modify: `internal/agent/reporter.go`
- Modify: `internal/agent/reporter_test.go`
- Modify: `cmd/agent/main.go`

- [ ] **Step 1: Failing test — Reporter runs all checks each tick**

Replace `internal/agent/reporter_test.go` (or add) with:

```go
package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/agent/checks"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeCheck struct {
	name   string
	calls  *atomic.Int32
	status string
}

func (f *fakeCheck) Name() string { return f.name }
func (f *fakeCheck) Run(_ context.Context, _ checks.Deps) wire.Check {
	f.calls.Add(1)
	return wire.Check{Name: f.name, Status: f.status, DurationMs: 1}
}

type fakeSender struct {
	mu   sync.Mutex
	last wire.Report
	n    int
}

func (s *fakeSender) SendReport(_ context.Context, r wire.Report) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = r
	s.n++
	return nil
}

func TestReporterFansOutAcrossChecks(t *testing.T) {
	c1 := atomic.Int32{}
	c2 := atomic.Int32{}
	chks := []checks.Check{
		&fakeCheck{name: "awg_handshake", status: "ok", calls: &c1},
		&fakeCheck{name: "dns_doh", status: "fail", calls: &c2},
	}
	s := &fakeSender{}
	r := NewReporter(s, "test", 10*time.Millisecond, chks, checks.Deps{})
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	time.Sleep(35 * time.Millisecond)
	cancel()
	if c1.Load() < 2 || c2.Load() < 2 {
		t.Fatalf("checks were not all run: c1=%d c2=%d", c1.Load(), c2.Load())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.last.Checks) != 3 { // 2 user-defined + agent_heartbeat
		t.Fatalf("checks in report: %d (%+v)", len(s.last.Checks), s.last.Checks)
	}
}
```

- [ ] **Step 2: Run, expect fail (signature mismatch)**

`go test ./internal/agent/... -run TestReporter`

- [ ] **Step 3: Rewrite `reporter.go`**

```go
// internal/agent/reporter.go
package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/agent/checks"
	"github.com/anex/wg-monitor/pkg/wire"
)

const perCheckTimeout = 10 * time.Second

type Sender interface {
	SendReport(ctx context.Context, r wire.Report) error
}

type Reporter struct {
	sender   Sender
	version  string
	interval time.Duration
	checks   []checks.Check
	deps     checks.Deps
}

func NewReporter(sender Sender, version string, interval time.Duration, chks []checks.Check, deps checks.Deps) *Reporter {
	return &Reporter{sender: sender, version: version, interval: interval, checks: chks, deps: deps}
}

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
	results := r.runAll(ctx)
	results = append(results, wire.Check{
		Name: "agent_heartbeat", Status: "ok", DurationMs: time.Since(start).Milliseconds(),
	})
	report := wire.Report{
		Timestamp:    start.UTC(),
		AgentVersion: r.version,
		Checks:       results,
	}
	if err := r.sender.SendReport(ctx, report); err != nil {
		slog.Warn("send report failed", "err", err)
	}
}

func (r *Reporter) runAll(parent context.Context) []wire.Check {
	out := make([]wire.Check, len(r.checks))
	var wg sync.WaitGroup
	for i, c := range r.checks {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, perCheckTimeout)
			defer cancel()
			out[i] = c.Run(ctx, r.deps)
		}()
	}
	wg.Wait()
	return out
}
```

- [ ] **Step 4: Update `cmd/agent/main.go` to build the check list from config**

Read the existing `cmd/agent/main.go` first, then replace the `NewReporter(...)` call with:

```go
	httpc := &http.Client{
		Transport: &http.Transport{
			DialContext: checks.IfaceDialer(cfg.Checks.AWG.Interface).DialContext,
		},
		Timeout: 12 * time.Second,
	}
	chks := []checks.Check{
		checks.AwgHandshake{Iface: cfg.Checks.AWG.Interface, MaxAge: cfg.Checks.AWG.HandshakeMaxAge()},
		checks.AwgRouting{Iface: cfg.Checks.AWG.Interface, URL: cfg.Checks.AWG.RoutingURL(), Expected: cfg.Checks.AWG.ExpectedExitIP},
		checks.AwgMarker{Iface: cfg.Checks.AWG.Interface, URL: cfg.Checks.AWG.MarkerURL, MaxRetries: 3, BaseBackoff: 250 * time.Millisecond},
		dnsCheckFromCfg(cfg.Checks.DNS),
	}
	deps := checks.Deps{Runner: checks.OSExec{}, HTTPClient: httpc}
	rep := agent.NewReporter(client, Version, cfg.Agent.Interval(), chks, deps)
```

Add a helper at the bottom of `cmd/agent/main.go`:

```go
func dnsCheckFromCfg(c agent.DNSCheckConfig) checks.DNSDoH {
	provs := make([]checks.DNSProvider, len(c.Providers))
	for i, p := range c.Providers {
		provs[i] = checks.DNSProvider{Name: p.Name, Host: p.Host}
	}
	return checks.DNSDoH{Providers: provs, TestDomain: c.TestDomain, FailThreshold: c.FailThreshold}
}
```

Add imports: `"net/http"`, `"github.com/anex/wg-monitor/internal/agent/checks"`.

- [ ] **Step 5: `make build-host` to verify everything compiles**

```
go build ./...
```
Expected: clean.

- [ ] **Step 6: Run all tests**

```
go test ./...
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/reporter.go internal/agent/reporter_test.go cmd/agent/main.go
git commit -m "feat(agent): Reporter fans out across []Check with per-check timeout"
```

---

## Phase B — Backend: SQLite store + state machine (Tasks 8-12)

### Task 8: Add `modernc.org/sqlite` + `db.Open` + migrations

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/backend/db/db.go`
- Create: `internal/backend/db/db_test.go`
- Create: `internal/backend/db/migrations.sql` (embedded)

- [ ] **Step 1: Add dependencies**

```bash
cd /c/Users/Anex/Projects/wg-monitor
export PATH="$PATH:/c/Program Files/Go/bin"
go get modernc.org/sqlite@latest
go get golang.org/x/crypto/bcrypt@latest
```

Expected: `go.mod` now lists both modules.

- [ ] **Step 2: Write the migrations file**

```sql
-- internal/backend/db/migrations.sql
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    nickname TEXT UNIQUE NOT NULL,
    token_hash TEXT NOT NULL,
    expected_exit_ip TEXT NOT NULL,
    awg_iface TEXT NOT NULL,
    telegram_thread_id INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    check_name TEXT NOT NULL,
    status TEXT NOT NULL,
    details_json TEXT,
    ts TIMESTAMP NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_events_user_ts ON events(user_id, ts DESC);

CREATE TABLE IF NOT EXISTS incident_state (
    user_id INTEGER NOT NULL,
    check_name TEXT NOT NULL,
    consecutive_fails INTEGER NOT NULL DEFAULT 0,
    consecutive_oks INTEGER NOT NULL DEFAULT 0,
    current_status TEXT NOT NULL DEFAULT 'ok',
    hard_since TIMESTAMP,
    last_alert_msg_id INTEGER,
    last_alert_at TIMESTAMP,
    silenced_until TIMESTAMP,
    acked_until TIMESTAMP,
    PRIMARY KEY (user_id, check_name),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS daily_soft_flaps (
    user_id INTEGER NOT NULL,
    check_name TEXT NOT NULL,
    flap_count INTEGER NOT NULL DEFAULT 0,
    date TEXT NOT NULL,
    PRIMARY KEY (user_id, check_name, date),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
```

> Note vs spec §5.2: added `consecutive_oks` (needed to detect 2-OK recovery without a separate query), `last_alert_at` (needed for the 6h re-alert clock). Both are additive.

- [ ] **Step 3: Failing test for `db.Open` + migration idempotency**

```go
// internal/backend/db/db_test.go
package db

import (
	"path/filepath"
	"testing"
)

func TestOpenAppliesMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	for _, table := range []string{"users", "events", "incident_state", "daily_soft_flaps"} {
		var name string
		err := d.SQL().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
}

func TestOpenIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	d.Close()
}
```

- [ ] **Step 4: Implement `db.go`**

```go
// internal/backend/db/db.go
// Package db wraps modernc.org/sqlite with our schema migrations and typed queries.
// modernc.org/sqlite is a pure-Go translation of SQLite — no cgo, cross-compiles cleanly.
package db

import (
	_ "embed"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed migrations.sql
var migrationsSQL string

type DB struct {
	db *sql.DB
}

func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := d.Exec(migrationsSQL); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &DB{db: d}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// SQL exposes the underlying *sql.DB for tests and ad-hoc queries.
// Production code should use the typed methods (Users(), Events(), etc.).
func (d *DB) SQL() *sql.DB { return d.db }
```

- [ ] **Step 5: Run tests, expect PASS**

`go test ./internal/backend/db/...`

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/backend/db/
git commit -m "feat(backend/db): pure-Go sqlite via modernc, embedded migrations for 4 tables"
```

---

### Task 9: Users queries (Insert with bcrypt, GetByTokenHash, etc.)

**Files:**
- Create: `internal/backend/db/users.go`
- Create: `internal/backend/db/users_test.go`

**Background on token storage:** spec §5.2 says `token_hash TEXT NOT NULL, -- bcrypt от raw token`. But bcrypt-comparing every incoming `/v1/report` request would blow CPU (bcrypt is slow by design — that's the point). Workaround: store **both** a fast SHA-256 lookup index (the actual `token_hash` column, indexed) and verify in constant time via `subtle.ConstantTimeCompare`. We never store raw tokens. Bcrypt can be added later if we ever need offline-replay-attack resistance — for now SHA-256 is fine because the DB is on the same box as the backend (compromise is symmetric).

> Decision: depart from spec wording — use SHA-256 hex of token, **not** bcrypt. Document this in `docs/superpowers/specs/2026-04-25-wg-monitor-design.md` Resolved-Questions follow-up after Stage 1 ships.

- [ ] **Step 1: Failing test**

```go
// internal/backend/db/users_test.go
package db

import (
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestInsertAndGetUser(t *testing.T) {
	d := newTestDB(t)
	rawToken := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	id, err := d.Users().Insert("vasya", rawToken, "1.2.3.4", "awg0")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("got id 0")
	}

	u, err := d.Users().GetByToken(rawToken)
	if err != nil {
		t.Fatalf("getbytoken: %v", err)
	}
	if u.Nickname != "vasya" || u.AWGIface != "awg0" || u.ExpectedExitIP != "1.2.3.4" {
		t.Fatalf("user: %+v", u)
	}

	if _, err := d.Users().GetByToken("wrongtoken"); err != ErrUserNotFound {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestInsertDuplicateNickname(t *testing.T) {
	d := newTestDB(t)
	tok1 := "11111111111111111111111111111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tok2 := "22222222222222222222222222222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := d.Users().Insert("vasya", tok1, "1.1.1.1", "awg0"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Users().Insert("vasya", tok2, "2.2.2.2", "awg1"); err == nil {
		t.Fatal("expected duplicate-nickname error")
	}
}

func TestUpdateLastSeenAndThreadID(t *testing.T) {
	d := newTestDB(t)
	tok := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	id, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	if err := d.Users().UpdateThreadID(id, 42); err != nil {
		t.Fatalf("thread: %v", err)
	}
	if err := d.Users().UpdateLastSeen(id); err != nil {
		t.Fatalf("seen: %v", err)
	}
	u, _ := d.Users().GetByToken(tok)
	if u.TelegramThreadID == nil || *u.TelegramThreadID != 42 {
		t.Fatalf("thread id: %+v", u.TelegramThreadID)
	}
	if u.LastSeenAt == nil {
		t.Fatal("last_seen_at not set")
	}
}

func TestGetAllUsers(t *testing.T) {
	d := newTestDB(t)
	for i, n := range []string{"a", "b", "c"} {
		tok := "0000000000000000000000000000000000000000000000000000000000000000"
		// build a unique 64-char token per user
		tok = string([]byte(tok)[:63]) + string('0'+rune(i))
		if _, err := d.Users().Insert(n, tok, "1.1.1.1", "awg0"); err != nil {
			t.Fatal(err)
		}
	}
	all, err := d.Users().GetAll()
	if err != nil || len(all) != 3 {
		t.Fatalf("getall: n=%d err=%v", len(all), err)
	}
}
```

- [ ] **Step 2: Run, expect undefined**

`go test ./internal/backend/db/... -run TestInsertAndGetUser`

- [ ] **Step 3: Implement `users.go`**

```go
// internal/backend/db/users.go
package db

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrUserNotFound = errors.New("user not found")

type User struct {
	ID               int64
	Nickname         string
	TokenHash        string
	ExpectedExitIP   string
	AWGIface         string
	TelegramThreadID *int64
	CreatedAt        time.Time
	LastSeenAt       *time.Time
}

type UsersRepo struct{ d *DB }

func (d *DB) Users() *UsersRepo { return &UsersRepo{d: d} }

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (u *UsersRepo) Insert(nickname, rawToken, expectedExitIP, awgIface string) (int64, error) {
	res, err := u.d.db.Exec(
		`INSERT INTO users(nickname, token_hash, expected_exit_ip, awg_iface) VALUES (?, ?, ?, ?)`,
		nickname, hashToken(rawToken), expectedExitIP, awgIface,
	)
	if err != nil {
		return 0, fmt.Errorf("users.Insert: %w", err)
	}
	return res.LastInsertId()
}

func (u *UsersRepo) GetByToken(rawToken string) (*User, error) {
	target := hashToken(rawToken)
	row := u.d.db.QueryRow(
		`SELECT id, nickname, token_hash, expected_exit_ip, awg_iface, telegram_thread_id, created_at, last_seen_at FROM users WHERE token_hash = ?`,
		target,
	)
	var got User
	var threadID sql.NullInt64
	var lastSeen sql.NullTime
	if err := row.Scan(&got.ID, &got.Nickname, &got.TokenHash, &got.ExpectedExitIP, &got.AWGIface, &threadID, &got.CreatedAt, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(got.TokenHash), []byte(target)) != 1 {
		// SHA-256 collision is astronomically unlikely; this branch is paranoia.
		return nil, ErrUserNotFound
	}
	if threadID.Valid {
		v := threadID.Int64
		got.TelegramThreadID = &v
	}
	if lastSeen.Valid {
		v := lastSeen.Time
		got.LastSeenAt = &v
	}
	return &got, nil
}

func (u *UsersRepo) GetByNickname(nickname string) (*User, error) {
	row := u.d.db.QueryRow(
		`SELECT id, nickname, token_hash, expected_exit_ip, awg_iface, telegram_thread_id, created_at, last_seen_at FROM users WHERE nickname = ?`,
		nickname,
	)
	var got User
	var threadID sql.NullInt64
	var lastSeen sql.NullTime
	if err := row.Scan(&got.ID, &got.Nickname, &got.TokenHash, &got.ExpectedExitIP, &got.AWGIface, &threadID, &got.CreatedAt, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if threadID.Valid {
		v := threadID.Int64
		got.TelegramThreadID = &v
	}
	if lastSeen.Valid {
		v := lastSeen.Time
		got.LastSeenAt = &v
	}
	return &got, nil
}

func (u *UsersRepo) GetAll() ([]User, error) {
	rows, err := u.d.db.Query(`SELECT id, nickname, expected_exit_ip, awg_iface, telegram_thread_id, last_seen_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var got User
		var threadID sql.NullInt64
		var lastSeen sql.NullTime
		if err := rows.Scan(&got.ID, &got.Nickname, &got.ExpectedExitIP, &got.AWGIface, &threadID, &lastSeen); err != nil {
			return nil, err
		}
		if threadID.Valid {
			v := threadID.Int64
			got.TelegramThreadID = &v
		}
		if lastSeen.Valid {
			v := lastSeen.Time
			got.LastSeenAt = &v
		}
		out = append(out, got)
	}
	return out, rows.Err()
}

func (u *UsersRepo) UpdateLastSeen(id int64) error {
	_, err := u.d.db.Exec(`UPDATE users SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

func (u *UsersRepo) UpdateThreadID(id, threadID int64) error {
	_, err := u.d.db.Exec(`UPDATE users SET telegram_thread_id = ? WHERE id = ?`, threadID, id)
	return err
}
```

- [ ] **Step 4: Run tests, expect PASS**

`go test ./internal/backend/db/...`

- [ ] **Step 5: Commit**

```bash
git add internal/backend/db/users.go internal/backend/db/users_test.go
git commit -m "feat(backend/db): users repo — SHA-256 token lookup, ConstantTimeCompare, thread_id update"
```

---

### Task 10: Events queries

**Files:**
- Create: `internal/backend/db/events.go`
- Create: `internal/backend/db/events_test.go`

- [ ] **Step 1: Failing test**

```go
// internal/backend/db/events_test.go
package db

import (
	"testing"
	"time"
)

func TestEventsInsertAndLatest(t *testing.T) {
	d := newTestDB(t)
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	now := time.Now().UTC()
	if err := d.Events().Insert(uid, "awg_handshake", "ok", `{"x":1}`, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "awg_handshake", "fail", `{"x":2}`, now); err != nil {
		t.Fatal(err)
	}

	got, err := d.Events().LatestPerUser(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsZero() {
		t.Fatal("got zero timestamp")
	}
	if got.Before(now.Add(-time.Second)) {
		t.Fatalf("got %v want close to %v", got, now)
	}
}

func TestEventsPruneBefore(t *testing.T) {
	d := newTestDB(t)
	tok := "2222222222222222222222222222222222222222222222222222222222222222"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	old := time.Now().Add(-30 * 24 * time.Hour)
	fresh := time.Now()
	d.Events().Insert(uid, "x", "ok", "", old)
	d.Events().Insert(uid, "x", "ok", "", fresh)

	n, err := d.Events().PruneBefore(time.Now().Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
}
```

- [ ] **Step 2: Run, expect undefined**

`go test ./internal/backend/db/... -run TestEvents`

- [ ] **Step 3: Implement `events.go`**

```go
// internal/backend/db/events.go
package db

import (
	"database/sql"
	"errors"
	"time"
)

type EventsRepo struct{ d *DB }

func (d *DB) Events() *EventsRepo { return &EventsRepo{d: d} }

func (e *EventsRepo) Insert(userID int64, checkName, status, detailsJSON string, ts time.Time) error {
	_, err := e.d.db.Exec(
		`INSERT INTO events(user_id, check_name, status, details_json, ts) VALUES (?, ?, ?, ?, ?)`,
		userID, checkName, status, detailsJSON, ts.UTC(),
	)
	return err
}

// LatestPerUser returns the timestamp of the most recent event across all checks for one user.
// Returns zero time and nil error if the user has no events yet.
func (e *EventsRepo) LatestPerUser(userID int64) (time.Time, error) {
	var ts sql.NullTime
	err := e.d.db.QueryRow(`SELECT MAX(ts) FROM events WHERE user_id = ?`, userID).Scan(&ts)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return ts.Time, nil
}

func (e *EventsRepo) PruneBefore(cutoff time.Time) (int64, error) {
	res, err := e.d.db.Exec(`DELETE FROM events WHERE ts < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

- [ ] **Step 4: Run tests, expect PASS**

`go test ./internal/backend/db/...`

- [ ] **Step 5: Commit**

```bash
git add internal/backend/db/events.go internal/backend/db/events_test.go
git commit -m "feat(backend/db): events repo — Insert, LatestPerUser, PruneBefore"
```

---

### Task 11: incident_state + daily_soft_flaps queries

**Files:**
- Create: `internal/backend/db/state.go`
- Create: `internal/backend/db/state_test.go`

- [ ] **Step 1: Failing test**

```go
// internal/backend/db/state_test.go
package db

import (
	"testing"
	"time"
)

func TestIncidentStateRoundtrip(t *testing.T) {
	d := newTestDB(t)
	tok := "3333333333333333333333333333333333333333333333333333333333333333"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	got, err := d.State().Get(uid, "awg_handshake")
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentStatus != "ok" || got.ConsecutiveFails != 0 {
		t.Fatalf("default state: %+v", got)
	}

	got.ConsecutiveFails = 2
	got.CurrentStatus = "fail"
	if err := d.State().Save(uid, "awg_handshake", got); err != nil {
		t.Fatal(err)
	}

	again, _ := d.State().Get(uid, "awg_handshake")
	if again.ConsecutiveFails != 2 || again.CurrentStatus != "fail" {
		t.Fatalf("roundtrip: %+v", again)
	}
}

func TestDailySoftFlapsIncr(t *testing.T) {
	d := newTestDB(t)
	tok := "4444444444444444444444444444444444444444444444444444444444444444"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	today := time.Now().UTC().Format("2006-01-02")

	for i := 0; i < 3; i++ {
		if err := d.State().IncSoftFlap(uid, "dns_doh", today); err != nil {
			t.Fatal(err)
		}
	}
	n, err := d.State().GetSoftFlap(uid, "dns_doh", today)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("got %d", n)
	}
}

func TestStaleHardsForRealert(t *testing.T) {
	d := newTestDB(t)
	tok := "5555555555555555555555555555555555555555555555555555555555555555"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	st := IncidentState{
		CurrentStatus: "hard",
		HardSince:     ptrTime(time.Now().Add(-7 * time.Hour)),
		LastAlertAt:   ptrTime(time.Now().Add(-7 * time.Hour)),
	}
	if err := d.State().Save(uid, "awg_handshake", st); err != nil {
		t.Fatal(err)
	}

	stale, err := d.State().StaleHards(time.Now().Add(-6 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 {
		t.Fatalf("got %d stale hards: %+v", len(stale), stale)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
```

- [ ] **Step 2: Run, expect undefined**

`go test ./internal/backend/db/... -run TestIncidentState`

- [ ] **Step 3: Implement `state.go`**

```go
// internal/backend/db/state.go
package db

import (
	"database/sql"
	"errors"
	"time"
)

type IncidentState struct {
	UserID           int64
	CheckName        string
	ConsecutiveFails int
	ConsecutiveOKs   int
	CurrentStatus    string // ok | fail | hard
	HardSince        *time.Time
	LastAlertMsgID   *int64
	LastAlertAt      *time.Time
	SilencedUntil    *time.Time
	AckedUntil       *time.Time
}

type StaleHard struct {
	UserID    int64
	CheckName string
	HardSince time.Time
}

type StateRepo struct{ d *DB }

func (d *DB) State() *StateRepo { return &StateRepo{d: d} }

func (s *StateRepo) Get(userID int64, checkName string) (IncidentState, error) {
	var got IncidentState
	got.UserID = userID
	got.CheckName = checkName
	got.CurrentStatus = "ok"

	row := s.d.db.QueryRow(
		`SELECT consecutive_fails, consecutive_oks, current_status, hard_since, last_alert_msg_id, last_alert_at, silenced_until, acked_until
		   FROM incident_state WHERE user_id = ? AND check_name = ?`,
		userID, checkName,
	)
	var hardSince, lastAlertAt, silenced, acked sql.NullTime
	var lastMsgID sql.NullInt64
	err := row.Scan(&got.ConsecutiveFails, &got.ConsecutiveOKs, &got.CurrentStatus,
		&hardSince, &lastMsgID, &lastAlertAt, &silenced, &acked)
	if errors.Is(err, sql.ErrNoRows) {
		return got, nil
	}
	if err != nil {
		return got, err
	}
	got.HardSince = nullTime(hardSince)
	got.LastAlertAt = nullTime(lastAlertAt)
	got.SilencedUntil = nullTime(silenced)
	got.AckedUntil = nullTime(acked)
	if lastMsgID.Valid {
		v := lastMsgID.Int64
		got.LastAlertMsgID = &v
	}
	return got, nil
}

func (s *StateRepo) Save(userID int64, checkName string, st IncidentState) error {
	_, err := s.d.db.Exec(
		`INSERT INTO incident_state(user_id, check_name, consecutive_fails, consecutive_oks, current_status,
		    hard_since, last_alert_msg_id, last_alert_at, silenced_until, acked_until)
		 VALUES(?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(user_id, check_name) DO UPDATE SET
		    consecutive_fails = excluded.consecutive_fails,
		    consecutive_oks   = excluded.consecutive_oks,
		    current_status    = excluded.current_status,
		    hard_since        = excluded.hard_since,
		    last_alert_msg_id = excluded.last_alert_msg_id,
		    last_alert_at     = excluded.last_alert_at,
		    silenced_until    = excluded.silenced_until,
		    acked_until       = excluded.acked_until`,
		userID, checkName, st.ConsecutiveFails, st.ConsecutiveOKs, st.CurrentStatus,
		st.HardSince, st.LastAlertMsgID, st.LastAlertAt, st.SilencedUntil, st.AckedUntil,
	)
	return err
}

func (s *StateRepo) IncSoftFlap(userID int64, checkName, date string) error {
	_, err := s.d.db.Exec(
		`INSERT INTO daily_soft_flaps(user_id, check_name, date, flap_count) VALUES (?,?,?,1)
		 ON CONFLICT(user_id, check_name, date) DO UPDATE SET flap_count = flap_count + 1`,
		userID, checkName, date,
	)
	return err
}

func (s *StateRepo) GetSoftFlap(userID int64, checkName, date string) (int, error) {
	var n int
	err := s.d.db.QueryRow(
		`SELECT flap_count FROM daily_soft_flaps WHERE user_id=? AND check_name=? AND date=?`,
		userID, checkName, date).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// StaleHards returns hard incidents whose last_alert_at is older than `cutoff`
// and which are not currently silenced.
func (s *StateRepo) StaleHards(cutoff time.Time) ([]StaleHard, error) {
	rows, err := s.d.db.Query(
		`SELECT user_id, check_name, hard_since FROM incident_state
		 WHERE current_status = 'hard'
		   AND last_alert_at < ?
		   AND (silenced_until IS NULL OR silenced_until < CURRENT_TIMESTAMP)`,
		cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StaleHard
	for rows.Next() {
		var sh StaleHard
		if err := rows.Scan(&sh.UserID, &sh.CheckName, &sh.HardSince); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

func nullTime(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	v := n.Time
	return &v
}
```

- [ ] **Step 4: Run tests, expect PASS**

`go test ./internal/backend/db/...`

- [ ] **Step 5: Commit**

```bash
git add internal/backend/db/state.go internal/backend/db/state_test.go
git commit -m "feat(backend/db): incident_state + daily_soft_flaps + StaleHards for re-alert"
```

---

### Task 12: State machine (pure FSM)

**Files:**
- Create: `internal/backend/state/fsm.go`
- Create: `internal/backend/state/fsm_test.go`

**Background:** the FSM is intentionally pure — no DB, no TG. It takes the previous `IncidentState` plus the incoming check status and returns a `Transition` describing what happened. The dispatcher (Task 14) then decides what to do (persist, send TG, etc).

- [ ] **Step 1: Failing tests — full coverage of the spec table**

```go
// internal/backend/state/fsm_test.go
package state

import (
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestFSM_OkOk_NoOp(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "ok"}
	tr := Apply(prev, "ok", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Noop {
		t.Fatalf("got %v", tr)
	}
}

func TestFSM_OkFail_StartsCounting(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "ok"}
	tr := Apply(prev, "fail", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Soft {
		t.Fatalf("kind=%v next=%+v", tr.Kind, tr.Next)
	}
	if tr.Next.ConsecutiveFails != 1 || tr.Next.CurrentStatus != "fail" {
		t.Fatalf("next: %+v", tr.Next)
	}
}

func TestFSM_FailFail_Hardens_ExactlyAtThreshold(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "fail", ConsecutiveFails: 2}
	now := time.Now()
	tr := Apply(prev, "fail", now, Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Hard {
		t.Fatalf("kind=%v", tr.Kind)
	}
	if tr.Next.CurrentStatus != "hard" {
		t.Fatalf("next status: %s", tr.Next.CurrentStatus)
	}
	if tr.Next.HardSince == nil || !tr.Next.HardSince.Equal(now) {
		t.Fatalf("hard_since: %v", tr.Next.HardSince)
	}
}

func TestFSM_FailFail_AfterHard_StaysHard(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 5}
	tr := Apply(prev, "fail", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Noop {
		t.Fatalf("hard+fail must noop, got %v", tr.Kind)
	}
}

func TestFSM_FailOk_FromSoft_FlipsBackToOk(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "fail", ConsecutiveFails: 2}
	tr := Apply(prev, "ok", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != SoftFlap {
		t.Fatalf("kind=%v", tr.Kind)
	}
	if tr.Next.ConsecutiveFails != 0 || tr.Next.CurrentStatus != "ok" {
		t.Fatalf("next: %+v", tr.Next)
	}
}

func TestFSM_HardOk_NeedsTwoConsecutiveOK(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "hard", ConsecutiveOKs: 0}
	tr := Apply(prev, "ok", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Noop {
		t.Fatalf("first ok in hard should noop, got %v (next=%+v)", tr.Kind, tr.Next)
	}
	if tr.Next.ConsecutiveOKs != 1 {
		t.Fatalf("oks: %d", tr.Next.ConsecutiveOKs)
	}

	prev = tr.Next
	tr = Apply(prev, "ok", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Recovery {
		t.Fatalf("second ok should recover, got %v", tr.Kind)
	}
	if tr.Next.CurrentStatus != "ok" {
		t.Fatalf("recovery should set ok, got %s", tr.Next.CurrentStatus)
	}
}

func TestFSM_HardFail_OkResetCountIfBroken(t *testing.T) {
	// hard → fail → ok → fail: after the second fail oks must reset to 0
	prev := db.IncidentState{CurrentStatus: "hard", ConsecutiveOKs: 1}
	tr := Apply(prev, "fail", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Next.ConsecutiveOKs != 0 {
		t.Fatalf("should reset oks, got %d", tr.Next.ConsecutiveOKs)
	}
}
```

- [ ] **Step 2: Run, expect undefined**

`go test ./internal/backend/state/...`

- [ ] **Step 3: Implement `fsm.go`**

```go
// internal/backend/state/fsm.go
// Package state implements the alert FSM described in spec §5.3.
// Pure: no I/O, no time.Now() — caller passes `now`. This makes the FSM
// trivially testable across millions of synthetic transitions.
package state

import (
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type Thresholds struct {
	Fail     int // consecutive fails to harden — spec default 3
	Recovery int // consecutive oks while hard to recover — spec default 2
}

type Kind int

const (
	Noop     Kind = iota // nothing observable to the operator
	Soft                 // failure but below threshold (just bumps counter)
	SoftFlap             // recovered before hard — increments daily_soft_flaps
	Hard                 // crossed threshold; HARD alert must be sent
	Recovery             // 2nd consecutive OK after hard; RECOVERY alert must be sent
)

func (k Kind) String() string {
	return [...]string{"noop", "soft", "soft_flap", "hard", "recovery"}[k]
}

type Transition struct {
	Kind Kind
	Next db.IncidentState
}

func Apply(prev db.IncidentState, incoming string, now time.Time, th Thresholds) Transition {
	next := prev
	switch {
	case prev.CurrentStatus == "ok" && incoming == "ok":
		next.ConsecutiveOKs = prev.ConsecutiveOKs + 1
		return Transition{Kind: Noop, Next: next}

	case prev.CurrentStatus == "ok" && incoming == "fail":
		next.ConsecutiveFails = 1
		next.ConsecutiveOKs = 0
		next.CurrentStatus = "fail"
		return Transition{Kind: Soft, Next: next}

	case prev.CurrentStatus == "fail" && incoming == "fail":
		next.ConsecutiveFails = prev.ConsecutiveFails + 1
		next.ConsecutiveOKs = 0
		if next.ConsecutiveFails >= th.Fail {
			next.CurrentStatus = "hard"
			t := now
			next.HardSince = &t
			next.LastAlertAt = &t
			return Transition{Kind: Hard, Next: next}
		}
		return Transition{Kind: Soft, Next: next}

	case prev.CurrentStatus == "fail" && incoming == "ok":
		next.ConsecutiveFails = 0
		next.ConsecutiveOKs = 1
		next.CurrentStatus = "ok"
		return Transition{Kind: SoftFlap, Next: next}

	case prev.CurrentStatus == "hard" && incoming == "fail":
		next.ConsecutiveOKs = 0
		next.ConsecutiveFails = prev.ConsecutiveFails + 1
		return Transition{Kind: Noop, Next: next}

	case prev.CurrentStatus == "hard" && incoming == "ok":
		next.ConsecutiveOKs = prev.ConsecutiveOKs + 1
		if next.ConsecutiveOKs >= th.Recovery {
			next.CurrentStatus = "ok"
			next.ConsecutiveFails = 0
			next.HardSince = nil
			return Transition{Kind: Recovery, Next: next}
		}
		return Transition{Kind: Noop, Next: next}
	}
	return Transition{Kind: Noop, Next: next}
}
```

- [ ] **Step 4: Run tests, expect PASS**

`go test ./internal/backend/state/...`

- [ ] **Step 5: Commit**

```bash
git add internal/backend/state/
git commit -m "feat(backend/state): pure FSM Apply(prev, incoming, now, th) — full spec §5.3 coverage"
```

---

## Phase C — Backend: Telegram + dispatcher + heartbeat watcher + handler integration (Tasks 13-15)

### Task 13: Telegram bot HTTP client

**Files:**
- Create: `internal/backend/tg/client.go`
- Create: `internal/backend/tg/client_test.go`

- [ ] **Step 1: Failing test (httptest server stands in for api.telegram.org)**

```go
// internal/backend/tg/client_test.go
package tg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendMessageInThread(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	mid, err := c.SendMessage(context.Background(), -100123, intPtr(7), "hi", "MarkdownV2", intPtr(99))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if mid != 42 {
		t.Fatalf("msg id: %d", mid)
	}
	if got["chat_id"].(float64) != -100123 {
		t.Fatalf("chat: %v", got["chat_id"])
	}
	if got["message_thread_id"].(float64) != 7 {
		t.Fatalf("thread: %v", got["message_thread_id"])
	}
	if got["reply_to_message_id"].(float64) != 99 {
		t.Fatalf("reply: %v", got["reply_to_message_id"])
	}
}

func TestCreateForumTopic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/createForumTopic") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true,"result":{"message_thread_id":555,"name":"vasya"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	tid, err := c.CreateForumTopic(context.Background(), -100123, "👤 vasya", 0xFF8C00)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tid != 555 {
		t.Fatalf("tid: %d", tid)
	}
}

func TestApiErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"ok":false,"error_code":403,"description":"bot was kicked"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	_, err := c.SendMessage(context.Background(), -100, nil, "x", "", nil)
	if err == nil || !strings.Contains(err.Error(), "bot was kicked") {
		t.Fatalf("err: %v", err)
	}
}

func intPtr(i int64) *int64 { return &i }
```

- [ ] **Step 2: Run, expect undefined**

`go test ./internal/backend/tg/...`

- [ ] **Step 3: Implement `client.go`**

```go
// internal/backend/tg/client.go
// Package tg is a hand-rolled net/http client for the small slice of the
// Telegram Bot API we need in Stage 1 (sendMessage, createForumTopic).
// We picked this over go-telegram-bot-api/v5 to keep the Stage 1 dep tree
// lean — full library will earn its place when callbacks land in Stage 2.
package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const DefaultBaseURL = "https://api.telegram.org/bot"

type Client struct {
	BaseURL string // typically https://api.telegram.org/bot — Token is appended
	Token   string
	HTTP    *http.Client
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Result      json.RawMessage `json:"result"`
}

type sendMessageReq struct {
	ChatID           int64  `json:"chat_id"`
	MessageThreadID  *int64 `json:"message_thread_id,omitempty"`
	Text             string `json:"text"`
	ParseMode        string `json:"parse_mode,omitempty"`
	ReplyToMessageID *int64 `json:"reply_to_message_id,omitempty"`
}

type sendMessageResult struct {
	MessageID int64 `json:"message_id"`
}

// SendMessage returns the message_id of the new message.
// Pass nil for threadID to post in General; pass nil for replyTo for top-level msgs.
func (c *Client) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	body, _ := json.Marshal(sendMessageReq{
		ChatID:           chatID,
		MessageThreadID:  threadID,
		Text:             text,
		ParseMode:        parseMode,
		ReplyToMessageID: replyTo,
	})
	var out sendMessageResult
	if err := c.call(ctx, "sendMessage", body, &out); err != nil {
		return 0, err
	}
	return out.MessageID, nil
}

type createTopicReq struct {
	ChatID    int64  `json:"chat_id"`
	Name      string `json:"name"`
	IconColor int    `json:"icon_color,omitempty"`
}

type createTopicResult struct {
	MessageThreadID int64 `json:"message_thread_id"`
}

func (c *Client) CreateForumTopic(ctx context.Context, chatID int64, name string, iconColor int) (int64, error) {
	body, _ := json.Marshal(createTopicReq{ChatID: chatID, Name: name, IconColor: iconColor})
	var out createTopicResult
	if err := c.call(ctx, "createForumTopic", body, &out); err != nil {
		return 0, err
	}
	return out.MessageThreadID, nil
}

func (c *Client) call(ctx context.Context, method string, body []byte, dst any) error {
	url := c.BaseURL + c.Token + "/" + method
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("tg %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	var ar apiResp
	if err := json.Unmarshal(raw, &ar); err != nil {
		return fmt.Errorf("tg %s: bad response (status %d): %s", method, resp.StatusCode, string(raw))
	}
	if !ar.OK {
		return fmt.Errorf("tg %s: %s (code=%d)", method, ar.Description, ar.ErrorCode)
	}
	if dst != nil {
		return json.Unmarshal(ar.Result, dst)
	}
	return nil
}
```

- [ ] **Step 4: Run tests, expect PASS**

`go test ./internal/backend/tg/...`

- [ ] **Step 5: Commit**

```bash
git add internal/backend/tg/
git commit -m "feat(backend/tg): pure-net/http TG client — sendMessage, createForumTopic"
```

---

### Task 14: Alert formatter + dispatcher

**Files:**
- Create: `internal/backend/alerts/format.go`
- Create: `internal/backend/alerts/format_test.go`
- Create: `internal/backend/alerts/dispatcher.go`
- Create: `internal/backend/alerts/dispatcher_test.go`

- [ ] **Step 1: Failing tests for formatter**

```go
// internal/backend/alerts/format_test.go
package alerts

import (
	"strings"
	"testing"
	"time"
)

func TestFormatHard(t *testing.T) {
	hardSince := time.Date(2026, 4, 26, 20, 3, 0, 0, time.UTC)
	got := FormatHard(HardArgs{
		Nickname:    "vasya",
		CheckName:   "awg_handshake",
		ConsecFails: 3,
		HardSince:   hardSince,
		Detail:      "handshake age 312s > 180s",
	})
	for _, want := range []string{"🔴", "vasya", "awg_handshake", "DOWN", "handshake age 312s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestFormatRecovery(t *testing.T) {
	since := time.Date(2026, 4, 26, 20, 3, 0, 0, time.UTC)
	got := FormatRecovery(RecoveryArgs{
		Nickname:    "vasya",
		CheckName:   "awg_handshake",
		HardSince:   since,
		RecoveredAt: since.Add(7 * time.Minute),
	})
	for _, want := range []string{"✅", "vasya", "RECOVERED", "7m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestFormatRouterOffline(t *testing.T) {
	got := FormatRouterOffline("vasya", 11*time.Minute)
	if !strings.Contains(got, "OFFLINE") || !strings.Contains(got, "vasya") || !strings.Contains(got, "11m") {
		t.Fatalf("got: %s", got)
	}
}
```

- [ ] **Step 2: Implement `format.go`**

```go
// internal/backend/alerts/format.go
package alerts

import (
	"fmt"
	"time"
)

type HardArgs struct {
	Nickname    string
	CheckName   string
	ConsecFails int
	HardSince   time.Time
	Detail      string
}

type RecoveryArgs struct {
	Nickname    string
	CheckName   string
	HardSince   time.Time
	RecoveredAt time.Time
}

func FormatHard(a HardArgs) string {
	return fmt.Sprintf(
		"🔴 [%s] %s — DOWN\nFails: %d подряд\nHard since: %s\n%s",
		a.Nickname, a.CheckName, a.ConsecFails,
		a.HardSince.In(mscLoc()).Format("2006-01-02 15:04:05 МСК"),
		a.Detail,
	)
}

func FormatRecovery(a RecoveryArgs) string {
	d := a.RecoveredAt.Sub(a.HardSince).Round(time.Minute)
	return fmt.Sprintf(
		"✅ [%s] %s — RECOVERED\nDowntime: %s",
		a.Nickname, a.CheckName, durFmt(d),
	)
}

func FormatRouterOffline(nickname string, since time.Duration) string {
	return fmt.Sprintf("🔴 [%s] ROUTER OFFLINE — нет heartbeat'ов %s", nickname, durFmt(since.Round(time.Minute)))
}

func durFmt(d time.Duration) string {
	if d < time.Minute {
		return "< 1m"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func mscLoc() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("МСК", 3*3600)
	}
	return loc
}
```

- [ ] **Step 3: Run formatter tests, expect PASS**

`go test ./internal/backend/alerts/... -run TestFormat`

- [ ] **Step 4: Failing tests for dispatcher**

```go
// internal/backend/alerts/dispatcher_test.go
package alerts

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
)

type fakeTG struct {
	mu       sync.Mutex
	sent     []sentMsg
	topicID  int64
	topicErr error
}

type sentMsg struct {
	chat     int64
	thread   *int64
	text     string
	replyTo  *int64
}

func (f *fakeTG) SendMessage(_ context.Context, chatID int64, threadID *int64, text, _ string, replyTo *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{chatID, threadID, text, replyTo})
	return int64(len(f.sent)) * 100, nil
}

func (f *fakeTG) CreateForumTopic(_ context.Context, _ int64, _ string, _ int) (int64, error) {
	if f.topicErr != nil {
		return 0, f.topicErr
	}
	if f.topicID == 0 {
		return 4242, nil
	}
	return f.topicID, nil
}

func newDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestDispatcherCreatesTopicLazily(t *testing.T) {
	d := newDB(t)
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	tg := &fakeTG{topicID: 7777}
	disp := NewDispatcher(d, tg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{
		Kind: state.Hard,
		Next: db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 3, HardSince: ptrT(time.Now())},
	}
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr, "details"); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(tg.sent) != 1 {
		t.Fatalf("sent %d messages", len(tg.sent))
	}
	if tg.sent[0].thread == nil || *tg.sent[0].thread != 7777 {
		t.Fatalf("thread: %v", tg.sent[0].thread)
	}
	u, _ := d.Users().GetByNickname("vasya")
	if u.TelegramThreadID == nil || *u.TelegramThreadID != 7777 {
		t.Fatalf("thread id not persisted: %+v", u.TelegramThreadID)
	}
}

func TestDispatcherRecoveryRepliesToHardMessage(t *testing.T) {
	d := newDB(t)
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	d.Users().UpdateThreadID(uid, 4242)
	hardMsgID := int64(999)
	d.State().Save(uid, "awg_handshake", db.IncidentState{
		CurrentStatus: "hard", LastAlertMsgID: &hardMsgID, HardSince: ptrT(time.Now().Add(-7 * time.Minute)),
	})
	tg := &fakeTG{}
	disp := NewDispatcher(d, tg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{
		Kind: state.Recovery,
		Next: db.IncidentState{CurrentStatus: "ok"},
	}
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr, ""); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(tg.sent) != 1 {
		t.Fatalf("sent: %d", len(tg.sent))
	}
	if tg.sent[0].replyTo == nil || *tg.sent[0].replyTo != 999 {
		t.Fatalf("replyTo: %v", tg.sent[0].replyTo)
	}
	if !strings.Contains(tg.sent[0].text, "RECOVERED") {
		t.Fatalf("text: %s", tg.sent[0].text)
	}
}

func TestDispatcherSoftFlapNoTGButCounted(t *testing.T) {
	d := newDB(t)
	tok := "2222222222222222222222222222222222222222222222222222222222222222"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	tg := &fakeTG{}
	disp := NewDispatcher(d, tg, Config{ChatID: -100, FailThreshold: 3, RecoveryThreshold: 2})

	tr := state.Transition{Kind: state.SoftFlap, Next: db.IncidentState{CurrentStatus: "ok"}}
	if err := disp.Handle(context.Background(), uid, "vasya", "awg_handshake", tr, ""); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(tg.sent) != 0 {
		t.Fatalf("soft flap must not send tg")
	}
	today := time.Now().UTC().Format("2006-01-02")
	n, _ := d.State().GetSoftFlap(uid, "awg_handshake", today)
	if n != 1 {
		t.Fatalf("flap count: %d", n)
	}
}

func ptrT(t time.Time) *time.Time { return &t }
```

- [ ] **Step 5: Implement `dispatcher.go`**

```go
// internal/backend/alerts/dispatcher.go
package alerts

import (
	"context"
	"fmt"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
)

type TGSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	CreateForumTopic(ctx context.Context, chatID int64, name string, iconColor int) (int64, error)
}

type Config struct {
	ChatID            int64
	FailThreshold     int
	RecoveryThreshold int
}

type Dispatcher struct {
	d   *db.DB
	tg  TGSender
	cfg Config
}

func NewDispatcher(d *db.DB, tg TGSender, cfg Config) *Dispatcher {
	return &Dispatcher{d: d, tg: tg, cfg: cfg}
}

func (di *Dispatcher) Handle(ctx context.Context, userID int64, nickname, checkName string, tr state.Transition, detail string) error {
	switch tr.Kind {
	case state.Noop, state.Soft:
		return di.d.State().Save(userID, checkName, tr.Next)
	case state.SoftFlap:
		today := time.Now().UTC().Format("2006-01-02")
		if err := di.d.State().IncSoftFlap(userID, checkName, today); err != nil {
			return err
		}
		return di.d.State().Save(userID, checkName, tr.Next)
	case state.Hard:
		threadID, err := di.ensureTopic(ctx, userID, nickname)
		if err != nil {
			return fmt.Errorf("ensure topic: %w", err)
		}
		text := FormatHard(HardArgs{
			Nickname:    nickname,
			CheckName:   checkName,
			ConsecFails: tr.Next.ConsecutiveFails,
			HardSince:   *tr.Next.HardSince,
			Detail:      detail,
		})
		mid, err := di.tg.SendMessage(ctx, di.cfg.ChatID, &threadID, text, "", nil)
		if err != nil {
			return err
		}
		next := tr.Next
		next.LastAlertMsgID = &mid
		now := time.Now()
		next.LastAlertAt = &now
		return di.d.State().Save(userID, checkName, next)
	case state.Recovery:
		threadID, err := di.ensureTopic(ctx, userID, nickname)
		if err != nil {
			return fmt.Errorf("ensure topic: %w", err)
		}
		prev, _ := di.d.State().Get(userID, checkName)
		var hardSince time.Time
		if prev.HardSince != nil {
			hardSince = *prev.HardSince
		}
		text := FormatRecovery(RecoveryArgs{
			Nickname:    nickname,
			CheckName:   checkName,
			HardSince:   hardSince,
			RecoveredAt: time.Now(),
		})
		_, err = di.tg.SendMessage(ctx, di.cfg.ChatID, &threadID, text, "", prev.LastAlertMsgID)
		if err != nil {
			return err
		}
		next := tr.Next
		next.LastAlertMsgID = nil
		next.LastAlertAt = nil
		return di.d.State().Save(userID, checkName, next)
	}
	return nil
}

// SendOffline sends a ROUTER OFFLINE notice (used by the heartbeat watcher).
func (di *Dispatcher) SendOffline(ctx context.Context, userID int64, nickname string, since time.Duration) error {
	threadID, err := di.ensureTopic(ctx, userID, nickname)
	if err != nil {
		return err
	}
	_, err = di.tg.SendMessage(ctx, di.cfg.ChatID, &threadID, FormatRouterOffline(nickname, since), "", nil)
	return err
}

func (di *Dispatcher) ensureTopic(ctx context.Context, userID int64, nickname string) (int64, error) {
	u, err := di.d.Users().GetByNickname(nickname)
	if err != nil {
		return 0, err
	}
	if u.TelegramThreadID != nil {
		return *u.TelegramThreadID, nil
	}
	tid, err := di.tg.CreateForumTopic(ctx, di.cfg.ChatID, "👤 "+nickname, 0xFF8C00)
	if err != nil {
		return 0, err
	}
	if err := di.d.Users().UpdateThreadID(userID, tid); err != nil {
		return 0, err
	}
	return tid, nil
}
```

- [ ] **Step 6: Run all alert tests, expect PASS**

`go test ./internal/backend/alerts/...`

- [ ] **Step 7: Commit**

```bash
git add internal/backend/alerts/
git commit -m "feat(backend/alerts): formatter + dispatcher (lazy topic create, recovery reply-to)"
```

---

### Task 15: Heartbeat watcher + handler integration + cmd/backend wire-up

**Files:**
- Create: `internal/backend/heartbeat/watcher.go`
- Create: `internal/backend/heartbeat/watcher_test.go`
- Modify: `internal/backend/handler.go`
- Modify: `internal/backend/handler_test.go`
- Modify: `internal/backend/auth.go` (lookup source: from map to *db.DB)
- Modify: `internal/backend/auth_test.go`
- Modify: `internal/backend/config.go` (Telegram, Heartbeat, State, DBPath)
- Modify: `internal/backend/config_test.go`
- Modify: `cmd/backend/main.go` (wire DB + dispatcher + watcher)

**Background:** the auth middleware no longer holds a `map[string]string`; instead it consults `db.Users().GetByToken(...)`. We keep `NicknameFromContext` and add `UserIDFromContext`.

- [ ] **Step 1: Failing tests for heartbeat watcher**

```go
// internal/backend/heartbeat/watcher_test.go
package heartbeat

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type fakeOffline struct {
	mu    sync.Mutex
	calls []callRec
}

type callRec struct {
	userID int64
	nick   string
	since  time.Duration
}

func (f *fakeOffline) SendOffline(_ context.Context, uid int64, nick string, since time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, callRec{uid, nick, since})
	return nil
}

func TestWatcherFiresOnceAfterStaleness(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	old := time.Now().Add(-10 * time.Minute).UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", old)

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: 25 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	w.WaitForExit()

	off.mu.Lock()
	defer off.mu.Unlock()
	if len(off.calls) == 0 {
		t.Fatal("expected at least one offline notice")
	}
	if off.calls[0].nick != "vasya" {
		t.Fatalf("nick: %s", off.calls[0].nick)
	}
}

func TestWatcherDoesNotFireWhenFresh(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", time.Now().UTC())

	off := &fakeOffline{}
	w := NewWatcher(d, off, Config{StaleAfter: 5 * time.Minute, ScanEvery: 25 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	w.WaitForExit()

	off.mu.Lock()
	defer off.mu.Unlock()
	if len(off.calls) != 0 {
		t.Fatalf("got %d calls for fresh user", len(off.calls))
	}
}

var _ atomic.Value // keep the import alive in case of future use
```

- [ ] **Step 2: Implement `watcher.go`**

```go
// internal/backend/heartbeat/watcher.go
package heartbeat

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type OfflineSender interface {
	SendOffline(ctx context.Context, userID int64, nickname string, since time.Duration) error
}

type Config struct {
	StaleAfter time.Duration // e.g. 5*time.Minute
	ScanEvery  time.Duration // e.g. 30*time.Second
}

type Watcher struct {
	d        *db.DB
	off      OfflineSender
	cfg      Config
	notified map[int64]time.Time
	mu       sync.Mutex
	wg       sync.WaitGroup
}

func NewWatcher(d *db.DB, off OfflineSender, cfg Config) *Watcher {
	return &Watcher{d: d, off: off, cfg: cfg, notified: map[int64]time.Time{}}
}

func (w *Watcher) Run(ctx context.Context) {
	w.wg.Add(1)
	defer w.wg.Done()
	w.scan(ctx)
	t := time.NewTicker(w.cfg.ScanEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.scan(ctx)
		}
	}
}

func (w *Watcher) WaitForExit() { w.wg.Wait() }

func (w *Watcher) scan(ctx context.Context) {
	users, err := w.d.Users().GetAll()
	if err != nil {
		slog.Warn("heartbeat scan: list users", "err", err)
		return
	}
	now := time.Now()
	for _, u := range users {
		latest, err := w.d.Events().LatestPerUser(u.ID)
		if err != nil {
			continue
		}
		if latest.IsZero() {
			continue // user has never reported — no false positive at first start
		}
		stale := now.Sub(latest)
		if stale < w.cfg.StaleAfter {
			w.mu.Lock()
			delete(w.notified, u.ID) // came back online → forget previous notice
			w.mu.Unlock()
			continue
		}
		w.mu.Lock()
		last, sent := w.notified[u.ID]
		notify := !sent || now.Sub(last) > 6*time.Hour
		if notify {
			w.notified[u.ID] = now
		}
		w.mu.Unlock()
		if !notify {
			continue
		}
		if err := w.off.SendOffline(ctx, u.ID, u.Nickname, stale); err != nil {
			slog.Warn("heartbeat: send offline failed", "nickname", u.Nickname, "err", err)
		}
	}
}
```

- [ ] **Step 3: Run watcher tests, expect PASS**

`go test ./internal/backend/heartbeat/...`

- [ ] **Step 4: Refactor `auth.go` to look up via the DB**

Replace `internal/backend/auth.go` body with:

```go
package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type ctxKey int

const (
	ctxKeyNickname ctxKey = iota
	ctxKeyUserID
)

type UserLookup interface {
	GetByToken(rawToken string) (*db.User, error)
}

func AuthMiddleware(lookup UserLookup) func(http.Handler) http.Handler {
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
			u, err := lookup.GetByToken(presented)
			if err != nil {
				if errors.Is(err, db.ErrUserNotFound) {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				http.Error(w, "auth lookup failed", http.StatusInternalServerError)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyNickname, u.Nickname)
			ctx = context.WithValue(ctx, ctxKeyUserID, u.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func NicknameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyNickname).(string)
	return v
}

func UserIDFromContext(ctx context.Context) int64 {
	v, _ := ctx.Value(ctxKeyUserID).(int64)
	return v
}
```

- [ ] **Step 5: Update `auth_test.go` — replace map fake with a fake `UserLookup`**

Replace the body of `internal/backend/auth_test.go` with:

```go
package backend

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type fakeLookup struct {
	want string
	user *db.User
}

func (f *fakeLookup) GetByToken(raw string) (*db.User, error) {
	if raw == f.want {
		return f.user, nil
	}
	return nil, db.ErrUserNotFound
}

func TestAuthMiddleware_OK(t *testing.T) {
	l := &fakeLookup{want: "tok-abc", user: &db.User{ID: 7, Nickname: "vasya"}}
	mw := AuthMiddleware(l)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/report", nil)
	req.Header.Set("Authorization", "Bearer tok-abc")
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := NicknameFromContext(r.Context()); got != "vasya" {
			t.Fatalf("nick: %s", got)
		}
		if got := UserIDFromContext(r.Context()); got != 7 {
			t.Fatalf("uid: %d", got)
		}
		w.WriteHeader(204)
	})).ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("code: %d", rec.Code)
	}
}

func TestAuthMiddleware_Reject(t *testing.T) {
	l := &fakeLookup{want: "right"}
	mw := AuthMiddleware(l)
	for _, hdr := range []string{"", "Bearer ", "Bearer wrong", "tok-abc"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/report", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		called := false
		mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(204)
		})).ServeHTTP(rec, req)
		if called {
			t.Fatalf("hdr %q: handler should not have been called", hdr)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("hdr %q: code %d", hdr, rec.Code)
		}
	}
}

var _ = errors.New // import keeper
```

- [ ] **Step 6: Update `handler.go` — wire FSM + dispatcher**

Replace `internal/backend/handler.go` with:

```go
package backend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/pkg/wire"
)

const maxReportBytes = 64 * 1024

type Dispatcher interface {
	Handle(ctx context.Context, userID int64, nickname, checkName string, tr state.Transition, detail string) error
}

type Deps struct {
	Logger     *slog.Logger
	DB         *db.DB
	Dispatcher Dispatcher
	Thresholds state.Thresholds
}

func NewMux(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	auth := AuthMiddleware(d.DB.Users())
	mux.Handle("/v1/report", auth(http.HandlerFunc(reportHandler(d))))
	return mux
}

func reportHandler(d Deps) http.HandlerFunc {
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
		uid := UserIDFromContext(r.Context())
		nick := NicknameFromContext(r.Context())

		_ = d.DB.Users().UpdateLastSeen(uid)
		ts := rep.Timestamp
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		for _, c := range rep.Checks {
			detailsJSON, _ := json.Marshal(c.Details)
			if err := d.DB.Events().Insert(uid, c.Name, c.Status, string(detailsJSON), ts); err != nil {
				d.Logger.Warn("event insert", "nickname", nick, "check", c.Name, "err", err)
				continue
			}
			if c.Name == "agent_heartbeat" {
				continue // heartbeat is delivery confirmation only — no FSM
			}
			prev, err := d.DB.State().Get(uid, c.Name)
			if err != nil {
				d.Logger.Warn("state.Get", "err", err)
				continue
			}
			tr := state.Apply(prev, c.Status, time.Now(), d.Thresholds)
			detail := buildDetail(c)
			if err := d.Dispatcher.Handle(r.Context(), uid, nick, c.Name, tr, detail); err != nil {
				d.Logger.Warn("dispatch", "check", c.Name, "kind", tr.Kind, "err", err)
			}
		}
		d.Logger.Info("report",
			"nickname", nick, "agent_version", rep.AgentVersion,
			"check_count", len(rep.Checks), "checks", checkSummary(rep.Checks),
		)
		w.WriteHeader(http.StatusOK)
	}
}

func buildDetail(c wire.Check) string {
	if c.Status == "ok" {
		return ""
	}
	if e, ok := c.Details["error"].(string); ok {
		return e
	}
	b, _ := json.Marshal(c.Details)
	return string(b)
}

func checkSummary(checks []wire.Check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Name + "=" + c.Status
	}
	return out
}
```

- [ ] **Step 7: Update `handler_test.go` — wire a real test DB + fake dispatcher**

Replace `internal/backend/handler_test.go` with:

```go
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeDisp struct {
	mu    sync.Mutex
	calls []state.Kind
}

func (f *fakeDisp) Handle(_ context.Context, _ int64, _, _ string, tr state.Transition, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tr.Kind)
	return nil
}

func TestReportPersistsEventsAndDispatches(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	disp := &fakeDisp{}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Checks: []wire.Check{
			{Name: "agent_heartbeat", Status: "ok"},
			{Name: "awg_handshake", Status: "fail", Details: map[string]any{"error": "stale"}},
		},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	disp.mu.Lock()
	if len(disp.calls) != 1 {
		t.Fatalf("dispatcher invoked %d times (heartbeat must NOT trigger)", len(disp.calls))
	}
	if disp.calls[0] != state.Soft {
		t.Fatalf("kind: %v", disp.calls[0])
	}
	disp.mu.Unlock()

	latest, _ := d.Events().LatestPerUser(uid)
	if latest.IsZero() {
		t.Fatal("event not persisted")
	}
}

func TestReportRejectsTooLarge(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{},
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	huge := bytes.Repeat([]byte("A"), 80*1024)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(huge))
	req.Header.Set("Authorization", "Bearer x")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		// auth fires first because the token is unknown
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
```

- [ ] **Step 8: Update backend `config.go` — drop `Agents`, add `DBPath`, `Telegram`, `Heartbeat`, `State`**

Replace the body of `internal/backend/config.go` with:

```go
package backend

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen    string          `yaml:"listen"`
	LogLevel  string          `yaml:"log_level"`
	DBPath    string          `yaml:"db_path"`
	Telegram  TelegramConfig  `yaml:"telegram"`
	Heartbeat HeartbeatConfig `yaml:"heartbeat"`
	State     StateConfig     `yaml:"state"`
}

type TelegramConfig struct {
	BotTokenFile string `yaml:"bot_token_file"`
	BotToken     string `yaml:"-"` // populated by LoadConfig from BotTokenFile
	ChatID       int64  `yaml:"chat_id"`
	AdminUserID  int64  `yaml:"admin_user_id"`
}

type HeartbeatConfig struct {
	StaleAfterSec  int `yaml:"stale_after_sec"`
	ScanIntervalSec int `yaml:"scan_interval_sec"`
}

type StateConfig struct {
	FailThreshold     int `yaml:"fail_threshold"`
	RecoveryThreshold int `yaml:"recovery_threshold"`
	RealertEverySec   int `yaml:"realert_every_sec"`
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
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("db_path is required")
	}
	if cfg.Telegram.BotTokenFile == "" {
		return nil, fmt.Errorf("telegram.bot_token_file is required")
	}
	tokBytes, err := os.ReadFile(cfg.Telegram.BotTokenFile)
	if err != nil {
		return nil, fmt.Errorf("read bot_token_file: %w", err)
	}
	cfg.Telegram.BotToken = strings.TrimSpace(string(tokBytes))
	if cfg.Telegram.BotToken == "" {
		return nil, fmt.Errorf("bot_token_file is empty")
	}
	if cfg.Telegram.ChatID == 0 {
		return nil, fmt.Errorf("telegram.chat_id is required")
	}
	if cfg.Telegram.AdminUserID == 0 {
		return nil, fmt.Errorf("telegram.admin_user_id is required")
	}
	if cfg.Heartbeat.StaleAfterSec == 0 {
		cfg.Heartbeat.StaleAfterSec = 300 // 5 min
	}
	if cfg.Heartbeat.ScanIntervalSec == 0 {
		cfg.Heartbeat.ScanIntervalSec = 30
	}
	if cfg.State.FailThreshold == 0 {
		cfg.State.FailThreshold = 3
	}
	if cfg.State.RecoveryThreshold == 0 {
		cfg.State.RecoveryThreshold = 2
	}
	if cfg.State.RealertEverySec == 0 {
		cfg.State.RealertEverySec = 6 * 3600
	}
	return &cfg, nil
}
```

- [ ] **Step 9: Update `config_test.go`**

Replace `internal/backend/config_test.go` with tests that build a tmp YAML + tmp bot-token file:

```go
package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "secret-bot-token-xyz")
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
	if cfg.Telegram.BotToken != "secret-bot-token-xyz" {
		t.Fatalf("token: %q", cfg.Telegram.BotToken)
	}
	if cfg.State.FailThreshold != 3 || cfg.State.RecoveryThreshold != 2 {
		t.Fatalf("state defaults: %+v", cfg.State)
	}
	if cfg.Heartbeat.StaleAfterSec != 300 {
		t.Fatalf("hb default: %d", cfg.Heartbeat.StaleAfterSec)
	}
}

func TestLoadConfigRejectsMissingChatID(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "x")
	cfgPath := writeFile(t, dir, "c.yaml", `
db_path: /tmp/state.db
telegram:
  bot_token_file: `+tokPath+`
  admin_user_id: 1
`)
	if _, err := LoadConfig(cfgPath); err == nil {
		t.Fatal("expected chat_id required")
	}
}
```

- [ ] **Step 10: Rewrite `cmd/backend/main.go` — wire everything**

Read the existing `cmd/backend/main.go` and replace its body with:

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
	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/heartbeat"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/internal/backend/tg"
)

var Version = "0.2.0-stage1-dev"

func main() {
	cfgPath := flag.String("config", "/etc/wg-monitor/backend.yaml", "path to backend config yaml")
	flag.Parse()

	cfg, err := backend.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)

	d, err := db.Open(cfg.DBPath)
	if err != nil {
		logger.Error("db open", "err", err)
		os.Exit(2)
	}
	defer d.Close()

	tgClient := &tg.Client{
		BaseURL: tg.DefaultBaseURL,
		Token:   cfg.Telegram.BotToken,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
	disp := alerts.NewDispatcher(d, tgClient, alerts.Config{
		ChatID:            cfg.Telegram.ChatID,
		FailThreshold:     cfg.State.FailThreshold,
		RecoveryThreshold: cfg.State.RecoveryThreshold,
	})

	mux := backend.NewMux(backend.Deps{
		Logger:     logger,
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: cfg.State.FailThreshold, Recovery: cfg.State.RecoveryThreshold},
	})
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	watcher := heartbeat.NewWatcher(d, disp, heartbeat.Config{
		StaleAfter: time.Duration(cfg.Heartbeat.StaleAfterSec) * time.Second,
		ScanEvery:  time.Duration(cfg.Heartbeat.ScanIntervalSec) * time.Second,
	})
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go watcher.Run(ctx)

	go func() {
		logger.Info("backend listening", "addr", cfg.Listen, "version", Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	watcher.WaitForExit()
	logger.Info("backend stopped")
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
```

- [ ] **Step 11: Build everything**

```
go build ./...
```
Expected: clean.

- [ ] **Step 12: Run all tests**

```
go test ./...
```
Expected: PASS.

- [ ] **Step 13: Commit**

```bash
git add internal/backend/heartbeat/ internal/backend/handler.go internal/backend/handler_test.go internal/backend/auth.go internal/backend/auth_test.go internal/backend/config.go internal/backend/config_test.go cmd/backend/main.go
git commit -m "feat(backend): wire DB+FSM+dispatcher into /v1/report, add heartbeat watcher, drop yaml-agents block"
```

---

## Phase D — CLI add-user + live verification (Tasks 16-17)

### Task 16: `wg-monitor-cli add-user` command

**Files:**
- Create: `cmd/wg-monitor-cli/main.go`
- Create: `cmd/wg-monitor-cli/add_user_test.go`
- Modify: `Makefile` (add `build-cli` target)

**Background:** the CLI talks to SQLite directly (same `internal/backend/db` package), not over HTTP. That keeps the CLI deployable as a single binary with no network dependency on the backend, and lets us run it on the same VPS where the DB lives.

- [ ] **Step 1: Failing test for add-user — generate token, insert, print install hint**

```go
// cmd/wg-monitor-cli/add_user_test.go
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestAddUserHappyPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	var out bytes.Buffer
	err = runAddUser(addUserOpts{
		DBPath:         dbPath,
		Nickname:       "vasya",
		AWGIface:       "awg0",
		ExpectedExitIP: "89.125.101.122",
		BackendURL:     "https://wgmonitor.jkaotlic.duckdns.org",
		Out:            &out,
	})
	if err != nil {
		t.Fatalf("add-user: %v", err)
	}
	got := out.String()
	for _, want := range []string{"vasya", "Token (raw):", "config.yaml", "https://wgmonitor.jkaotlic.duckdns.org"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}

	// Re-open DB and confirm the user is there
	d, _ = db.Open(dbPath)
	defer d.Close()
	u, err := d.Users().GetByNickname("vasya")
	if err != nil {
		t.Fatalf("user not found: %v", err)
	}
	if u.AWGIface != "awg0" || u.ExpectedExitIP != "89.125.101.122" {
		t.Fatalf("user fields: %+v", u)
	}
}

func TestAddUserRejectsBadNickname(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	err := runAddUser(addUserOpts{
		DBPath: dbPath, Nickname: "Vasya!", AWGIface: "awg0",
		ExpectedExitIP: "1.1.1.1", BackendURL: "https://x", Out: &out,
	})
	if err == nil {
		t.Fatal("expected nickname validation error")
	}
}

func TestAddUserRejectsDuplicate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	opts := addUserOpts{
		DBPath: dbPath, Nickname: "vasya", AWGIface: "awg0",
		ExpectedExitIP: "1.1.1.1", BackendURL: "https://x", Out: &out,
	}
	if err := runAddUser(opts); err != nil {
		t.Fatal(err)
	}
	if err := runAddUser(opts); err == nil {
		t.Fatal("expected duplicate error on second add")
	}
}
```

- [ ] **Step 2: Run, expect undefined**

`go test ./cmd/wg-monitor-cli/...`

- [ ] **Step 3: Implement `cmd/wg-monitor-cli/main.go`**

```go
// cmd/wg-monitor-cli/main.go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/anex/wg-monitor/internal/backend/db"
)

var Version = "0.2.0-stage1-dev"

var nicknameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage())
		os.Exit(2)
	}
	cmd := os.Args[1]
	switch cmd {
	case "add-user":
		fs := flag.NewFlagSet("add-user", flag.ExitOnError)
		dbPath := fs.String("db", "/var/lib/wg-monitor/state.db", "path to SQLite DB")
		nick := fs.String("nickname", "", "user nickname (regexp ^[a-z][a-z0-9_-]{1,15}$)")
		iface := fs.String("awg-iface", "", "AWG interface name on the router (per-user, see spec Q4)")
		exitIP := fs.String("expected-exit-ip", "", "expected exit IPv4 when probing through the tunnel")
		backendURL := fs.String("backend-url", "https://wgmonitor.jkaotlic.duckdns.org", "backend HTTPS URL printed in install hint")
		_ = fs.Parse(os.Args[2:])
		if err := runAddUser(addUserOpts{
			DBPath: *dbPath, Nickname: *nick, AWGIface: *iface,
			ExpectedExitIP: *exitIP, BackendURL: *backendURL, Out: os.Stdout,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(Version)
	default:
		fmt.Fprintln(os.Stderr, usage())
		os.Exit(2)
	}
}

func usage() string {
	return `wg-monitor-cli — onboarding CLI

Usage:
  wg-monitor-cli add-user --nickname=NAME --awg-iface=IFACE --expected-exit-ip=IP [--db PATH] [--backend-url URL]
  wg-monitor-cli version
`
}

type addUserOpts struct {
	DBPath         string
	Nickname       string
	AWGIface       string
	ExpectedExitIP string
	BackendURL     string
	Out            io.Writer
}

func runAddUser(o addUserOpts) error {
	if !nicknameRegexp.MatchString(o.Nickname) {
		return fmt.Errorf("nickname %q must match %s", o.Nickname, nicknameRegexp)
	}
	if o.AWGIface == "" {
		return fmt.Errorf("--awg-iface is required (no default — per-user)")
	}
	if o.ExpectedExitIP == "" {
		return fmt.Errorf("--expected-exit-ip is required (no default — per-user)")
	}
	d, err := db.Open(o.DBPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", o.DBPath, err)
	}
	defer d.Close()

	rawToken, err := generateToken()
	if err != nil {
		return err
	}
	id, err := d.Users().Insert(o.Nickname, rawToken, o.ExpectedExitIP, o.AWGIface)
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	fmt.Fprintf(o.Out, "User created: id=%d nickname=%s awg_iface=%s expected_exit_ip=%s\n",
		id, o.Nickname, o.AWGIface, o.ExpectedExitIP)
	fmt.Fprintf(o.Out, "Token (raw, save now — only shown once): %s\n\n", rawToken)
	fmt.Fprintf(o.Out, "Place this in /opt/etc/wg-monitor/config.yaml on the router (chmod 600):\n\n")
	fmt.Fprintf(o.Out, `backend:
  url: %s
  token: %s

agent:
  nickname: %s
  interval_sec: 60

checks:
  awg:
    interface: %s
    handshake_max_age_sec: 180
    expected_exit_ip: %s
    marker_url: https://www.youtube.com/-/manifest
  dns:
    test_domain: example.com
    fail_threshold: 2
    providers:
      - { name: cloudflare, host: 1.1.1.1 }
      - { name: google,     host: 8.8.8.8 }
      - { name: quad9,      host: 9.9.9.9 }
`, o.BackendURL, rawToken, o.Nickname, o.AWGIface, o.ExpectedExitIP)
	fmt.Fprintf(o.Out, "\nThe Telegram topic for this user will be created automatically on the first HARD alert.\n")
	return nil
}

func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
```

- [ ] **Step 4: Update Makefile**

Add to `Makefile` after `build-host`:

```make
build-cli:
	mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -o $(BIN_DIR)/wg-monitor-cli ./cmd/wg-monitor-cli
```

And add `build-cli` to `build-host` target dependencies — change:

```make
all: build-host
```

to:

```make
all: build-host build-cli
```

- [ ] **Step 5: Run all tests**

```
go test ./...
```
Expected: PASS.

- [ ] **Step 6: Build CLI host binary**

```
go build -o bin/wg-monitor-cli ./cmd/wg-monitor-cli
./bin/wg-monitor-cli version
```

Expected: `0.2.0-stage1-dev`.

- [ ] **Step 7: Commit**

```bash
git add cmd/wg-monitor-cli/ Makefile
git commit -m "feat(cli): wg-monitor-cli add-user — generates token, inserts user, prints config snippet"
```

---

### Task 17: Live verification — deploy + provoke fail + observe alert + tag

**Files:**
- Modify: `deploy/backend/wg-monitor-backend.service` (add `StateDirectory=wg-monitor`)
- Live edit: `/etc/wg-monitor/backend.yaml` on VPS Main
- Live edit: `/opt/etc/wg-monitor/config.yaml` on MyRouter

This task is **mostly operational** — it does not write code. Run each step on the indicated host. Use `mcp__exec_ssh__exec` for VPS Main, the Paramiko deploy script for MyRouter.

- [ ] **Step 1: Add `StateDirectory` to the systemd unit**

```diff
--- a/deploy/backend/wg-monitor-backend.service
+++ b/deploy/backend/wg-monitor-backend.service
@@
 ProtectSystem=strict
+StateDirectory=wg-monitor
+ReadWritePaths=/var/lib/wg-monitor
```

The `StateDirectory=wg-monitor` directive auto-creates `/var/lib/wg-monitor` owned by the service user and grants write access to it. `ReadWritePaths` is a safety belt for the strict ProtectSystem.

- [ ] **Step 2: Build host binaries (cross-compile is not needed for backend — VPS Main is amd64)**

```
go build -o bin/wg-monitor-backend ./cmd/backend
go build -o bin/wg-monitor-cli ./cmd/wg-monitor-cli
make pack    # cross-compile mipsel + arm64 agent + UPX
```

Expected: `bin/linux-arm64/wg-monitor` < 3 MiB (Stage 0 was 1.75 MiB; Stage 1 grows ~300 KB for SQLite-driver-free agent — agent does NOT link sqlite, only backend does, so it should stay ~2 MB).

- [ ] **Step 3: Deploy backend + CLI to VPS Main**

```bash
scp bin/wg-monitor-backend root@103.106.1.253:/usr/local/bin/wg-monitor-backend.new
scp bin/wg-monitor-cli root@103.106.1.253:/usr/local/bin/wg-monitor-cli.new
scp deploy/backend/wg-monitor-backend.service root@103.106.1.253:/etc/systemd/system/wg-monitor-backend.service
ssh root@103.106.1.253 'mv /usr/local/bin/wg-monitor-backend.new /usr/local/bin/wg-monitor-backend && \
  mv /usr/local/bin/wg-monitor-cli.new /usr/local/bin/wg-monitor-cli && \
  chmod 755 /usr/local/bin/wg-monitor-backend /usr/local/bin/wg-monitor-cli && \
  systemctl daemon-reload'
```

- [ ] **Step 4: Write the new backend config (replaces the old `agents:` block)**

```bash
ssh root@103.106.1.253 'cat > /etc/wg-monitor/backend.yaml <<EOF
listen: 127.0.0.1:8080
log_level: info
db_path: /var/lib/wg-monitor/state.db

telegram:
  bot_token_file: /root/wgmon-secrets/bot-token.txt
  chat_id: -1003651873378
  admin_user_id: 136513775

heartbeat:
  stale_after_sec: 300
  scan_interval_sec: 30

state:
  fail_threshold: 3
  recovery_threshold: 2
  realert_every_sec: 21600
EOF
chmod 640 /etc/wg-monitor/backend.yaml
chown root:wgmonitor /etc/wg-monitor/backend.yaml'
```

- [ ] **Step 5: Restart backend, observe DB initialised**

```bash
ssh root@103.106.1.253 'systemctl restart wg-monitor-backend && \
  sleep 2 && \
  systemctl is-active wg-monitor-backend && \
  ls -l /var/lib/wg-monitor/ && \
  journalctl -u wg-monitor-backend --since "30 sec ago" -n 30 --no-pager'
```

Expected: `active`, `state.db` exists, log mentions `backend listening addr=127.0.0.1:8080 version=0.2.0-stage1-dev`. **Note: at this point existing testkeen agent will start failing auth (token still valid in old yaml-map, but the new backend looks in DB)** — this is expected; we fix it in the next step.

- [ ] **Step 6: Re-onboard testkeen via the CLI**

```bash
ssh root@103.106.1.253 '/usr/local/bin/wg-monitor-cli add-user \
  --db /var/lib/wg-monitor/state.db \
  --nickname testkeen \
  --awg-iface awg0 \
  --expected-exit-ip 89.125.101.122 \
  --backend-url https://wgmonitor.jkaotlic.duckdns.org'
```

Capture the printed `Token (raw): ...` value. We will paste it into the router config in the next step. The CLI also prints the full `config.yaml` snippet — save it locally.

- [ ] **Step 7: Update local-values.yaml + push new agent + new config to MyRouter**

```bash
# 1. Update local-values.yaml with the new token (this file is gitignored)
# 2. Build the new config.yaml from the CLI snippet (replace the old token)
# 3. Push:
python deploy/agent/deploy_keenetic.py \
  --password 'Algal0n007' \
  --binary bin/linux-arm64/wg-monitor \
  --config /tmp/testkeen-config.yaml
```

(If the deploy script does not yet support `--binary`, add a quick `--binary` flag — the existing script already pushes the config; just add an `scp -O` style raw-bytes push for the binary using the same Paramiko pattern.)

Expected: agent restarts via init.d, sends a /v1/report within 60 s.

- [ ] **Step 8: Confirm reports flow + DB populated**

```bash
ssh root@103.106.1.253 'journalctl -u wg-monitor-backend --since "2 min ago" | grep -c report' # expect ≥ 1
ssh root@103.106.1.253 'sqlite3 /var/lib/wg-monitor/state.db \
  "SELECT u.nickname, e.check_name, e.status, e.ts FROM events e JOIN users u ON u.id = e.user_id ORDER BY e.id DESC LIMIT 10"'
```

Expected: 4 real checks (awg_handshake, awg_routing, awg_marker, dns_doh) + agent_heartbeat per minute.

- [ ] **Step 9: Provoke FAIL — drop AWG on MyRouter**

```bash
python deploy/agent/deploy_keenetic.py --password 'Algal0n007' --shell 'wg-quick down awg0; sleep 1; wg show'
```

Wait ~3 minutes. Backend should:
- Receive 3 consecutive `awg_handshake=fail` reports
- FSM emits `Hard` transition
- Dispatcher creates the `👤 testkeen` topic in `Status_Group` (first HARD ever)
- HARD message lands in the topic

- [ ] **Step 10: Confirm HARD alert in Telegram**

Open `Status_Group` → `👤 testkeen` topic. Expect:
```
🔴 [testkeen] awg_handshake — DOWN
Fails: 3 подряд
Hard since: 2026-04-26 21:XX:XX МСК
stale
```

If the topic does not appear, check `journalctl -u wg-monitor-backend -n 50 | grep -E 'dispatch|Hard|tg'`.

- [ ] **Step 11: Provoke RECOVERY**

```bash
python deploy/agent/deploy_keenetic.py --password 'Algal0n007' --shell 'wg-quick up awg0'
```

Wait another ~2 minutes. Expect a RECOVERY message **as a reply to the HARD message** in the same topic:
```
✅ [testkeen] awg_handshake — RECOVERED
Downtime: ~3m
```

- [ ] **Step 12: Verify state in DB**

```bash
ssh root@103.106.1.253 'sqlite3 /var/lib/wg-monitor/state.db \
  "SELECT check_name, current_status, consecutive_fails, consecutive_oks, hard_since, last_alert_msg_id FROM incident_state"'
```

Expected: `awg_handshake | ok | 0 | 2 | NULL | NULL` (or fresh OK counter ≥ 2).

- [ ] **Step 13: Tag and push**

```bash
git checkout feature/stage-1
git push -u origin feature/stage-1   # if a remote ever gets configured; otherwise skip
git tag -a v0.2.0-stage1 -m "Stage 1 — checks + FSM + TG alerts (HARD + RECOVERY) + add-user CLI"
```

- [ ] **Step 14: Update memory + active_tasks + session_context**

Mark `wg-monitor Stage 1` complete in `~/.claude/projects/C--Users-Anex/memory/active_tasks.md`. Update `project_wg_monitor.md` with: tag `v0.2.0-stage1`, what works live (4 checks, FSM, TG alerts, add-user CLI), what's deferred (callbacks → Stage 2, command channel → Stage 3, install.sh + self-update → Stage 5). Refresh `session_context.md` with the new "what was done / what's next" snapshot.

- [ ] **Step 15: Final commit (memory & docs only)**

```bash
git add docs/superpowers/plans/2026-04-26-wg-monitor-stage-1.md
git commit -m "docs: mark Stage 1 plan complete (checks + FSM + TG + CLI + live verify)"
```

---

## Self-Review

**1. Spec coverage.** Mapping each requirement in spec §14.2 ("Этап 1") to a task:
- "4 проверки в агенте" → Tasks 2, 3, 4, 5
- "SQLite + state machine + heartbeat watcher" → Tasks 8, 9, 10, 11, 12, 15 (watcher)
- "TG bot, топики, HARD/RECOVERY алерты (без кнопок)" → Tasks 13, 14, 15
- "install.sh + CLI add-user" — install.sh **deferred** to Stage 5 (called out in plan header). `add-user` → Task 16.
- "Verify: подключить себя, спровоцировать AWG fail, увидеть алерт + recovery" → Task 17 steps 9-12.
- "Re-alert на залипший hard каждые 6 часов" (spec §5.3) → Schema field `last_alert_at` + `db.State().StaleHards()` query is implemented (Task 11). The poller goroutine that consumes `StaleHards` and re-fires HARD is **not in Stage 1** — the dispatcher writes `LastAlertAt` so Stage-2 can add the poll loop without schema changes. Documented in plan header under "Out of scope".
- Spec says token is bcrypt-hashed; we deviate to SHA-256 + ConstantTimeCompare with rationale in Task 9 background. This is a documented departure, not a gap.

**2. Placeholder scan.** Searched for `TBD`, `TODO`, `implement later`, `add validation`, `error handling`, `similar to`. Two intentional `TODO` comments survived — `dns_doh` rename to `dns_dot` in Task 5 (deliberate, scheduled for Stage 2+) and the Stage-2 backlog of re-alert poller in Task 11. Both are pointers to future work, not gaps in this plan.

**3. Type consistency.**
- `Check.Run(ctx, Deps) wire.Check` — same signature in Tasks 1, 2, 3, 4, 5, 7. ✓
- `db.IncidentState` field names match across Tasks 11, 12, 14, 15 (`ConsecutiveFails`, `ConsecutiveOKs`, `CurrentStatus`, `HardSince`, `LastAlertMsgID`, `LastAlertAt`). ✓
- `state.Transition{Kind, Next}` and `state.Apply(prev, incoming, now, th) Transition` consistent in Tasks 12, 14, 15. ✓
- `tg.Client.SendMessage(ctx, chatID, threadID, text, parseMode, replyTo)` — 6 args, signature stable across Tasks 13, 14, 15. ✓
- `alerts.Dispatcher.Handle(ctx, userID, nickname, checkName, transition, detail)` — same in Tasks 14, 15. ✓
- `heartbeat.OfflineSender.SendOffline(ctx, uid, nick, since)` matches `alerts.Dispatcher.SendOffline` exactly so the dispatcher implements the interface — confirmed in Tasks 14, 15. ✓
- `db.UsersRepo.GetByToken(rawToken)` matches `backend.UserLookup.GetByToken(rawToken)` so `*db.UsersRepo` satisfies the interface in Task 15. ✓

**4. Risk surface.** Three hot spots that warrant a second look during execution:
- **`SO_BINDTODEVICE` requires root + Linux.** Agent already runs as root via Entware init.d (Stage 0 confirmed), so no surprise — but if a non-root agent ever ships, awg_routing + awg_marker silently fall back to default routing (the dial just won't bind). Mitigation: have Task 7's wire-up log a `slog.Warn` if `IfaceDialer` returns an error on first probe. **Defer to Stage 2** — not blocking Stage 1 verify.
- **First-time createForumTopic in production.** Bot must be admin in `Status_Group` with `can_manage_topics` (verified in Stage 0). If the API call fails, dispatcher returns the error and HARD alert is not sent — the report still succeeds (we only log-warn, see Task 15 step 6). Recovery: restart backend after fixing perms; FSM is in DB so `current_status='hard'` survives.
- **Heartbeat watcher false positive on a brand-new user.** Mitigated in Task 15 watcher.go: `if latest.IsZero() { continue }` — a user with zero events is never declared offline. Only after the first successful report does the staleness clock start.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-26-wg-monitor-stage-1.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration. Phases A and B are independent and can be parallelised by two subagents in worktrees if you want speed; Phases C and D depend on B.

**2. Inline Execution** — Execute tasks in this session using `executing-plans`, batch checkpoints after each phase.

**Which approach?**



