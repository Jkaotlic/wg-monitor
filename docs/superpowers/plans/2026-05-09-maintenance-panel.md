# Maintenance Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `🛠 Обслуживание` Telegram inline panel for restarting hrneo / awg-manager / the Keenetic router itself, showing & installing KeeneticOS firmware updates, and surfacing soft warnings about outdated software inside the existing smart-reply.

**Architecture:** Reuses the established pattern from Routes panel (v0.10.0): `🛠 Обслуживание` reply-button → loading panel message → enqueue agent cmd via cmd-queue → agent runs action → `MaintPanelNotifier` edits panel in place. Agent grows three new actions (`service_restart`, `firmware_status`, `firmware_install`, `version_audit`); backend grows an `upstream.Cache` (12h TTL GitHub releases fetcher), pending-tokens + cooldown maps mirroring `pendingRebinds`, and a smart-reply update section.

**Tech Stack:** Go 1.22+, `golang.org/x/mod/semver` for software version comparison (already transitively pulled), `net/http/httptest` for unit tests. No new third-party dependencies.

**Spec:** [docs/superpowers/specs/2026-05-09-maintenance-panel-design.md](../specs/2026-05-09-maintenance-panel-design.md)

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `docs/superpowers/notes/2026-05-09-maint-probes.md` | Create | Captured outputs of the 6 SSH probes done in M0 |
| `pkg/wire/maintenance.go` | Create | `VersionAudit`, `FirmwareStatus`, `MaintActionResult` types |
| `pkg/wire/maintenance_test.go` | Create | JSON round-trip |
| `pkg/wire/types.go` | Modify | Add `service_restart`, `firmware_status`, `firmware_install`, `version_audit` to `validCommandActions` |
| `internal/agent/awgmgr/system.go` | Modify | Add `RestartSelf(ctx)` method (impl chosen by M0 finding) |
| `internal/agent/awgmgr/system_test.go` | Modify | httptest fixture for `RestartSelf` |
| `internal/agent/actions/maintenance.go` | Create | `GetFirmwareStatus`, `InstallFirmware`, `VersionAudit`, `daemonUptime` helpers |
| `internal/agent/actions/maintenance_test.go` | Create | Parse golden `ndmc show version`; mock Exec; aggregator |
| `internal/agent/actions/runner.go` | Modify | Add 4 cases; new `Runner` fields `AllowRouterReboot`, `AllowFirmwareInstall` |
| `internal/agent/actions/runner_test.go` | Modify | All 4 cases; allow-flag enforcement |
| `internal/agent/config.go` | Modify | Add `Maintenance{AllowRouterReboot, AllowFirmwareInstall}` config struct |
| `internal/agent/config_test.go` | Modify | Round-trip test for new section |
| `cmd/agent/main.go` | Modify | Wire config flags into Runner |
| `internal/backend/upstream/versions.go` | Create | `Cache`, `Source`, `Entry`, `Latest`, `LatestAll` |
| `internal/backend/upstream/versions_test.go` | Create | Mock GitHub server, TTL, debounced error log, graceful empty |
| `internal/backend/upstream/compare.go` | Create | `firmware.NewerThan(installed, candidate string) bool`; `software.NewerThan` semver wrapper |
| `internal/backend/upstream/compare_test.go` | Create | Golden cases for both comparators |
| `internal/backend/callbacks/maint.go` | Create | `pendingMaint`, `cooldownEntry`, `MaintConfirmAction`, `makeMaintToken`, cooldown helpers |
| `internal/backend/callbacks/maint_test.go` | Create | Token TTL, cooldown apply/check, MaintConfirmAction enqueues correct cmd, replay rejection |
| `internal/backend/callbacks/parse.go` | Modify | Add 8 new actions; `MaintName`, `MaintToken` fields on `Args` |
| `internal/backend/callbacks/parse_test.go` | Modify | Round-trip parse all 8 actions; invalid args |
| `internal/backend/tg/maint_panel.go` | Create | Render Status, RestartConfirm, Firmware, FirmwareConfirm screens |
| `internal/backend/tg/maint_panel_test.go` | Create | Golden render: status (with/without updates / cooldown), restart-confirm, firmware (update / no-update) |
| `internal/backend/callbacks/router.go` | Modify | Reply-keyboard branch `🛠 Обслуживание`; case branches for new actions; `pendingMaint`/`cooldown` fields + helpers |
| `internal/backend/callbacks/maint_notifier.go` | Create | Handles `version_audit` / `service_restart` / `firmware_*` results — edits panel in place |
| `internal/backend/callbacks/maint_notifier_test.go` | Create | Edits with correct text; cache wires to upstream.Cache |
| `internal/backend/handler.go` | Modify | `MaintNotifier` interface in `Deps`; dispatch in `cmdResultHandler` for `maint_*` actions |
| `internal/backend/handler_test.go` | Modify | Asserts dispatch by `ref.Action` |
| `internal/backend/tg/reply_keyboard.go` | Modify | Add `🛠 Обслуживание` button row |
| `internal/backend/tg/reply_keyboard_test.go` | Modify | Golden keyboard layout |
| `internal/backend/alerts/smart_reply.go` | Modify | `SmartReplyArgs.Updates` field |
| `internal/backend/alerts/smart_reply_test.go` | Modify | Render with/without updates |
| `internal/backend/alerts/format.go` | Modify | `FormatSmartReply` appends Updates section if non-empty |
| `internal/backend/callbacks/router.go` (smart-reply) | Modify | `dispatchSmartReply` populates `Updates` from `upstream.Cache` + last events |
| `internal/backend/config.go` | Modify | `Upstream{AwgmgrRepo, HrneoRepo, CacheTTL}` config struct |
| `internal/backend/config_test.go` | Modify | Round-trip test for new section |
| `cmd/backend/main.go` | Modify | Wire `upstream.Cache`, `MaintPanelNotifier`, `MaintConfirmAction`, pendingMaint store |
| `README.md` | Modify | Feature line for Maintenance panel |

---

## Conventions

- **Tests first**, then minimal implementation. One green run before committing.
- **Commit boundaries** = task boundaries unless the task explicitly says "no commit".
- **Run `go test ./...`** after each implementation step.
- **No new third-party imports** — `golang.org/x/mod/semver` is the only one and is already transitively pulled (verify with `go list -m all | grep mod` before importing).
- **Russian text** in TG renderers — match `tunnels_panel.go` / `routes_panel.go` style. Plain text only (no MarkdownV2, per `feedback_telegram_api`).
- **JSON payloads** for cmd results — agent encodes as JSON string in `wire.CommandResult.Output`; backend decodes. Pattern matches `route_status`.

---

## Milestone 0: SSH Probe — Verify TBDs from Spec

### Task 0.1: Probe testkeen for the 6 unknowns

**Files:**
- Create: `docs/superpowers/notes/2026-05-09-maint-probes.md`

This is a research-only task. Goal: capture exact outputs of commands so subsequent milestones write code against real shape, not assumed shape. Use `mcp__ssh-keenetic__exec` (preferred) or manual SSH.

- [ ] **Step 1: Probe each command, paste output verbatim into a notes file**

Run on testkeen (192.168.31.1:222 root):

```sh
# Probe 1 — awg-manager self-restart endpoint discovery.
# Inspect frontend bundle for restart-related calls:
grep -ri 'restart' /opt/share/www/awg-manager/_app/immutable/chunks/ | head -20
# Try common candidates (expect 404 or 200):
curl -s -o /dev/null -w "%{http_code}\n" -X POST -H 'X-Requested-With: XMLHttpRequest' http://127.0.0.1/api/system/restart
curl -s -o /dev/null -w "%{http_code}\n" -X POST -H 'X-Requested-With: XMLHttpRequest' http://127.0.0.1/api/system/restart-self
ls /opt/etc/init.d/ | grep -i awg

# Probe 2 — upstream repos. Open a browser to:
#   https://github.com/search?q=awg-manager+keenetic+entware&type=repositories
#   https://github.com/search?q=HydraRoute-Neo&type=repositories
# Note repo full names (owner/name) and verify /releases page shows tags.
# If found: curl https://api.github.com/repos/<owner>/<repo>/releases?per_page=1

# Probe 3 — ndmc show version exact format:
ndmc -c "show version"

# Probe 4 — does `components commit` actually trigger install + reboot?
# DO NOT EXECUTE — read help text only:
ndmc -c "help components commit"
ndmc -c "help system reboot"

# Probe 5 — hrneo version source:
curl -s -H 'X-Requested-With: XMLHttpRequest' http://127.0.0.1/api/system/hydraroute-status | head -50
ls /opt/etc/HydraRoute/ 2>/dev/null | head
cat /opt/etc/HydraRoute/VERSION 2>/dev/null
ipkg-cl list-installed 2>/dev/null | grep -i hydra
opkg list-installed | grep -i hydra

# Probe 6 — daemon uptime:
pidof hrneo
pidof awg-manager 2>/dev/null || pidof awgmgr
# Pick the right name from above, then:
PID=<the-pid>
stat -c %Y /proc/$PID  # works on most Linuxes
ps -o pid,etime,comm | head
busybox ps --help 2>&1 | head  # confirm flags supported
```

- [ ] **Step 2: Write the probe notes**

Create `docs/superpowers/notes/2026-05-09-maint-probes.md` with sections for each of the 6 probes; paste raw outputs and a one-line "decision" line. Example:

```markdown
# Maintenance probes — testkeen — 2026-05-09

## Probe 1: awg-manager self-restart

Frontend bundle grep: <output>
POST /api/system/restart       → <code>
POST /api/system/restart-self  → <code>
init.d script: /opt/etc/init.d/S52awg-manager (exists? yes/no)

**Decision:** use [API endpoint X | init.d fallback].
```

- [ ] **Step 3: Commit the notes file**

```sh
git add docs/superpowers/notes/2026-05-09-maint-probes.md
git commit -m "docs: maint probes — testkeen findings (M0)"
```

---

## Milestone 1: Wire Types

### Task 1.1: Define maintenance wire types

**Files:**
- Create: `pkg/wire/maintenance.go`
- Create: `pkg/wire/maintenance_test.go`
- Modify: `pkg/wire/types.go` (add 4 actions to validCommandActions)

- [ ] **Step 1: Write the failing test**

```go
// pkg/wire/maintenance_test.go
package wire

import (
	"encoding/json"
	"testing"
)

func TestVersionAudit_RoundTrip(t *testing.T) {
	in := VersionAudit{
		AwgmgrVersion:   "2.8.2",
		HrneoVersion:    "2.4.0",
		FirmwareCurrent: "4.2.6",
		FirmwareAvail:   "5.0.1",
		HrneoUptime:     "3д 4ч",
		AwgmgrUptime:    "7д 12ч",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out VersionAudit
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip diverged:\n  in=%+v\n out=%+v", in, out)
	}
}

func TestFirmwareStatus_RoundTrip(t *testing.T) {
	in := FirmwareStatus{
		Current:   "4.2.6",
		Available: "5.0.1",
		Hint:      "system upgrade is available",
		Channel:   "release",
	}
	b, _ := json.Marshal(in)
	var out FirmwareStatus
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip diverged:\n  in=%+v\n out=%+v", in, out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./pkg/wire/ -run TestVersionAudit_RoundTrip -v
```
Expected: FAIL with `undefined: VersionAudit`.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/wire/maintenance.go
package wire

// VersionAudit is the agent's compact reply to a version_audit command.
// Backend uses it to render the Maintenance panel and to compute soft-warning
// updates for the smart-reply.
type VersionAudit struct {
	AwgmgrVersion   string `json:"awgmgr_version"`
	HrneoVersion    string `json:"hrneo_version,omitempty"`
	FirmwareCurrent string `json:"firmware_current"`
	FirmwareAvail   string `json:"firmware_avail,omitempty"`
	HrneoUptime     string `json:"hrneo_uptime,omitempty"`
	AwgmgrUptime    string `json:"awgmgr_uptime,omitempty"`
}

// FirmwareStatus is the agent's reply to a firmware_status command.
// Mirrors the relevant fields from `ndmc -c "show version"`.
type FirmwareStatus struct {
	Current   string `json:"current"`
	Available string `json:"available,omitempty"`
	Hint      string `json:"hint,omitempty"`
	Channel   string `json:"channel,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./pkg/wire/ -run 'TestVersionAudit_RoundTrip|TestFirmwareStatus_RoundTrip' -v
```
Expected: PASS.

- [ ] **Step 5: Add new action names to validCommandActions**

Find `validCommandActions` in `pkg/wire/types.go` and add:

```go
"service_restart":  true,
"firmware_status":  true,
"firmware_install": true,
"version_audit":    true,
```

Run: `go test ./pkg/wire/...`. Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add pkg/wire/maintenance.go pkg/wire/maintenance_test.go pkg/wire/types.go
git commit -m "feat(wire): VersionAudit, FirmwareStatus types + 4 maint actions"
```

---

## Milestone 2: Agent — awgmgr.RestartSelf

### Task 2.1: Implement RestartSelf using M0 finding

**Files:**
- Modify: `internal/agent/awgmgr/system.go`
- Modify: `internal/agent/awgmgr/system_test.go` (or `client_test.go` if that's the existing test home)

**Note:** the implementation choice (API endpoint vs init.d Exec) depends on M0 Probe 1. Both branches below — pick one and delete the other when implementing.

- [ ] **Step 1: Write the failing test (API endpoint branch)**

```go
// internal/agent/awgmgr/system_test.go (add)
func TestRestartSelf_API(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Method + " " + r.URL.Path
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing XHR header")
		}
		w.WriteHeader(204) // awg-manager replies before dying
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	if err := c.RestartSelf(context.Background()); err != nil {
		t.Fatalf("RestartSelf: %v", err)
	}
	want := "POST /api/system/restart-self" // ← REPLACE with the path M0 confirmed
	if seen != want {
		t.Errorf("called %q, want %q", seen, want)
	}
}
```

If M0 found there is **no** API endpoint, instead test the init.d fallback by depending on an injected Exec function — see "init.d fallback branch" comment below.

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/agent/awgmgr/ -run TestRestartSelf_API -v
```
Expected: FAIL with `c.RestartSelf undefined`.

- [ ] **Step 3: Write minimal implementation (API branch)**

```go
// internal/agent/awgmgr/system.go (append)

// RestartSelf restarts the awg-manager daemon. The HTTP call may not return
// (the daemon could exit before flushing the response); treat conn-reset as
// success.
func (c *Client) RestartSelf(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/system/restart-self", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Daemon may have died mid-response — treat connection reset as success.
		if errors.Is(err, io.EOF) || isConnReset(err) {
			return nil
		}
		return fmt.Errorf("awgmgr POST /api/system/restart-self: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr restart-self: HTTP %d", resp.StatusCode)
	}
	return nil
}

func isConnReset(err error) bool {
	return err != nil && strings.Contains(err.Error(), "connection reset")
}
```

**Init.d fallback branch (use instead of above if M0 says no API):**

```go
// In runner.go's service_restart case, call:
//   r.Exec(ctx, "/opt/etc/init.d/S52awg-manager", "restart")
// No client-side method — just drop the RestartSelf method and adjust runner test.
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/agent/awgmgr/ -run TestRestartSelf_API -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/agent/awgmgr/system.go internal/agent/awgmgr/system_test.go
git commit -m "feat(awgmgr): RestartSelf via /api/system/restart-self"
```

---

## Milestone 3: Agent — Maintenance Actions

### Task 3.1: Parse `ndmc show version` → FirmwareStatus

**Files:**
- Create: `internal/agent/actions/maintenance.go`
- Create: `internal/agent/actions/maintenance_test.go`

- [ ] **Step 1: Write the failing test**

Use the **exact** golden output captured in M0 Probe 3. Example assumes a typical shape:

```go
// internal/agent/actions/maintenance_test.go
package actions

import (
	"context"
	"testing"
)

const ndmcShowVersionGolden = `release: 5.0.1
sandbox: stable
title: Keenetic Sprinter (KN-3710)
description: KeeneticOS firmware
hint: Update available
hwid: KN-3710
manufacturer: Keenetic
arch: aarch64
ndm:
  exact: 4.2.6
  cdate: 22 Jan 2026
` // ← REPLACE with M0 Probe 3 verbatim

func TestParseShowVersion_HasUpdate(t *testing.T) {
	fs, err := parseShowVersion(ndmcShowVersionGolden)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.Current != "4.2.6" {
		t.Errorf("Current=%q, want %q", fs.Current, "4.2.6")
	}
	if fs.Available != "5.0.1" {
		t.Errorf("Available=%q, want %q", fs.Available, "5.0.1")
	}
	if fs.Channel != "stable" {
		t.Errorf("Channel=%q, want %q", fs.Channel, "stable")
	}
}

func TestParseShowVersion_NoUpdate(t *testing.T) {
	in := `release: 5.0.1
sandbox: stable
ndm:
  exact: 5.0.1
  cdate: 22 Jan 2026
`
	fs, _ := parseShowVersion(in)
	if fs.Available != "" {
		t.Errorf("expected Available empty when current==release, got %q", fs.Available)
	}
}

func TestGetFirmwareStatus_ExecError(t *testing.T) {
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, &execErr{msg: "boom"}
	}
	if _, err := GetFirmwareStatus(context.Background(), exec); err == nil {
		t.Fatal("expected error from exec failure")
	}
}

type execErr struct{ msg string }

func (e *execErr) Error() string { return e.msg }
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/agent/actions/ -run TestParseShowVersion -v
```
Expected: FAIL with `parseShowVersion undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/agent/actions/maintenance.go
package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// GetFirmwareStatus runs `ndmc -c "show version"` and parses the output
// into a wire.FirmwareStatus.
func GetFirmwareStatus(ctx context.Context, exec ExecFunc) (wire.FirmwareStatus, error) {
	out, err := exec(ctx, "ndmc", "-c", "show version")
	if err != nil {
		return wire.FirmwareStatus{}, fmt.Errorf("ndmc show version: %w", err)
	}
	return parseShowVersion(string(out))
}

// parseShowVersion is the format parser, separated for table-driven tests.
// Format observed on testkeen (M0 Probe 3): YAML-ish key/value pairs with
// a nested `ndm:` block. We do simple line-prefix matching (full YAML parse
// would be overkill for the 8-10 fields we care about).
func parseShowVersion(s string) (wire.FirmwareStatus, error) {
	var fs wire.FirmwareStatus
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "release:"):
			fs.Available = strings.TrimSpace(strings.TrimPrefix(line, "release:"))
		case strings.HasPrefix(line, "sandbox:"):
			fs.Channel = strings.TrimSpace(strings.TrimPrefix(line, "sandbox:"))
		case strings.HasPrefix(line, "hint:"):
			fs.Hint = strings.TrimSpace(strings.TrimPrefix(line, "hint:"))
		case strings.HasPrefix(line, "  exact:"):
			fs.Current = strings.TrimSpace(strings.TrimPrefix(line, "  exact:"))
		}
	}
	if fs.Current == "" {
		return fs, fmt.Errorf("could not extract current version (looked for `  exact:`)")
	}
	// If release == current, no update available.
	if fs.Available == fs.Current {
		fs.Available = ""
		fs.Hint = ""
	}
	return fs, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/agent/actions/ -run TestParseShowVersion -v
go test ./internal/agent/actions/ -run TestGetFirmwareStatus_ExecError -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/agent/actions/maintenance.go internal/agent/actions/maintenance_test.go
git commit -m "feat(agent): parse ndmc show version → FirmwareStatus"
```

### Task 3.2: InstallFirmware

**Files:**
- Modify: `internal/agent/actions/maintenance.go`
- Modify: `internal/agent/actions/maintenance_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/actions/maintenance_test.go (append)
func TestInstallFirmware_ExecCommand(t *testing.T) {
	var got [][]string
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		got = append(got, append([]string{name}, args...))
		return []byte("ok"), nil
	}
	if err := InstallFirmware(context.Background(), exec); err != nil {
		t.Fatalf("InstallFirmware: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(got))
	}
	want := []string{"ndmc", "-c", "components commit"} // ← M0 Probe 4 confirms this is the right command
	if !slicesEq(got[0], want) {
		t.Errorf("exec=%v, want %v", got[0], want)
	}
}

func slicesEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/agent/actions/ -run TestInstallFirmware -v
```
Expected: FAIL with `InstallFirmware undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/agent/actions/maintenance.go (append)

// InstallFirmware kicks the KeeneticOS firmware install. After this returns
// `ok`, the router will reboot in seconds — the agent will lose connection
// before any further work completes; the caller should not expect a follow-up.
func InstallFirmware(ctx context.Context, exec ExecFunc) error {
	if _, err := exec(ctx, "ndmc", "-c", "components commit"); err != nil {
		return fmt.Errorf("ndmc components commit: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/agent/actions/ -run TestInstallFirmware -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/agent/actions/maintenance.go internal/agent/actions/maintenance_test.go
git commit -m "feat(agent): InstallFirmware via ndmc components commit"
```

### Task 3.3: VersionAudit aggregator + daemon uptime

**Files:**
- Modify: `internal/agent/actions/maintenance.go`
- Modify: `internal/agent/actions/maintenance_test.go`

The hrneo version + uptime sources depend on M0 Probes 5 and 6. Code below assumes:
- hrneo version → `/api/system/hydraroute-status` returns `version` field (extend `awgmgr.HRStatus` if needed).
- daemon uptime → busybox `ps -o pid,etime,comm` parsing.

If M0 Probe 5 says hrneo version comes from a file, swap that line for `exec(ctx, "cat", "/opt/etc/HydraRoute/VERSION")`.

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/actions/maintenance_test.go (append)

type fakeAwg struct{
	sysInfo awgmgr.SystemInfo
	hrStat  awgmgr.HRStatus
}
func (f *fakeAwg) SystemInfo(ctx context.Context) (awgmgr.SystemInfo, error)        { return f.sysInfo, nil }
func (f *fakeAwg) HydraRouteStatus(ctx context.Context) (awgmgr.HRStatus, error)    { return f.hrStat, nil }

func TestVersionAudit_AllFields(t *testing.T) {
	awg := &fakeAwg{
		sysInfo: awgmgr.SystemInfo{Version: "2.8.2", FirmwareVersion: "4.2.6"},
		hrStat:  awgmgr.HRStatus{Installed: true, Running: true, Version: "2.4.0"},
	}
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ndmc" && len(args) == 2 && args[1] == "show version":
			return []byte(ndmcShowVersionGolden), nil
		case name == "pidof" && len(args) == 1 && args[0] == "hrneo":
			return []byte("12345\n"), nil
		case name == "pidof" && len(args) == 1 && args[0] == "awg-manager":
			return []byte("23456\n"), nil
		case name == "ps" && len(args) >= 1 && args[0] == "-o":
			return []byte("12345 03-04:00:00 hrneo\n23456 07-12:00:00 awg-manager\n"), nil
		}
		return nil, fmt.Errorf("unexpected exec: %s %v", name, args)
	}
	got, err := VersionAudit(context.Background(), awg, exec)
	if err != nil {
		t.Fatalf("VersionAudit: %v", err)
	}
	want := wire.VersionAudit{
		AwgmgrVersion:   "2.8.2",
		HrneoVersion:    "2.4.0",
		FirmwareCurrent: "4.2.6",
		FirmwareAvail:   "5.0.1",
		HrneoUptime:     "3д 4ч",
		AwgmgrUptime:    "7д 12ч",
	}
	if got != want {
		t.Errorf("VersionAudit:\n  got=%+v\n want=%+v", got, want)
	}
}

func TestParseEtime(t *testing.T) {
	cases := map[string]string{
		"03-04:00:00": "3д 4ч",
		"07-12:00:00": "7д 12ч",
		"05:30:00":    "5ч 30м",
		"01:23":       "1м 23с",
		"00:42":       "42с",
	}
	for in, want := range cases {
		if got := humanizeEtime(in); got != want {
			t.Errorf("humanizeEtime(%q)=%q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/agent/actions/ -run TestVersionAudit -v
```
Expected: FAIL — `VersionAudit undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/agent/actions/maintenance.go (append)

// AwgInfoClient is the subset of *awgmgr.Client VersionAudit needs.
type AwgInfoClient interface {
	SystemInfo(ctx context.Context) (awgmgr.SystemInfo, error)
	HydraRouteStatus(ctx context.Context) (awgmgr.HRStatus, error)
}

// VersionAudit returns versions + uptimes of awgmgr/hrneo + firmware status.
func VersionAudit(ctx context.Context, awg AwgInfoClient, exec ExecFunc) (wire.VersionAudit, error) {
	sys, err := awg.SystemInfo(ctx)
	if err != nil {
		return wire.VersionAudit{}, fmt.Errorf("awg.SystemInfo: %w", err)
	}
	out := wire.VersionAudit{
		AwgmgrVersion:   sys.Version,
		FirmwareCurrent: sys.FirmwareVersion,
	}
	if hr, err := awg.HydraRouteStatus(ctx); err == nil && hr.Installed {
		out.HrneoVersion = hr.Version
	}
	if fs, err := GetFirmwareStatus(ctx, exec); err == nil {
		out.FirmwareCurrent = fs.Current
		out.FirmwareAvail = fs.Available
	}
	out.HrneoUptime = daemonUptime(ctx, exec, "hrneo")
	out.AwgmgrUptime = daemonUptime(ctx, exec, "awg-manager")
	return out, nil
}

// daemonUptime returns a human-readable uptime for the named daemon, or
// empty string if the daemon is not running / parsing fails. Never returns
// an error — uptime is a "nice to have" cell in the panel.
func daemonUptime(ctx context.Context, exec ExecFunc, name string) string {
	pidB, err := exec(ctx, "pidof", name)
	if err != nil {
		return ""
	}
	pid := strings.TrimSpace(strings.SplitN(string(pidB), " ", 2)[0])
	if pid == "" {
		return ""
	}
	psB, err := exec(ctx, "ps", "-o", "pid,etime,comm")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(psB), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[0] == pid {
			return humanizeEtime(fields[1])
		}
	}
	return ""
}

// humanizeEtime turns busybox `etime` ("D-HH:MM:SS" / "HH:MM:SS" / "MM:SS")
// into a Russian short form: "3д 4ч" / "5ч 30м" / "1м 23с" / "42с".
func humanizeEtime(s string) string {
	var d, h, m, sec int
	if strings.Contains(s, "-") {
		parts := strings.SplitN(s, "-", 2)
		fmt.Sscanf(parts[0], "%d", &d)
		s = parts[1]
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 3:
		fmt.Sscanf(parts[0], "%d", &h)
		fmt.Sscanf(parts[1], "%d", &m)
		fmt.Sscanf(parts[2], "%d", &sec)
	case 2:
		fmt.Sscanf(parts[0], "%d", &m)
		fmt.Sscanf(parts[1], "%d", &sec)
	}
	switch {
	case d > 0:
		return fmt.Sprintf("%dд %dч", d, h)
	case h > 0:
		return fmt.Sprintf("%dч %dм", h, m)
	case m > 0:
		return fmt.Sprintf("%dм %dс", m, sec)
	default:
		return fmt.Sprintf("%dс", sec)
	}
}

// EncodeVersionAudit serialises for transport via wire.CommandResult.Output.
func EncodeVersionAudit(va wire.VersionAudit) (string, error) {
	b, err := json.Marshal(va)
	return string(b), err
}
```

**Note:** `awgmgr.HRStatus` may not have a `Version` field today. If M0 Probe 5 confirms version is in the API response, extend the struct in `awgmgr/types.go` first; otherwise read from a file. Adjust `HydraRouteStatus` accordingly.

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/agent/actions/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/agent/actions/maintenance.go internal/agent/actions/maintenance_test.go internal/agent/awgmgr/types.go
git commit -m "feat(agent): VersionAudit aggregator + daemon uptime parser"
```

---

## Milestone 4: Agent — Runner Cases + Config Flags

### Task 4.1: Extend Runner with maintenance flags + 4 new cases

**Files:**
- Modify: `internal/agent/actions/runner.go`
- Modify: `internal/agent/actions/runner_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/actions/runner_test.go (add)

func TestRunner_ServiceRestart_Hrneo(t *testing.T) {
	called := false
	awg := &fakeAwgWithControl{
		hydraControl: func(ctx context.Context, action string) error {
			called = true
			if action != "restart" { t.Errorf("action=%q, want restart", action) }
			return nil
		},
	}
	r := &Runner{AwgClient: nil, awgInjected: awg, AllowRouterReboot: true} // see refactor note
	res := r.Execute(context.Background(), wire.Command{
		ID: "1", Action: "service_restart",
		Args: map[string]any{"name": "hrneo"},
	})
	if res.Status != "ok" { t.Errorf("status=%q, want ok; output=%q", res.Status, res.Output) }
	if !called { t.Error("HydraRouteControl not called") }
}

func TestRunner_ServiceRestart_Router_Disabled(t *testing.T) {
	r := &Runner{AllowRouterReboot: false}
	res := r.Execute(context.Background(), wire.Command{
		ID: "1", Action: "service_restart",
		Args: map[string]any{"name": "router"},
	})
	if res.Status != "err" { t.Errorf("status=%q, want err", res.Status) }
	if !strings.Contains(res.Output, "disabled") { t.Errorf("output=%q, expected 'disabled'", res.Output) }
}

func TestRunner_FirmwareInstall_Disabled(t *testing.T) {
	r := &Runner{AllowFirmwareInstall: false}
	res := r.Execute(context.Background(), wire.Command{ID: "1", Action: "firmware_install"})
	if res.Status != "err" || !strings.Contains(res.Output, "disabled") {
		t.Errorf("expected disabled error; got status=%q output=%q", res.Status, res.Output)
	}
}

func TestRunner_VersionAudit_JSON(t *testing.T) {
	r := &Runner{
		AwgClient: nil,
		awgInjected: &fakeAwg{
			sysInfo: awgmgr.SystemInfo{Version: "2.8.2", FirmwareVersion: "4.2.6"},
			hrStat:  awgmgr.HRStatus{Installed: true, Version: "2.4.0"},
		},
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "ndmc" { return []byte(ndmcShowVersionGolden), nil }
			return []byte(""), nil
		},
	}
	res := r.Execute(context.Background(), wire.Command{ID: "1", Action: "version_audit"})
	if res.Status != "ok" { t.Fatalf("status=%q, output=%q", res.Status, res.Output) }
	var va wire.VersionAudit
	if err := json.Unmarshal([]byte(res.Output), &va); err != nil { t.Fatalf("decode: %v", err) }
	if va.AwgmgrVersion != "2.8.2" || va.HrneoVersion != "2.4.0" {
		t.Errorf("VA=%+v", va)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/agent/actions/ -run 'TestRunner_ServiceRestart|TestRunner_FirmwareInstall|TestRunner_VersionAudit' -v
```
Expected: FAIL with `awgInjected undefined` / case branches missing.

- [ ] **Step 3: Refactor Runner to accept an interface for awg + add fields**

Modify `internal/agent/actions/runner.go`:

```go
// Runner is built once at agent startup and re-used per-command.
type Runner struct {
	AwgClient            *awgmgr.Client
	ForceRecheck         func(ctx context.Context)
	Opkg                 OpkgExecutor
	Exec                 ExecFunc
	Now                  func() time.Time
	AllowRouterReboot    bool   // gate for `service_restart router`
	AllowFirmwareInstall bool   // gate for `firmware_install`
	routeMu              sync.Mutex

	// awgInjected is for tests that need to swap the awgmgr client with a
	// stub. When nil, falls back to AwgClient (the production wiring).
	awgInjected AwgInfoClient
}

func (r *Runner) awg() AwgInfoClient {
	if r.awgInjected != nil {
		return r.awgInjected
	}
	return r.AwgClient
}
```

Add new cases to `dispatch`:

```go
case "service_restart":
	name, _ := cmd.Args["name"].(string)
	switch name {
	case "hrneo":
		if r.AwgClient == nil { return "err", "awgmgr client not configured" }
		if err := r.AwgClient.HydraRouteControl(ctx, "restart"); err != nil { return "err", err.Error() }
		return "ok", "hrneo restart sent"
	case "awgmgr":
		if r.AwgClient == nil { return "err", "awgmgr client not configured" }
		if err := r.AwgClient.RestartSelf(ctx); err != nil { return "err", err.Error() }
		return "ok", "awg-manager restart sent"
	case "router":
		if !r.AllowRouterReboot { return "err", "router reboot disabled in agent config" }
		if r.Exec == nil { return "err", "exec not configured" }
		out, err := r.Exec(ctx, "ndmc", "-c", "system reboot")
		if err != nil { return "err", fmt.Sprintf("ndmc reboot: %v\n%s", err, string(out)) }
		return "ok", "reboot scheduled"
	default:
		return "err", "unknown service: " + name
	}
case "firmware_status":
	if r.Exec == nil { return "err", "exec not configured" }
	fs, err := GetFirmwareStatus(ctx, r.Exec)
	if err != nil { return "err", err.Error() }
	b, _ := json.Marshal(fs)
	return "ok", string(b)
case "firmware_install":
	if !r.AllowFirmwareInstall { return "err", "firmware install disabled in agent config" }
	if r.Exec == nil { return "err", "exec not configured" }
	if err := InstallFirmware(ctx, r.Exec); err != nil { return "err", err.Error() }
	return "ok", "firmware install kicked; router will reboot"
case "version_audit":
	if r.AwgClient == nil { return "err", "awgmgr client not configured" }
	if r.Exec == nil { return "err", "exec not configured" }
	va, err := VersionAudit(ctx, r.awg(), r.Exec)
	if err != nil { return "err", err.Error() }
	out, _ := EncodeVersionAudit(va)
	return "ok", out
```

Add stub `fakeAwgWithControl` to test file (or inline closure-based stub).

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/agent/actions/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/agent/actions/runner.go internal/agent/actions/runner_test.go
git commit -m "feat(agent): runner cases for service_restart, firmware_*, version_audit"
```

### Task 4.2: Agent config — Maintenance section

**Files:**
- Modify: `internal/agent/config.go`
- Modify: `internal/agent/config_test.go`
- Modify: `cmd/agent/main.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/config_test.go (add)
func TestConfig_MaintenanceDefaults(t *testing.T) {
	yaml := `
backend_url: "http://localhost"
agent_token: "x"
nickname: "n"
` // no maintenance: section
	cfg, err := LoadConfig(strings.NewReader(yaml))
	if err != nil { t.Fatal(err) }
	if cfg.Maintenance.AllowRouterReboot { t.Error("AllowRouterReboot should default false") }
	if cfg.Maintenance.AllowFirmwareInstall { t.Error("AllowFirmwareInstall should default false") }
}

func TestConfig_MaintenanceParsed(t *testing.T) {
	yaml := `
backend_url: "http://localhost"
agent_token: "x"
nickname: "n"
maintenance:
  allow_router_reboot: true
  allow_firmware_install: true
`
	cfg, _ := LoadConfig(strings.NewReader(yaml))
	if !cfg.Maintenance.AllowRouterReboot || !cfg.Maintenance.AllowFirmwareInstall {
		t.Errorf("flags not parsed: %+v", cfg.Maintenance)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/agent/ -run TestConfig_Maintenance -v
```
Expected: FAIL with `cfg.Maintenance undefined`.

- [ ] **Step 3: Add config struct**

```go
// internal/agent/config.go — extend Config struct

type Config struct {
	// ... existing fields
	Maintenance MaintenanceConfig `yaml:"maintenance"`
}

type MaintenanceConfig struct {
	AllowRouterReboot    bool `yaml:"allow_router_reboot"`
	AllowFirmwareInstall bool `yaml:"allow_firmware_install"`
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/agent/ -run TestConfig_Maintenance -v
```
Expected: PASS.

- [ ] **Step 5: Wire into Runner in cmd/agent/main.go**

Find where `Runner{}` is constructed; add:

```go
runner := &actions.Runner{
	// ... existing fields
	AllowRouterReboot:    cfg.Maintenance.AllowRouterReboot,
	AllowFirmwareInstall: cfg.Maintenance.AllowFirmwareInstall,
}
```

Verify build: `go build ./cmd/agent/...`. Expected: success.

- [ ] **Step 6: Commit**

```sh
git add internal/agent/config.go internal/agent/config_test.go cmd/agent/main.go
git commit -m "feat(agent): MaintenanceConfig flags wired into Runner"
```

---

## Milestone 5: Backend — Upstream Version Cache

### Task 5.1: Cache + GitHub fetcher

**Files:**
- Create: `internal/backend/upstream/versions.go`
- Create: `internal/backend/upstream/versions_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/upstream/versions_test.go
package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCache_FetchAndCache(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v2.9.0"}})
	}))
	defer srv.Close()
	c := NewCache(1*time.Hour, []Source{{Name: "awgmgr", GitHubRepo: "x/y"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=1"
	v, err := c.Latest(context.Background(), "awgmgr")
	if err != nil { t.Fatalf("Latest: %v", err) }
	if v != "2.9.0" { t.Errorf("got %q want 2.9.0", v) }
	v2, _ := c.Latest(context.Background(), "awgmgr")
	if v2 != "2.9.0" { t.Errorf("cached: got %q want 2.9.0", v2) }
	if hits != 1 { t.Errorf("expected 1 GitHub hit, got %d (cache miss?)", hits) }
}

func TestCache_TTLExpiry(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v1.0"}})
	}))
	defer srv.Close()
	c := NewCache(1*time.Millisecond, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=1"
	_, _ = c.Latest(context.Background(), "x")
	time.Sleep(5 * time.Millisecond)
	_, _ = c.Latest(context.Background(), "x")
	if hits != 2 { t.Errorf("expected 2 hits after TTL expiry, got %d", hits) }
}

func TestCache_GracefulOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := NewCache(1*time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=1"
	v, err := c.Latest(context.Background(), "x")
	if err == nil { t.Error("expected error for 404") }
	if v != "" { t.Errorf("expected empty version on error, got %q", v) }
}

func TestCache_UnknownSource(t *testing.T) {
	c := NewCache(1*time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	if _, err := c.Latest(context.Background(), "y"); err == nil {
		t.Error("expected error for unknown source")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/backend/upstream/ -v
```
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backend/upstream/versions.go
// Package upstream fetches latest released versions of external software
// (awg-manager, HydraRoute-Neo) from GitHub, caching responses to avoid
// burning anonymous rate-limit. Used by the backend to compute soft warnings
// in smart-reply and the maint panel.
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Source struct {
	Name       string // logical key e.g. "awgmgr", "hrneo"
	GitHubRepo string // "owner/name"
}

type Entry struct {
	Latest    string
	FetchedAt time.Time
	Err       error
}

type Cache struct {
	TTL     time.Duration
	sources map[string]Source
	http    *http.Client
	api     string // template; "%s" becomes the repo. Test-overridable.
	mu      sync.RWMutex
	data    map[string]Entry
}

const defaultAPI = "https://api.github.com/repos/%s/releases?per_page=1"

func NewCache(ttl time.Duration, sources []Source) *Cache {
	m := make(map[string]Source, len(sources))
	for _, s := range sources {
		m[s.Name] = s
	}
	return &Cache{
		TTL:     ttl,
		sources: m,
		http:    &http.Client{Timeout: 10 * time.Second},
		api:     defaultAPI,
		data:    make(map[string]Entry),
	}
}

// Latest returns the latest tag (with leading "v" stripped) for the named
// source. Cached for TTL. Returns error for unknown name or fetch failure;
// callers should treat error as "unknown" and skip the warning.
func (c *Cache) Latest(ctx context.Context, name string) (string, error) {
	src, ok := c.sources[name]
	if !ok {
		return "", fmt.Errorf("unknown upstream source: %q", name)
	}
	c.mu.RLock()
	if e, ok := c.data[name]; ok && time.Since(e.FetchedAt) < c.TTL {
		c.mu.RUnlock()
		return e.Latest, e.Err
	}
	c.mu.RUnlock()

	v, err := c.fetch(ctx, src.GitHubRepo)
	c.mu.Lock()
	c.data[name] = Entry{Latest: v, Err: err, FetchedAt: time.Now()}
	c.mu.Unlock()
	return v, err
}

// LatestAll returns a snapshot of all sources (cache-only — does not refresh).
// Callers wanting fresh data must call Latest per source.
func (c *Cache) LatestAll() map[string]Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]Entry, len(c.data))
	for k, v := range c.data {
		out[k] = v
	}
	return out
}

func (c *Cache) fetch(ctx context.Context, repo string) (string, error) {
	url := fmt.Sprintf(c.api, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil { return "", err }
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil { return "", err }
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("github %s: HTTP %d", repo, resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var arr []struct{ TagName string `json:"tag_name"` }
	if err := json.Unmarshal(body, &arr); err != nil {
		return "", fmt.Errorf("decode releases for %s: %w", repo, err)
	}
	if len(arr) == 0 {
		return "", fmt.Errorf("no releases for %s", repo)
	}
	return strings.TrimPrefix(arr[0].TagName, "v"), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/backend/upstream/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/backend/upstream/versions.go internal/backend/upstream/versions_test.go
git commit -m "feat(backend): upstream.Cache — GitHub releases fetcher with TTL"
```

### Task 5.2: Version comparison helpers

**Files:**
- Create: `internal/backend/upstream/compare.go`
- Create: `internal/backend/upstream/compare_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/upstream/compare_test.go
package upstream

import "testing"

func TestSoftwareNewerThan(t *testing.T) {
	cases := []struct{ installed, candidate string; want bool }{
		{"2.8.2", "2.9.0", true},
		{"2.9.0", "2.8.2", false},
		{"2.9.0", "2.9.0", false},
		{"v2.8.2", "2.9.0", true}, // strips leading v
		{"", "2.9.0", false},      // missing installed -> no warning
		{"2.8.2", "", false},      // missing candidate -> no warning
		{"junk", "2.9.0", false},  // parse error -> false (no false warnings)
	}
	for _, c := range cases {
		if got := SoftwareNewerThan(c.installed, c.candidate); got != c.want {
			t.Errorf("SoftwareNewerThan(%q,%q)=%v want %v", c.installed, c.candidate, got, c.want)
		}
	}
}

func TestFirmwareNewerThan(t *testing.T) {
	cases := []struct{ installed, candidate string; want bool }{
		{"4.2.6", "5.0.1", true},
		{"5.0.1", "4.2.6", false},
		{"4.2.6", "4.2.6", false},
		{"4.2.A6", "4.2.B1", true},  // alpha-suffix lex
		{"", "5.0.1", false},
		{"4.2.6", "", false},
	}
	for _, c := range cases {
		if got := FirmwareNewerThan(c.installed, c.candidate); got != c.want {
			t.Errorf("FirmwareNewerThan(%q,%q)=%v want %v", c.installed, c.candidate, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/backend/upstream/ -run NewerThan -v
```
Expected: FAIL — undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backend/upstream/compare.go
package upstream

import (
	"strings"

	"golang.org/x/mod/semver"
)

// SoftwareNewerThan returns true if candidate > installed in semver order.
// Returns false if either input is empty or unparseable — false-positive
// warnings are worse than missed warnings.
func SoftwareNewerThan(installed, candidate string) bool {
	if installed == "" || candidate == "" { return false }
	i := normalize(installed)
	c := normalize(candidate)
	if !semver.IsValid(i) || !semver.IsValid(c) { return false }
	return semver.Compare(c, i) > 0
}

func normalize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	return "v" + s
}

// FirmwareNewerThan compares Keenetic firmware versions. KeeneticOS uses a
// dotted format that is not strict semver (occasional alpha suffix). Strategy:
// split on '.', compare segment-by-segment numerically when both parse as
// int, lexically otherwise. Returns false on empty input.
func FirmwareNewerThan(installed, candidate string) bool {
	if installed == "" || candidate == "" { return false }
	a := strings.Split(installed, ".")
	b := strings.Split(candidate, ".")
	n := len(a)
	if len(b) < n { n = len(b) }
	for i := 0; i < n; i++ {
		if cmp := compareSeg(a[i], b[i]); cmp != 0 {
			return cmp < 0
		}
	}
	return len(b) > len(a)
}

func compareSeg(a, b string) int {
	ai, aerr := parseInt(a)
	bi, berr := parseInt(b)
	if aerr == nil && berr == nil {
		switch {
		case ai < bi: return -1
		case ai > bi: return 1
		default: return 0
		}
	}
	return strings.Compare(a, b)
}

func parseInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' { return 0, errParseInt }
		n = n*10 + int(r-'0')
	}
	if s == "" { return 0, errParseInt }
	return n, nil
}

var errParseInt = strings.NewReader("").Read // placeholder to avoid extra import
```

(The `errParseInt` trick is a hack — replace with `errors.New("not int")` and add `"errors"` to imports; the placeholder above is just to keep the snippet self-contained as written.)

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/backend/upstream/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/backend/upstream/compare.go internal/backend/upstream/compare_test.go
git commit -m "feat(backend): SoftwareNewerThan + FirmwareNewerThan comparators"
```

---

## Milestone 6: Backend — Pending Tokens, Cooldown, Confirm Action

### Task 6.1: pendingMaint store + cooldown helpers

**Files:**
- Create: `internal/backend/callbacks/maint.go`
- Create: `internal/backend/callbacks/maint_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/callbacks/maint_test.go
package callbacks

import (
	"testing"
	"time"
)

func TestPendingMaint_TTL(t *testing.T) {
	store := newPendingMaintStore()
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "hrneo", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	got, ok := store.consume(1, tok)
	if !ok || got.Name != "hrneo" { t.Errorf("consume failed: ok=%v got=%+v", ok, got) }
	// Replay rejected.
	if _, ok := store.consume(1, tok); ok { t.Error("replay should fail") }
}

func TestPendingMaint_ExpiredRejected(t *testing.T) {
	store := newPendingMaintStore()
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "router", Token: tok, ExpiresAt: time.Now().Add(-1 * time.Second)})
	if _, ok := store.consume(1, tok); ok { t.Error("expired token should fail") }
}

func TestCooldown_BlocksWithinWindow(t *testing.T) {
	store := newCooldownStore()
	store.set(1, "router_reboot", 5*time.Minute)
	if rem := store.remaining(1, "router_reboot"); rem <= 0 {
		t.Errorf("cooldown should be active, remaining=%v", rem)
	}
	if rem := store.remaining(2, "router_reboot"); rem != 0 {
		t.Errorf("other user should not be in cooldown, got %v", rem)
	}
	if rem := store.remaining(1, "firmware_install"); rem != 0 {
		t.Errorf("other action should not be in cooldown, got %v", rem)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/backend/callbacks/ -run 'TestPendingMaint|TestCooldown' -v
```
Expected: FAIL — undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backend/callbacks/maint.go
package callbacks

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type pendingMaint struct {
	UserID    int64
	Name      string // "hrneo" | "awgmgr" | "router" | "firmware"
	Token     string
	ExpiresAt time.Time
}

type pendingMaintStore struct {
	mu sync.Mutex
	m  map[string]*pendingMaint // keyed by token
}

func newPendingMaintStore() *pendingMaintStore {
	return &pendingMaintStore{m: make(map[string]*pendingMaint)}
}

func (s *pendingMaintStore) put(p *pendingMaint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.Token] = p
}

// consume atomically removes and returns the pending entry if it matches
// userID and is unexpired. Returns ok=false otherwise.
func (s *pendingMaintStore) consume(userID int64, token string) (*pendingMaint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[token]
	if !ok { return nil, false }
	delete(s.m, token)
	if p.UserID != userID || time.Now().After(p.ExpiresAt) { return nil, false }
	return p, true
}

func makeMaintToken() string {
	var b [4]byte
	_, _ = cryptoRand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// cooldownStore tracks per-user, per-action cooldown windows for destructive
// ops (router reboot, firmware install). All in-memory; lost on backend
// restart — acceptable since cooldown is at most 5 min.
type cooldownStore struct {
	mu sync.Mutex
	m  map[cooldownKey]time.Time // value = expires-at
}

type cooldownKey struct {
	UserID int64
	Action string // "router_reboot" | "firmware_install"
}

func newCooldownStore() *cooldownStore {
	return &cooldownStore{m: make(map[cooldownKey]time.Time)}
}

func (s *cooldownStore) set(userID int64, action string, dur time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[cooldownKey{userID, action}] = time.Now().Add(dur)
}

// remaining returns the time left in the cooldown window, or 0 if none.
func (s *cooldownStore) remaining(userID int64, action string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.m[cooldownKey{userID, action}]
	if !ok { return 0 }
	rem := time.Until(until)
	if rem <= 0 {
		delete(s.m, cooldownKey{userID, action})
		return 0
	}
	return rem
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/backend/callbacks/ -run 'TestPendingMaint|TestCooldown' -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/backend/callbacks/maint.go internal/backend/callbacks/maint_test.go
git commit -m "feat(callbacks): pendingMaint + cooldown stores"
```

### Task 6.2: MaintConfirmAction — token consume + enqueue + cooldown apply

**Files:**
- Modify: `internal/backend/callbacks/maint.go`
- Modify: `internal/backend/callbacks/maint_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/callbacks/maint_test.go (append)

type fakeSink struct{ enq []wire.Command }

func (f *fakeSink) Enqueue(uid int64, cmd wire.Command) error                     { f.enq = append(f.enq, cmd); return nil }
func (f *fakeSink) EnqueueWithRef(uid int64, cmd wire.Command, _ cmdpkg.MessageRef) error { f.enq = append(f.enq, cmd); return nil }

func TestMaintConfirmAction_HrneoEnqueues(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "hrneo", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-1" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}, Message: tg.Message{Chat: tg.Chat{ID: 0}}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "hrneo", MaintToken: tok}
	status, err := a.Apply(context.Background(), q, args)
	if err != nil { t.Fatalf("Apply: %v", err) }
	if !strings.Contains(status, "hrneo") { t.Errorf("status=%q", status) }
	if len(sink.enq) != 1 { t.Fatalf("expected 1 enq, got %d", len(sink.enq)) }
	if sink.enq[0].Action != "service_restart" { t.Errorf("action=%q", sink.enq[0].Action) }
	if sink.enq[0].Args["name"] != "hrneo" { t.Errorf("args=%v", sink.enq[0].Args) }
	// Cooldown for hrneo: none.
	if cd.remaining(1, "router_reboot") != 0 { t.Error("hrneo should not set cooldown") }
}

func TestMaintConfirmAction_RouterAppliesCooldown(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "router", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-1" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "router", MaintToken: tok}
	if _, err := a.Apply(context.Background(), q, args); err != nil { t.Fatal(err) }
	if cd.remaining(1, "router_reboot") <= 0 { t.Error("router reboot should set cooldown") }
}

func TestMaintConfirmAction_FirmwareAppliesCooldown(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "firmware", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-1" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "firmware", MaintToken: tok}
	if _, err := a.Apply(context.Background(), q, args); err != nil { t.Fatal(err) }
	if sink.enq[0].Action != "firmware_install" { t.Errorf("expected firmware_install, got %q", sink.enq[0].Action) }
	if cd.remaining(1, "firmware_install") <= 0 { t.Error("firmware install should set cooldown") }
}

func TestMaintConfirmAction_BadToken(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-1" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "hrneo", MaintToken: "deadbeef"}
	status, err := a.Apply(context.Background(), q, args)
	if err == nil { t.Errorf("expected error for unknown token; status=%q", status) }
	if len(sink.enq) != 0 { t.Errorf("nothing should be enqueued") }
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/backend/callbacks/ -run TestMaintConfirmAction -v
```
Expected: FAIL — undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backend/callbacks/maint.go (append)

import (
	"context"
	"fmt"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// MaintConfirmAction consumes a pendingMaint token, enqueues the right
// agent command, and applies a cooldown for destructive actions.
type MaintConfirmAction struct {
	sink    CommandEnqueuer
	store   *pendingMaintStore
	cd      *cooldownStore
	idGen   func() string
}

func NewMaintConfirmAction(sink CommandEnqueuer, store *pendingMaintStore, cd *cooldownStore, idGen func() string) *MaintConfirmAction {
	return &MaintConfirmAction{sink: sink, store: store, cd: cd, idGen: idGen}
}

func (a *MaintConfirmAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	pm, ok := a.store.consume(args.UserID, args.MaintToken)
	if !ok {
		return "", fmt.Errorf("token expired or unknown")
	}
	cmd := wire.Command{ID: a.idGen(), IssuedAt: time.Now().UTC()}
	cooldownAction := ""
	switch pm.Name {
	case "hrneo", "awgmgr":
		cmd.Action = "service_restart"
		cmd.Args = map[string]any{"name": pm.Name}
	case "router":
		cmd.Action = "service_restart"
		cmd.Args = map[string]any{"name": "router"}
		cooldownAction = "router_reboot"
	case "firmware":
		cmd.Action = "firmware_install"
		cooldownAction = "firmware_install"
	default:
		return "", fmt.Errorf("unknown maint name: %q", pm.Name)
	}
	if err := a.sink.Enqueue(args.UserID, cmd); err != nil {
		return "", fmt.Errorf("enqueue failed: %w", err)
	}
	if cooldownAction != "" {
		a.cd.set(args.UserID, cooldownAction, 5*time.Minute)
	}
	return fmt.Sprintf("✅ запрос отправлен: %s", pm.Name), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/backend/callbacks/ -run TestMaintConfirmAction -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/backend/callbacks/maint.go internal/backend/callbacks/maint_test.go
git commit -m "feat(callbacks): MaintConfirmAction with token consume + cooldown"
```

---

## Milestone 7: Backend — Callback Grammar Parse

### Task 7.1: Parse 8 new actions

**Files:**
- Modify: `internal/backend/callbacks/parse.go`
- Modify: `internal/backend/callbacks/parse_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/callbacks/parse_test.go (append)

func TestParse_MaintActions(t *testing.T) {
	cases := []struct{
		data    string
		want    Args
		wantErr bool
	}{
		{data: "maint_open:42:_panel_", want: Args{Action: "maint_open", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_close:42:_panel_", want: Args{Action: "maint_close", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_restart:42:hrneo", want: Args{Action: "maint_restart", UserID: 42, CheckName: "hrneo", MaintName: "hrneo"}},
		{data: "maint_restart:42:awgmgr", want: Args{Action: "maint_restart", UserID: 42, CheckName: "awgmgr", MaintName: "awgmgr"}},
		{data: "maint_restart:42:router", want: Args{Action: "maint_restart", UserID: 42, CheckName: "router", MaintName: "router"}},
		{data: "maint_confirm:42:hrneo:a1b2c3d4", want: Args{Action: "maint_confirm", UserID: 42, CheckName: "hrneo", MaintName: "hrneo", MaintToken: "a1b2c3d4"}},
		{data: "maint_fw_open:42:_panel_", want: Args{Action: "maint_fw_open", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_fw_check:42:_panel_", want: Args{Action: "maint_fw_check", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_fw_install:42:_panel_", want: Args{Action: "maint_fw_install", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_fw_confirm:42:_panel_:deadbeef", want: Args{Action: "maint_fw_confirm", UserID: 42, CheckName: "_panel_", IsPanel: true, MaintName: "firmware", MaintToken: "deadbeef"}},
		{data: "maint_restart:42", wantErr: true},                  // missing name
		{data: "maint_confirm:42:hrneo", wantErr: true},            // missing token
		{data: "maint_fw_confirm:42:_panel_", wantErr: true},       // missing token
	}
	for _, c := range cases {
		got, err := Parse(c.data)
		if c.wantErr {
			if err == nil { t.Errorf("Parse(%q): expected error", c.data) }
			continue
		}
		if err != nil { t.Errorf("Parse(%q): %v", c.data, err); continue }
		if got != c.want { t.Errorf("Parse(%q):\n  got=%+v\n want=%+v", c.data, got, c.want) }
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/backend/callbacks/ -run TestParse_MaintActions -v
```
Expected: FAIL — actions not in whitelist; fields missing.

- [ ] **Step 3: Extend parser**

In `internal/backend/callbacks/parse.go`:

1. Add fields to `Args`:

```go
type Args struct {
	// ... existing fields
	MaintName  string // "hrneo" | "awgmgr" | "router" | "firmware"
	MaintToken string
}
```

2. Add 8 new entries to `validActions`:

```go
"maint_open": true, "maint_close": true,
"maint_restart": true, "maint_confirm": true,
"maint_fw_open": true, "maint_fw_check": true,
"maint_fw_install": true, "maint_fw_confirm": true,
```

3. Add per-action arg-parsing in the switch at end of `Parse`:

```go
case "maint_restart":
	if len(parts) < 3 || parts[2] == "" || parts[2] == panelSentinel {
		return Args{}, fmt.Errorf("maint_restart requires name: %q", data)
	}
	a.MaintName = parts[2]
case "maint_confirm":
	if len(parts) < 4 || parts[3] == "" {
		return Args{}, fmt.Errorf("maint_confirm requires token: %q", data)
	}
	a.MaintName = parts[2]
	a.MaintToken = parts[3]
case "maint_fw_confirm":
	if len(parts) < 4 || parts[3] == "" {
		return Args{}, fmt.Errorf("maint_fw_confirm requires token: %q", data)
	}
	a.MaintName = "firmware"
	a.MaintToken = parts[3]
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/backend/callbacks/ -run TestParse_MaintActions -v
go test ./internal/backend/callbacks/ -v   # ensure no regressions
```
Expected: PASS for both.

- [ ] **Step 5: Commit**

```sh
git add internal/backend/callbacks/parse.go internal/backend/callbacks/parse_test.go
git commit -m "feat(callbacks): parse maint_* callback grammar (8 actions)"
```

---

## Milestone 8: Backend — MaintPanel Renderer

### Task 8.1: Status screen render

**Files:**
- Create: `internal/backend/tg/maint_panel.go`
- Create: `internal/backend/tg/maint_panel_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/tg/maint_panel_test.go
package tg

import (
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestMaintPanelText_WithUpdatesAndCooldown(t *testing.T) {
	args := MaintPanelArgs{
		Nickname:    "testkeen",
		HrneoUptime: "3д 4ч",  HrneoVersion: "2.4.0",  HrneoRunning: true,
		AwgmgrUptime:"7д 12ч", AwgmgrVersion:"2.8.2",
		Firmware:    wire.FirmwareStatus{Current: "4.2.6", Available: "5.0.1"},
		KeeneticOS:  "KN-1811",
		Updates: []UpdateLine{
			{Name: "KeeneticOS", Installed: "4.2.6", Available: "5.0.1"},
			{Name: "awg-manager", Installed: "2.8.2", Available: "2.9.0"},
		},
		RouterCooldownRemaining:   2*time.Minute + 23*time.Second,
		FirmwareCooldownRemaining: 0,
	}
	text := MaintPanelText(args)
	for _, want := range []string{
		"🛠 Обслуживание — testkeen",
		"HydraRoute-Neo  ✅ running, v2.4.0  uptime 3д 4ч",
		"awg-manager     ✅ running, v2.8.2  uptime 7д 12ч",
		"Keenetic OS     KN-1811, v4.2.6",
		"🟡 Доступны обновления:",
		"KeeneticOS 4.2.6 → 5.0.1",
		"awg-manager 2.8.2 → 2.9.0",
	} {
		if !strings.Contains(text, want) { t.Errorf("missing %q in:\n%s", want, text) }
	}
	kb := MaintPanelKeyboard(42, args)
	// Find a row with reboot button → should show cooldown variant
	found := false
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, "Cooldown") { found = true }
		}
	}
	if !found { t.Error("cooldown button label not rendered") }
}

func TestRestartConfirmText(t *testing.T) {
	text := RestartConfirmText("hrneo", "a1b2c3d4")
	for _, want := range []string{"⚠️", "HydraRoute-Neo", "Token: a1b2c3d4", "TTL 5"} {
		if !strings.Contains(text, want) { t.Errorf("missing %q in:\n%s", want, text) }
	}
}

func TestFirmwareScreenText_NoUpdate(t *testing.T) {
	text := FirmwareScreenText("testkeen", wire.FirmwareStatus{Current: "5.0.1", Channel: "release"})
	for _, want := range []string{"📦 Прошивка", "Текущая:    KeeneticOS 5.0.1", "актуальная"} {
		if !strings.Contains(text, want) { t.Errorf("missing %q in:\n%s", want, text) }
	}
}

func TestFirmwareScreenText_WithUpdate(t *testing.T) {
	text := FirmwareScreenText("testkeen", wire.FirmwareStatus{Current: "4.2.6", Available: "5.0.1", Channel: "release"})
	for _, want := range []string{"Доступная:  KeeneticOS 5.0.1", "⬆ обновление"} {
		if !strings.Contains(text, want) { t.Errorf("missing %q in:\n%s", want, text) }
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/backend/tg/ -run 'TestMaintPanelText|TestRestartConfirmText|TestFirmwareScreenText' -v
```
Expected: FAIL — undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/backend/tg/maint_panel.go
package tg

import (
	"fmt"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type UpdateLine struct {
	Name      string
	Installed string
	Available string
}

type MaintPanelArgs struct {
	Nickname                  string
	HrneoVersion              string
	HrneoUptime               string
	HrneoRunning              bool
	AwgmgrVersion             string
	AwgmgrUptime              string
	KeeneticOS                string // model — e.g. "KN-1811"
	Firmware                  wire.FirmwareStatus
	Updates                   []UpdateLine
	RouterCooldownRemaining   time.Duration
	FirmwareCooldownRemaining time.Duration
}

func MaintPanelText(a MaintPanelArgs) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🛠 Обслуживание — %s\n\n", a.Nickname)
	b.WriteString("Сервисы:\n")
	hrIcon := "❌"
	if a.HrneoRunning { hrIcon = "✅" }
	if a.HrneoVersion != "" {
		fmt.Fprintf(&b, "  • HydraRoute-Neo  %s running, v%s  uptime %s\n", hrIcon, a.HrneoVersion, a.HrneoUptime)
	} else {
		b.WriteString("  • HydraRoute-Neo  ⚪ не установлен\n")
	}
	fmt.Fprintf(&b, "  • awg-manager     ✅ running, v%s  uptime %s\n", a.AwgmgrVersion, a.AwgmgrUptime)
	fmt.Fprintf(&b, "  • Keenetic OS     %s, v%s\n", a.KeeneticOS, a.Firmware.Current)
	if len(a.Updates) > 0 {
		b.WriteString("\n🟡 Доступны обновления:\n")
		for _, u := range a.Updates {
			fmt.Fprintf(&b, "  • %s %s → %s\n", u.Name, u.Installed, u.Available)
		}
	}
	return b.String()
}

func MaintPanelKeyboard(userID int64, a MaintPanelArgs) InlineKeyboardMarkup {
	cd := func(action, arg string) string { return fmt.Sprintf("%s:%d:%s", action, userID, arg) }
	rebootLabel := "🔁 Reboot router"
	rebootCD := cd("maint_restart", "router")
	if a.RouterCooldownRemaining > 0 {
		rebootLabel = fmt.Sprintf("🕒 Cooldown %s", fmtCooldown(a.RouterCooldownRemaining))
		rebootCD = cd("maint_open", "_panel_") // tap reloads panel; could be a no-op
	}
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{
			{Text: "🔁 Restart hrneo", CallbackData: cd("maint_restart", "hrneo")},
			{Text: "🔁 Restart awg-mgr", CallbackData: cd("maint_restart", "awgmgr")},
		},
		{
			{Text: rebootLabel, CallbackData: rebootCD},
			{Text: "📦 Прошивка", CallbackData: cd("maint_fw_open", "_panel_")},
		},
		{
			{Text: "🔄 Проверить апдейты", CallbackData: cd("maint_open", "_panel_")},
			{Text: "✖ Закрыть", CallbackData: cd("maint_close", "_panel_")},
		},
	}}
}

func RestartConfirmText(name, token string) string {
	display := nameToDisplay(name)
	var what string
	switch name {
	case "hrneo":
		what = "  • DNS-routes на короткое время (~5 сек) перестанут резолвиться по правилам.\n  • Static-routes продолжат работать.\n  • Кратковременная просадка на доменах из ip.list."
	case "awgmgr":
		what = "  • Веб-интерфейс awg-manager на ~3-5 сек перестанет отвечать.\n  • Туннели не разрываются — это перезапуск только демона awg-manager.\n  • API-вызовы из бэкенда (recheck, restart_tunnel) дадут ошибку, если попадут в окно."
	case "router":
		what = "  • Все туннели разорвутся (~1-2 мин downtime).\n  • Если ты сейчас сидишь через VPN, TG отвалится до восстановления.\n  • Алерты придут сразу после reboot — это нормально.\n  • Кулдаун: 5 мин (повторное нажатие заблокировано)."
	}
	return fmt.Sprintf("🛠 Обслуживание\n\n⚠️ Перезапустить %s?\n\nЧто произойдёт:\n%s\n\nToken: %s (TTL 5 мин)", display, what, token)
}

func RestartConfirmKeyboard(userID int64, name, token string) InlineKeyboardMarkup {
	cd := func(action, arg string) string { return fmt.Sprintf("%s:%d:%s", action, userID, arg) }
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "✅ Подтвердить", CallbackData: fmt.Sprintf("maint_confirm:%d:%s:%s", userID, name, token)},
		{Text: "↩ Отмена", CallbackData: cd("maint_open", "_panel_")},
	}}}
}

func FirmwareScreenText(nickname string, fs wire.FirmwareStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📦 Прошивка — %s\n\nТекущая:    KeeneticOS %s\n", nickname, fs.Current)
	if fs.Available == "" {
		b.WriteString("Доступная:  актуальная\n")
	} else {
		fmt.Fprintf(&b, "Доступная:  KeeneticOS %s   ⬆ обновление\n", fs.Available)
	}
	if fs.Channel != "" {
		fmt.Fprintf(&b, "\nКанал: %s\n", fs.Channel)
	}
	return b.String()
}

func FirmwareScreenKeyboard(userID int64, fs wire.FirmwareStatus, cdRem time.Duration) InlineKeyboardMarkup {
	cd := func(action, arg string) string { return fmt.Sprintf("%s:%d:%s", action, userID, arg) }
	rows := [][]InlineKeyboardButton{}
	if fs.Available != "" {
		label := "⬆ Установить и перезагрузить"
		data := cd("maint_fw_install", "_panel_")
		if cdRem > 0 {
			label = fmt.Sprintf("🕒 Cooldown %s", fmtCooldown(cdRem))
			data = cd("maint_fw_open", "_panel_")
		}
		rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: data}})
	}
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🔄 Перепроверить", CallbackData: cd("maint_fw_check", "_panel_")},
		{Text: "↩ Назад", CallbackData: cd("maint_open", "_panel_")},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

func FirmwareConfirmText(token string) string {
	return fmt.Sprintf("📦 Прошивка\n\n⚠️ Установить новую прошивку и перезагрузить роутер?\n\n  • После старта установки роутер уйдёт в reboot (~2-3 мин).\n  • Все туннели разорвутся.\n  • Кулдаун: 5 мин.\n\nToken: %s (TTL 5 мин)", token)
}

func FirmwareConfirmKeyboard(userID int64, token string) InlineKeyboardMarkup {
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "✅ Подтвердить", CallbackData: fmt.Sprintf("maint_fw_confirm:%d:_panel_:%s", userID, token)},
		{Text: "↩ Отмена", CallbackData: fmt.Sprintf("maint_fw_open:%d:_panel_", userID)},
	}}}
}

func nameToDisplay(name string) string {
	switch name {
	case "hrneo": return "HydraRoute-Neo"
	case "awgmgr": return "awg-manager"
	case "router": return "роутер"
	default: return name
	}
}

func fmtCooldown(d time.Duration) string {
	d = d.Round(time.Second)
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%02d:%02d", m, s)
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/backend/tg/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/backend/tg/maint_panel.go internal/backend/tg/maint_panel_test.go
git commit -m "feat(tg): MaintPanel renderer (4 screens + keyboards)"
```

---

## Milestone 9: Backend — Reply-Keyboard Button

### Task 9.1: Add `🛠 Обслуживание` to per_router reply-keyboard

**Files:**
- Modify: `internal/backend/tg/reply_keyboard.go`
- Modify: `internal/backend/tg/keyboard_test.go` (or wherever reply-keyboard tests live)

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/tg/reply_keyboard_test.go (new file or extend existing)
package tg

import "testing"

func TestReplyKeyboard_PerRouter_HasMaintButton(t *testing.T) {
	v := ReplyKeyboardForTopic("per_router")
	kb, ok := v.(*ReplyKeyboardMarkup)
	if !ok { t.Fatalf("type=%T, want *ReplyKeyboardMarkup", v) }
	found := false
	for _, row := range kb.Keyboard {
		for _, b := range row {
			if b.Text == "🛠 Обслуживание" { found = true }
		}
	}
	if !found { t.Error("🛠 Обслуживание button missing from per_router keyboard") }
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/backend/tg/ -run TestReplyKeyboard_PerRouter_HasMaintButton -v
```
Expected: FAIL.

- [ ] **Step 3: Add the button**

In `internal/backend/tg/reply_keyboard.go`, modify the `per_router` case:

```go
case "per_router":
	return &ReplyKeyboardMarkup{
		Keyboard: [][]ReplyKeyboardButton{
			{{Text: "📊 Что происходит?"}, {Text: "🎛 Туннели"}},
			{{Text: "🌍 Через тоннель?"}, {Text: "🇷🇺 Напрямую?"}},
			{{Text: "🛣 Маршруты"}, {Text: "⬆ Обновить пакеты"}},
			{{Text: "🛠 Обслуживание"}},
		},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/backend/tg/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/backend/tg/reply_keyboard.go internal/backend/tg/reply_keyboard_test.go
git commit -m "feat(tg): reply-keyboard 🛠 Обслуживание button"
```

---

## Milestone 10: Backend — Router Handlers

### Task 10.1: Open Maint panel from reply-button

**Files:**
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/callbacks/router_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/callbacks/router_test.go (append to existing message-router test suite)
func TestHandleMessage_MaintButton_OpensPanel(t *testing.T) {
	tg := newFakeTG()  // existing test helper
	d := newTestDB(t)
	u := seedUser(t, d, "testkeen", 12345) // existing helper, returns *db.User with ThreadID set
	sink := &fakeSink{}
	r := NewRouterWithSink(d, tg, sink, Config{ChatID: u.ChatID, AdminUserID: u.ID})
	threadID := u.ThreadID
	r.HandleMessage(context.Background(), &tg.Message{
		Chat: tg.Chat{ID: u.ChatID}, From: tg.User{ID: u.ID},
		MessageThreadID: &threadID, Text: "🛠 Обслуживание",
	})
	// Expectation: bot sent a "обновляется…" loading message AND enqueued version_audit.
	if !tg.lastSendContains("Обслуживание") { t.Errorf("loading message not sent: %v", tg.sent) }
	if len(sink.enq) != 1 || sink.enq[0].Action != "version_audit" {
		t.Errorf("expected version_audit enqueued, got %v", sink.enq)
	}
}
```

(Replace `newFakeTG` / `newTestDB` / `seedUser` / `fakeSink` with whatever helpers `router_test.go` already uses; reuse — don't reinvent.)

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/backend/callbacks/ -run TestHandleMessage_MaintButton -v
```
Expected: FAIL — text branch missing.

- [ ] **Step 3: Add Router fields and message-handler branch**

In `internal/backend/callbacks/router.go`:

1. Add fields to `Router`:

```go
type Router struct {
	// ... existing fields
	pendingMaint     *pendingMaintStore
	cooldown         *cooldownStore
	maintConfirmAct  Action
}
```

2. Initialize in `NewRouterWithSink`:

```go
r.pendingMaint = newPendingMaintStore()
r.cooldown = newCooldownStore()
r.maintConfirmAct = NewMaintConfirmAction(sink, r.pendingMaint, r.cooldown, defaultCmdID)
```

3. Add branch to `HandleMessage`'s text switch:

```go
case "🛠 Обслуживание":
	if kind == "per_router" && user != nil {
		r.openMaintPanelMessage(ctx, m, user)
	} else {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"эта команда работает только в топике пользователя.", "", nil)
	}
```

4. Implement `openMaintPanelMessage` (mirror `openRoutesPanelMessage`):

```go
func (r *Router) openMaintPanelMessage(ctx context.Context, m *tg.Message, user *db.User) {
	loadingText := fmt.Sprintf("🛠 Обслуживание — %s\n   обновляется…", user.Nickname)
	mid, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, loadingText, "", nil)
	if err != nil {
		slog.Warn("maint panel send failed", "err", err)
		return
	}
	if r.cmdSink == nil { return }
	cmd := wire.Command{ID: defaultCmdID(), Action: "version_audit", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: m.Chat.ID, MessageID: mid, ThreadID: m.MessageThreadID}
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		slog.Warn("version_audit enqueue failed", "err", err)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/backend/callbacks/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/backend/callbacks/router.go internal/backend/callbacks/router_test.go
git commit -m "feat(callbacks): 🛠 Обслуживание opens maint panel"
```

### Task 10.2: HandleCallback dispatch for all maint_* actions

**Files:**
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/callbacks/router_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/backend/callbacks/router_test.go (append)

func TestHandleCallback_MaintRestart_RendersConfirm(t *testing.T) {
	// Setup as before; tap maint_restart:UID:hrneo
	// Assert tg.lastEditTextContains("Перезапустить HydraRoute-Neo")
	// Assert pendingMaint store has a token with Name=hrneo
	// (full body omitted — mirror existing routes-related router tests)
}

func TestHandleCallback_MaintConfirm_EnqueuesAndCooldown(t *testing.T) {
	// Pre-seed pendingMaint with name=router, token=T.
	// Tap maint_confirm:UID:router:T → assert sink.enq has service_restart{name=router}
	// AND cooldown.remaining(uid, "router_reboot") > 0.
}

func TestHandleCallback_MaintFwOpen_RendersFirmwareScreen(t *testing.T) {
	// Pre-seed lastVersionAudit cache with FirmwareStatus.
	// Tap maint_fw_open:UID:_panel_ → assert tg.lastEditTextContains("📦 Прошивка")
}

func TestHandleCallback_MaintClose_ClearsKeyboard(t *testing.T) {
	// Tap maint_close → assert EditMessageText called with empty inline keyboard
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/callbacks/ -run TestHandleCallback_Maint -v
```
Expected: FAIL — case branches missing.

- [ ] **Step 3: Add case branches in HandleCallback**

Find the giant `switch args.Action` in `HandleCallback`. Add:

```go
case "maint_open":
	r.handleMaintOpen(ctx, q, args)
	return
case "maint_close":
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "закрыто")
	empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, q.Message.Text, "", &empty)
	return
case "maint_restart":
	r.handleMaintRestart(ctx, q, args)
	return
case "maint_fw_open":
	r.handleMaintFwOpen(ctx, q, args)
	return
case "maint_fw_check":
	r.handleMaintFwCheck(ctx, q, args)
	return
case "maint_fw_install":
	r.handleMaintFwInstall(ctx, q, args)
	return
case "maint_confirm", "maint_fw_confirm":
	if r.maintConfirmAct != nil {
		action = r.maintConfirmAct
	}
```

- [ ] **Step 4: Implement the helpers**

```go
// internal/backend/callbacks/router.go (append)

// handleMaintOpen re-renders the panel: re-enqueues version_audit so the
// cmd-result handler edits with fresh data.
func (r *Router) handleMaintOpen(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "user not found")
		return
	}
	if r.cmdSink == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "command sink не подключён")
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "version_audit", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	loadingText := fmt.Sprintf("🛠 Обслуживание — %s\n   обновляется…", user.Nickname)
	loadingKB := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, loadingText, "", &loadingKB)
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не получилось запросить статус")
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleMaintRestart(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil { return }
	if args.MaintName == "router" {
		if rem := r.cooldown.remaining(user.ID, "router_reboot"); rem > 0 {
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, fmt.Sprintf("🕒 кулдаун ещё %s", rem.Round(time.Second)))
			return
		}
	}
	tok := makeMaintToken()
	r.pendingMaint.put(&pendingMaint{UserID: user.ID, Name: args.MaintName, Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	text := tg.RestartConfirmText(args.MaintName, tok)
	kb := tg.RestartConfirmKeyboard(user.ID, args.MaintName, tok)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleMaintFwOpen(ctx context.Context, q *tg.CallbackQuery, args Args) {
	// Render with last-known firmware status if cached; otherwise enqueue firmware_status fetch.
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil { return }
	fs, ok := r.lastFirmwareStatus(user.ID)  // helper below — pulls from a small in-memory cache
	if !ok {
		// Force a fetch and re-render after.
		r.handleMaintFwCheck(ctx, q, args)
		return
	}
	cdRem := r.cooldown.remaining(user.ID, "firmware_install")
	text := tg.FirmwareScreenText(user.Nickname, fs)
	kb := tg.FirmwareScreenKeyboard(user.ID, fs, cdRem)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleMaintFwCheck(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil || r.cmdSink == nil { return }
	cmd := wire.Command{ID: defaultCmdID(), Action: "firmware_status", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	loading := fmt.Sprintf("📦 Прошивка — %s\n   обновляется…", user.Nickname)
	empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, loading, "", &empty)
	if err := r.cmdSink.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не получилось")
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleMaintFwInstall(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil { return }
	if rem := r.cooldown.remaining(user.ID, "firmware_install"); rem > 0 {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, fmt.Sprintf("🕒 кулдаун ещё %s", rem.Round(time.Second)))
		return
	}
	tok := makeMaintToken()
	r.pendingMaint.put(&pendingMaint{UserID: user.ID, Name: "firmware", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	text := tg.FirmwareConfirmText(tok)
	kb := tg.FirmwareConfirmKeyboard(user.ID, tok)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}
```

- [ ] **Step 5: Run tests to verify they pass**

```sh
go test ./internal/backend/callbacks/ -v
```
Expected: PASS. (Resolve any helper signature mismatches by adapting to existing test fixtures.)

- [ ] **Step 6: Commit**

```sh
git add internal/backend/callbacks/router.go internal/backend/callbacks/router_test.go
git commit -m "feat(callbacks): handlers for maint_* callbacks (open/restart/fw)"
```

---

## Milestone 11: Backend — MaintPanelNotifier

### Task 11.1: Notifier edits panel on cmd result

**Files:**
- Create: `internal/backend/callbacks/maint_notifier.go`
- Create: `internal/backend/callbacks/maint_notifier_test.go`
- Modify: `internal/backend/callbacks/router.go` (add `lastVersionAudit` and `lastFirmwareStatus` cache + setters)
- Modify: `internal/backend/handler.go` (dispatch `version_audit`, `firmware_status`, `service_restart`, `firmware_install` results to MaintNotifier)

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/callbacks/maint_notifier_test.go
package callbacks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/pkg/wire"
)

func TestMaintNotifier_VersionAudit_EditsPanel(t *testing.T) {
	tg := newFakeTG()
	upstream := newFakeUpstream(map[string]string{"awgmgr": "2.9.0"})
	cd := newCooldownStore()
	store := newSimpleAuditCache()
	n := NewMaintNotifier(tg, upstream, cd, store, mockUser("testkeen"))
	va := wire.VersionAudit{AwgmgrVersion: "2.8.2", FirmwareCurrent: "4.2.6"}
	body, _ := json.Marshal(va)
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200}
	if err := n.NotifyCommandResult(context.Background(), ref, "version_audit",
		wire.CommandResult{Status: "ok", Output: string(body)}, 4096); err != nil {
		t.Fatal(err)
	}
	if !tg.lastEditTextContains("🛠 Обслуживание") { t.Error("panel text not edited") }
	if !tg.lastEditTextContains("awg-manager 2.8.2 → 2.9.0") {
		t.Errorf("expected upstream-aware update line; got:\n%s", tg.lastEditText())
	}
	// Ensure cache was populated for fw_open lookups.
	if got, ok := store.GetVersionAudit(1); !ok || got.AwgmgrVersion != "2.8.2" {
		t.Error("audit cache not populated")
	}
}

func TestMaintNotifier_FirmwareStatus_EditsFwScreen(t *testing.T) {
	// Similar — Output is FirmwareStatus JSON, action="firmware_status".
	// Assert tg.lastEditTextContains("📦 Прошивка")
}

func TestMaintNotifier_ServiceRestart_AppendsResult(t *testing.T) {
	// action="service_restart", Output="hrneo restart sent"
	// Assert panel re-renders with banner "✅ hrneo restart sent" or similar.
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/callbacks/ -run TestMaintNotifier -v
```
Expected: FAIL — undefined.

- [ ] **Step 3: Implement notifier**

```go
// internal/backend/callbacks/maint_notifier.go
package callbacks

import (
	"context"
	"encoding/json"
	"fmt"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/internal/backend/upstream"
	"github.com/anex/wg-monitor/pkg/wire"
)

// MaintNotifier consumes maint-related CommandResults and edits the panel
// in place. Handles four agent actions: version_audit, firmware_status,
// service_restart, firmware_install.
type MaintNotifier struct {
	TG       TGClient
	Up       *upstream.Cache
	CD       *cooldownStore
	Audit    *simpleAuditCache
	UserByID func(int64) *db.User
}

func NewMaintNotifier(tgClient TGClient, up *upstream.Cache, cd *cooldownStore, audit *simpleAuditCache, userByID func(int64) *db.User) *MaintNotifier {
	return &MaintNotifier{TG: tgClient, Up: up, CD: cd, Audit: audit, UserByID: userByID}
}

func (n *MaintNotifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, action string, result wire.CommandResult, _ int) error {
	user := n.UserByID(ref.UserID)  // see refactor note: MessageRef may need UserID; if not present, look up via thread/chat.
	if user == nil {
		return fmt.Errorf("user not resolvable from ref")
	}
	switch action {
	case "version_audit":
		var va wire.VersionAudit
		if err := json.Unmarshal([]byte(result.Output), &va); err != nil {
			return fmt.Errorf("decode version_audit: %w", err)
		}
		n.Audit.PutVersionAudit(user.ID, va)
		n.editPanel(ctx, ref, user, va)
	case "firmware_status":
		var fs wire.FirmwareStatus
		if err := json.Unmarshal([]byte(result.Output), &fs); err != nil {
			return fmt.Errorf("decode firmware_status: %w", err)
		}
		n.Audit.PutFirmwareStatus(user.ID, fs)
		cdRem := n.CD.remaining(user.ID, "firmware_install")
		text := tg.FirmwareScreenText(user.Nickname, fs)
		kb := tg.FirmwareScreenKeyboard(user.ID, fs, cdRem)
		_ = n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	case "service_restart", "firmware_install":
		// Re-render panel with a one-line result banner; pull the cached audit.
		va, _ := n.Audit.GetVersionAudit(user.ID)
		args := n.buildPanelArgs(user, va)
		text := "✅ " + result.Output + "\n\n" + tg.MaintPanelText(args)
		if result.Status == "err" {
			text = "❌ " + result.Output + "\n\n" + tg.MaintPanelText(args)
		}
		kb := tg.MaintPanelKeyboard(user.ID, args)
		_ = n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	return nil
}

func (n *MaintNotifier) editPanel(ctx context.Context, ref cmdpkg.MessageRef, user *db.User, va wire.VersionAudit) {
	args := n.buildPanelArgs(user, va)
	text := tg.MaintPanelText(args)
	kb := tg.MaintPanelKeyboard(user.ID, args)
	_ = n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *MaintNotifier) buildPanelArgs(user *db.User, va wire.VersionAudit) tg.MaintPanelArgs {
	var updates []tg.UpdateLine
	if v, _ := n.Up.Latest(context.Background(), "awgmgr"); v != "" && upstream.SoftwareNewerThan(va.AwgmgrVersion, v) {
		updates = append(updates, tg.UpdateLine{Name: "awg-manager", Installed: va.AwgmgrVersion, Available: v})
	}
	if v, _ := n.Up.Latest(context.Background(), "hrneo"); v != "" && va.HrneoVersion != "" && upstream.SoftwareNewerThan(va.HrneoVersion, v) {
		updates = append(updates, tg.UpdateLine{Name: "HydraRoute-Neo", Installed: va.HrneoVersion, Available: v})
	}
	if va.FirmwareAvail != "" && upstream.FirmwareNewerThan(va.FirmwareCurrent, va.FirmwareAvail) {
		updates = append([]tg.UpdateLine{{Name: "KeeneticOS", Installed: va.FirmwareCurrent, Available: va.FirmwareAvail}}, updates...)
	}
	return tg.MaintPanelArgs{
		Nickname:                  user.Nickname,
		HrneoVersion:              va.HrneoVersion,
		HrneoUptime:               va.HrneoUptime,
		HrneoRunning:              va.HrneoVersion != "",
		AwgmgrVersion:             va.AwgmgrVersion,
		AwgmgrUptime:              va.AwgmgrUptime,
		KeeneticOS:                "KN", // refine via user.Model when available
		Firmware:                  wire.FirmwareStatus{Current: va.FirmwareCurrent, Available: va.FirmwareAvail},
		Updates:                   updates,
		RouterCooldownRemaining:   n.CD.remaining(user.ID, "router_reboot"),
		FirmwareCooldownRemaining: n.CD.remaining(user.ID, "firmware_install"),
	}
}

// simpleAuditCache is an in-memory per-user cache of latest VersionAudit and
// FirmwareStatus payloads. Lost on restart — that's fine; next panel-open
// re-fetches.
type simpleAuditCache struct {
	mu sync.RWMutex
	va map[int64]wire.VersionAudit
	fs map[int64]wire.FirmwareStatus
}

func newSimpleAuditCache() *simpleAuditCache {
	return &simpleAuditCache{va: map[int64]wire.VersionAudit{}, fs: map[int64]wire.FirmwareStatus{}}
}

func (c *simpleAuditCache) PutVersionAudit(uid int64, va wire.VersionAudit) {
	c.mu.Lock(); defer c.mu.Unlock(); c.va[uid] = va
}
func (c *simpleAuditCache) GetVersionAudit(uid int64) (wire.VersionAudit, bool) {
	c.mu.RLock(); defer c.mu.RUnlock(); v, ok := c.va[uid]; return v, ok
}
func (c *simpleAuditCache) PutFirmwareStatus(uid int64, fs wire.FirmwareStatus) {
	c.mu.Lock(); defer c.mu.Unlock(); c.fs[uid] = fs
}
func (c *simpleAuditCache) GetFirmwareStatus(uid int64) (wire.FirmwareStatus, bool) {
	c.mu.RLock(); defer c.mu.RUnlock(); v, ok := c.fs[uid]; return v, ok
}
```

**Note on `cmdpkg.MessageRef.UserID`:** the code above assumes `MessageRef` carries `UserID`. If it does not in current main, either (a) extend `MessageRef` with a `UserID` field (and adjust all enqueue paths), or (b) resolve user from `ChatID + ThreadID` via `r.d.Users().GetByThreadID`. Pick the one that matches existing pattern from RoutesPanelNotifier.

- [ ] **Step 4: Add `lastFirmwareStatus` lookup helper to Router**

```go
// internal/backend/callbacks/router.go (append)
func (r *Router) lastFirmwareStatus(uid int64) (wire.FirmwareStatus, bool) {
	if r.auditCache == nil { return wire.FirmwareStatus{}, false }
	return r.auditCache.GetFirmwareStatus(uid)
}
```

Add `auditCache *simpleAuditCache` field on `Router`, initialise in `NewRouterWithSink`.

- [ ] **Step 5: Wire dispatch in handler.go**

In `internal/backend/handler.go`'s `cmdResultHandler` switch, add cases:

```go
case "version_audit", "firmware_status", "service_restart", "firmware_install":
	if d.MaintNotifier != nil {
		return d.MaintNotifier.NotifyCommandResult(ctx, ref, action, result, d.MaxChars)
	}
```

Add `MaintNotifier` field to `handler.Deps` (interface mirroring `RoutesNotifier`).

- [ ] **Step 6: Run tests**

```sh
go test ./internal/backend/... -v
```
Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add internal/backend/callbacks/maint_notifier.go internal/backend/callbacks/maint_notifier_test.go \
        internal/backend/callbacks/router.go internal/backend/handler.go internal/backend/handler_test.go
git commit -m "feat(backend): MaintNotifier dispatches version_audit/fw/restart results"
```

---

## Milestone 12: Backend — Smart-Reply Updates Section

### Task 12.1: Updates field + format

**Files:**
- Modify: `internal/backend/alerts/smart_reply.go`
- Modify: `internal/backend/alerts/format.go`
- Modify: `internal/backend/alerts/format_test.go`
- Modify: `internal/backend/callbacks/router.go` (`dispatchSmartReply` populates Updates)

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/alerts/format_test.go (append)
func TestFormatSmartReply_WithUpdates(t *testing.T) {
	args := SmartReplyArgs{
		Nickname: "testkeen",
		Tunnels: []TunnelView{},
		Updates: []UpdateAvailable{
			{Name: "KeeneticOS", Installed: "4.2.6", Available: "5.0.1"},
			{Name: "awg-manager", Installed: "2.8.2", Available: "2.9.0"},
		},
	}
	text, _ := FormatSmartReply(args)
	for _, want := range []string{
		"🟡 Доступны обновления:",
		"KeeneticOS 4.2.6 → 5.0.1",
		"awg-manager 2.8.2 → 2.9.0",
	} {
		if !strings.Contains(text, want) { t.Errorf("missing %q in:\n%s", want, text) }
	}
}

func TestFormatSmartReply_NoUpdatesSectionHidden(t *testing.T) {
	args := SmartReplyArgs{Nickname: "x", Tunnels: []TunnelView{}, Updates: nil}
	text, _ := FormatSmartReply(args)
	if strings.Contains(text, "Доступны обновления") {
		t.Error("update section should be hidden when Updates is empty")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/alerts/ -run TestFormatSmartReply_WithUpdates -v
```
Expected: FAIL — `Updates` field does not exist.

- [ ] **Step 3: Add field + format section**

In `internal/backend/alerts/smart_reply.go`:

```go
type SmartReplyArgs struct {
	// ... existing fields
	Updates []UpdateAvailable
}

type UpdateAvailable struct {
	Name      string
	Installed string
	Available string
}
```

In `internal/backend/alerts/format.go` (`FormatSmartReply`), append before keyboard:

```go
if len(args.Updates) > 0 {
	b.WriteString("\n🟡 Доступны обновления:\n")
	for _, u := range args.Updates {
		fmt.Fprintf(&b, "  • %s %s → %s\n", u.Name, u.Installed, u.Available)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/backend/alerts/ -v
```
Expected: PASS.

- [ ] **Step 5: Populate Updates in dispatchSmartReply**

In `internal/backend/callbacks/router.go`, modify `dispatchSmartReply`:

```go
// After args := alerts.SmartReplyArgs{...}, add:
if r.auditCache != nil && r.upstream != nil {
	if va, ok := r.auditCache.GetVersionAudit(user.ID); ok {
		args.Updates = computeUpdates(r.upstream, va)
	}
}

// New helper at file bottom:
func computeUpdates(up *upstream.Cache, va wire.VersionAudit) []alerts.UpdateAvailable {
	var out []alerts.UpdateAvailable
	if va.FirmwareAvail != "" && upstream.FirmwareNewerThan(va.FirmwareCurrent, va.FirmwareAvail) {
		out = append(out, alerts.UpdateAvailable{Name: "KeeneticOS", Installed: va.FirmwareCurrent, Available: va.FirmwareAvail})
	}
	if v, _ := up.Latest(context.Background(), "awgmgr"); v != "" && upstream.SoftwareNewerThan(va.AwgmgrVersion, v) {
		out = append(out, alerts.UpdateAvailable{Name: "awg-manager", Installed: va.AwgmgrVersion, Available: v})
	}
	if v, _ := up.Latest(context.Background(), "hrneo"); v != "" && va.HrneoVersion != "" && upstream.SoftwareNewerThan(va.HrneoVersion, v) {
		out = append(out, alerts.UpdateAvailable{Name: "HydraRoute-Neo", Installed: va.HrneoVersion, Available: v})
	}
	return out
}
```

Add `upstream *upstream.Cache` field to `Router` (set via new `SetUpstream` method called from cmd/backend/main.go).

- [ ] **Step 6: Verify build**

```sh
go build ./...
go test ./internal/backend/...
```
Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add internal/backend/alerts/smart_reply.go internal/backend/alerts/format.go \
        internal/backend/alerts/format_test.go internal/backend/callbacks/router.go
git commit -m "feat(backend): smart-reply Updates section + computeUpdates helper"
```

---

## Milestone 13: Backend — Config + Wiring

### Task 13.1: Backend config — Upstream block

**Files:**
- Modify: `internal/backend/config.go`
- Modify: `internal/backend/config_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/backend/config_test.go (append)
func TestConfig_UpstreamDefaults(t *testing.T) {
	yaml := `
chat_id: 1
` // no upstream:
	cfg, _ := LoadConfig(strings.NewReader(yaml))
	if cfg.Upstream.CacheTTL != 12*time.Hour {
		t.Errorf("default CacheTTL=%v, want 12h", cfg.Upstream.CacheTTL)
	}
}

func TestConfig_UpstreamParsed(t *testing.T) {
	yaml := `
chat_id: 1
upstream:
  awgmgr_repo: "Slava-Shchipunov/awg-keenetic"
  hrneo_repo:  "Mihaylov-Sergei/HydraRoute-Neo"
  cache_ttl: "6h"
`
	cfg, err := LoadConfig(strings.NewReader(yaml))
	if err != nil { t.Fatal(err) }
	if cfg.Upstream.AwgmgrRepo != "Slava-Shchipunov/awg-keenetic" { t.Errorf("AwgmgrRepo=%q", cfg.Upstream.AwgmgrRepo) }
	if cfg.Upstream.CacheTTL != 6*time.Hour { t.Errorf("CacheTTL=%v", cfg.Upstream.CacheTTL) }
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/backend/ -run TestConfig_Upstream -v
```
Expected: FAIL.

- [ ] **Step 3: Add config struct + default**

```go
// internal/backend/config.go — extend Config + add defaulting in LoadConfig

type Config struct {
	// ... existing fields
	Upstream UpstreamConfig `yaml:"upstream"`
}

type UpstreamConfig struct {
	AwgmgrRepo string        `yaml:"awgmgr_repo"`
	HrneoRepo  string        `yaml:"hrneo_repo"`
	CacheTTL   time.Duration `yaml:"cache_ttl"`
}

// In LoadConfig after Unmarshal:
if cfg.Upstream.CacheTTL == 0 {
	cfg.Upstream.CacheTTL = 12 * time.Hour
}
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/backend/ -run TestConfig_Upstream -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/backend/config.go internal/backend/config_test.go
git commit -m "feat(backend): UpstreamConfig with 12h default TTL"
```

### Task 13.2: cmd/backend/main.go — wire all maint plumbing

**Files:**
- Modify: `cmd/backend/main.go`

- [ ] **Step 1: Wire upstream cache, MaintNotifier, plumbing**

After existing routes-cache wiring, add:

```go
// Upstream version cache.
var upSources []upstream.Source
if cfg.Upstream.AwgmgrRepo != "" {
	upSources = append(upSources, upstream.Source{Name: "awgmgr", GitHubRepo: cfg.Upstream.AwgmgrRepo})
}
if cfg.Upstream.HrneoRepo != "" {
	upSources = append(upSources, upstream.Source{Name: "hrneo", GitHubRepo: cfg.Upstream.HrneoRepo})
}
upCache := upstream.NewCache(cfg.Upstream.CacheTTL, upSources)

// Maint plumbing — pendingMaint, cooldown, audit cache live inside the router
// (initialised in NewRouterWithSink). MaintNotifier consumes them.
maintNotifier := callbacks.NewMaintNotifier(
	tgClient, upCache,
	router.CooldownStore(), router.AuditCache(),
	func(uid int64) *db.User { u, _ := d.Users().GetByID(uid); return u },
)

// Wire upstream + audit cache into router for smart-reply updates.
router.SetUpstream(upCache)

// Pass MaintNotifier to handler Deps.
handlerDeps.MaintNotifier = maintNotifier
```

(Add `Router.CooldownStore()`, `Router.AuditCache()`, `Router.SetUpstream()` accessors as needed.)

- [ ] **Step 2: Verify build + tests**

```sh
go build ./...
go test ./...
```
Expected: PASS.

- [ ] **Step 3: Commit**

```sh
git add cmd/backend/main.go internal/backend/callbacks/router.go
git commit -m "feat(backend): wire upstream.Cache and MaintNotifier in main"
```

---

## Milestone 14: Documentation

### Task 14.1: README feature line

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add bullet to features section**

Find the v0.10.0 routes line in README and append below:

```markdown
- **Maintenance panel** — restart HydraRoute-Neo / awg-manager / router and install KeeneticOS firmware updates from Telegram, with confirm tokens and 5-min cooldowns for destructive ops. Smart-reply also surfaces "🟡 Доступны обновления" when installed software lags upstream.
```

- [ ] **Step 2: Commit**

```sh
git add README.md
git commit -m "docs(README): Maintenance panel feature line"
```

---

## Milestone 15: Manual Smoke (Out-of-PR)

After PR merge + deploy via `wizard update-backend && wizard update-agent`:

- [ ] Tap `🛠 Обслуживание` → panel shows hrneo/awgmgr versions + uptimes within 2 sec.
- [ ] Tap `🔁 Restart hrneo` → confirm → status banner "✅ hrneo restart sent" within 2 sec; SSH `pidof hrneo` returns new PID.
- [ ] Tap `🔁 Restart awg-mgr` → confirm → web UI 502s briefly, then 200.
- [ ] Tap `🔁 Reboot router` → confirm → router pings stop, ~90s later RECOVERY alert arrives.
- [ ] Within 5 min of reboot, re-open panel — Reboot button shows `🕒 Cooldown HH:MM`.
- [ ] Tap `📦 Прошивка` while no update available → screen says "актуальная", no install button.
- [ ] Patch installed version older than upstream and tap `📊 Что происходит?` — "🟡 Доступны обновления" section appears.
- [ ] Tag `v0.11.0-rc1`, smoke on testkeen, then tag `v0.11.0`.

---

## Self-Review (filled by author)

**Spec coverage:**
- Restart hrneo / awgmgr / router → M3, M4, M6, M8, M10. ✓
- Firmware show + install → M3, M8, M10, M11. ✓
- Soft warnings in smart-reply → M5, M6, M11, M12. ✓
- Confirm tokens + cooldown for router/firmware → M6.1, M6.2, M10. ✓
- 6 SSH probes (TBDs) → M0. ✓
- Agent config gates → M4.2. ✓
- Backend config block → M13.1. ✓
- Tests on every milestone — TDD throughout. ✓

**Placeholder scan:**
- `errParseInt` placeholder in compare.go — flagged in step 3 with explicit "replace with `errors.New(...)`" instruction. Acceptable as a tagged note; an executing engineer must replace.
- "REPLACE with M0 Probe N" markers in M0/M3 — these are deliberate placeholders that get filled by M0 output before M2/M3 starts. Acceptable.
- All other steps have complete code.

**Type consistency:**
- `MaintName` / `MaintToken` field names consistent across parse.go, maint.go, router.go.
- `pendingMaint`, `cooldownEntry`, `simpleAuditCache` consistent.
- `wire.VersionAudit`, `wire.FirmwareStatus` field names match between agent and backend usage.
- `upstream.Source.Name` is the stable key ("awgmgr", "hrneo") used throughout.

No issues to fix.
