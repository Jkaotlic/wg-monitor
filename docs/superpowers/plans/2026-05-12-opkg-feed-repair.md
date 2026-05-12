# OPKG Feed Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tolerate `opkg update` partial failures (≥1 feed downloaded) and let the TG operator one-tap a `🔧 Отключить мёртвый фид` button under the upgrade message to comment out the dead feed line + auto-retry SmartUpgrade.

**Architecture:** Agent parses `opkg update` stdout, returns structured `OpkgUpgradeResult{FailedFeeds}` in a new optional `CommandResult.Payload` field (`Output` stays human-readable, preserving forward-compat). When the backend sees `FailedFeeds` non-empty in a `CommandResult` for `opkg_smart_upgrade` or `opkg_feed_disable`, it renders one inline button per URL with a 5-min single-use token. Tap → callback → new wire action `opkg_feed_disable{url}` → agent comments out the matching `src/gz` line in `/opt/etc/opkg.conf` or `/opt/etc/opkg/*.conf` (with timestamped backup, atomic write) → immediately re-runs SmartUpgrade → bundled report edits the original message in place.

**Tech Stack:** Go 1.23 (existing project), `pkg/wire` JSON contract, `internal/agent/actions` runner, `internal/backend/callbacks` Action interface + token store pattern (mirrors `pendingMaint` from M11 maintenance panel).

---

## File Structure

**Modified:**

| File | Change |
|---|---|
| `pkg/wire/types.go` | Add `OpkgFeedDisable` to `validCommandActions`. Add `Payload json.RawMessage` field to `CommandResult`. Add `OpkgUpgradeResult` struct. |
| `pkg/wire/types_test.go` | Cover new constant + struct round-trip + `Payload` round-trip. |
| `internal/agent/actions/opkg.go` | Add `ConfigRoot`, `parseOpkgUpdate`, `normalizeFeedURL`, `disableMatchingLine`, `backupAndWrite`. Change `SmartUpgrade` signature to return `(status, output string, payload wire.OpkgUpgradeResult)`; tolerate partial-failure update. Add `DisableFeed` method (auto-retry SmartUpgrade). |
| `internal/agent/actions/opkg_test.go` | New tests (10) — see Task 1, 3, 5, 6, 7. |
| `internal/agent/actions/runner.go` | Update `OpkgExecutor` interface (new `SmartUpgrade` signature + `DisableFeed` method). Update `opkg_upgrade` dispatch to flatten payload into `CommandResult`. Add `opkg_feed_disable` dispatch case. |
| `internal/agent/actions/runner_test.go` | Cover both dispatch paths. |
| `internal/backend/callbacks/parse.go` | Add `opkg_disable` to `validActions`. Parse `opkg_disable:<uid>:_menu:<token>` (`_menu` filler keeps the 3-segment min so existing parsing checks don't trip). Store token in `Args.OpkgRepairToken`. |
| `internal/backend/callbacks/parse_test.go` | Tests for valid / missing-token / unknown-action. |
| `internal/backend/callbacks/router.go` | Add `pendingOpkgRepair *pendingOpkgRepairStore` field + `opkgRepairAct Action`. Wire in `NewRouterWithSink`. Add dispatch case for `opkg_disable` in `handleCallback`. |
| `internal/backend/callbacks/router_test.go` | Cover end-to-end token consume + enqueue. |
| `internal/backend/handler.go` | In `cmdResultHandler` relay path: when CommandResult is for `opkg_upgrade` or `opkg_feed_disable` and has `Payload` with `FailedFeeds`, render inline-button keyboard with token per URL. Register token entries in `pendingOpkgRepair`. |
| `internal/backend/handler_test.go` | Cover render with / without FailedFeeds. |
| `cmd/backend/main.go` | Construct `pendingOpkgRepairStore` and pass into router via setter; wire repair-render hook into handler relay. |

**Created:**

| File | Purpose |
|---|---|
| `internal/backend/callbacks/opkg_repair.go` | `pendingOpkgRepair` entry struct, `pendingOpkgRepairStore` (mirrors `pendingMaintStore`), `OpkgRepairAction` (mirrors `MaintConfirmAction`), `makeOpkgRepairToken` helper. |
| `internal/backend/callbacks/opkg_repair_test.go` | Store put/consume/expired/wrong-user tests. Action enqueues + bad-token tests. |

---

## Task 1: Parse `opkg update` stdout

**Files:**
- Modify: `internal/agent/actions/opkg.go`
- Test: `internal/agent/actions/opkg_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/actions/opkg_test.go` (just before `equalStrings`):

```go
// Real-world opkg update output when one of five feeds is dead (HTTP 404).
// opkg exits 1 even though four feeds downloaded successfully — historically
// SmartUpgrade treated this as total failure, blocking the upgrade.
const partialUpdateOutput = `Downloading http://bin.entware.net/aarch64-k3.10/Packages.gz
Updated list of available packages in /opt/var/opkg-lists/entware
Downloading http://bin.entware.net/aarch64-k3.10/keenetic/Packages.gz
Updated list of available packages in /opt/var/opkg-lists/keendev
Downloading http://repo.hoaxisr.ru/aarch64-k3.10/Packages.gz
Updated list of available packages in /opt/var/opkg-lists/hoaxisr
Downloading https://git.zerrolabs.org/Ground-Zerro/release/pages/keenetic/aarch64-k3.10/Packages.gz
Updated list of available packages in /opt/var/opkg-lists/ground-zerro
Downloading https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz
*** Failed to download the package list from https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz

Collected errors:
 * opkg_download: Failed to download https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz, wget returned 8.
`

func TestParseOpkgUpdate_PartialFailure(t *testing.T) {
	got := parseOpkgUpdate(partialUpdateOutput)
	if got.feedsUpdated != 4 {
		t.Errorf("feedsUpdated = %d, want 4", got.feedsUpdated)
	}
	if len(got.failedFeeds) != 1 {
		t.Fatalf("failedFeeds = %v, want 1 entry", got.failedFeeds)
	}
	if got.failedFeeds[0] != "https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz" {
		t.Errorf("failedFeeds[0] = %q", got.failedFeeds[0])
	}
}

func TestParseOpkgUpdate_AllSuccess(t *testing.T) {
	out := "Downloading http://x/Packages.gz\nUpdated list of available packages in /opt/var/opkg-lists/x\n"
	got := parseOpkgUpdate(out)
	if got.feedsUpdated != 1 || len(got.failedFeeds) != 0 {
		t.Errorf("got %+v, want feedsUpdated=1, failedFeeds=[]", got)
	}
}

func TestParseOpkgUpdate_TotalFailure(t *testing.T) {
	out := "Downloading http://x/Packages.gz\n*** Failed to download the package list from http://x/Packages.gz\n"
	got := parseOpkgUpdate(out)
	if got.feedsUpdated != 0 || len(got.failedFeeds) != 1 {
		t.Errorf("got %+v, want feedsUpdated=0, failedFeeds=[1]", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/actions/ -run TestParseOpkgUpdate -v`
Expected: FAIL — `undefined: parseOpkgUpdate`.

- [ ] **Step 3: Implement `parseOpkgUpdate`**

Append to `internal/agent/actions/opkg.go` (just before `humanKB`):

```go
// opkgUpdateOutcome is the parsed shape of `opkg update` combined output.
type opkgUpdateOutcome struct {
	feedsUpdated int      // count of "Updated list of available packages in ..." lines
	failedFeeds  []string // URLs from "*** Failed to download the package list from <url>" lines
}

// parseOpkgUpdate scans opkg's combined output and tallies feed success/failure.
// `Collected errors:` block is ignored — URLs appear there in a different
// format and would otherwise double-count.
func parseOpkgUpdate(out string) opkgUpdateOutcome {
	var o opkgUpdateOutcome
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Updated list of available packages in "):
			o.feedsUpdated++
		case strings.HasPrefix(line, "*** Failed to download the package list from "):
			url := strings.TrimPrefix(line, "*** Failed to download the package list from ")
			url = strings.TrimSpace(url)
			if url != "" {
				o.failedFeeds = append(o.failedFeeds, url)
			}
		}
	}
	return o
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/actions/ -run TestParseOpkgUpdate -v`
Expected: PASS, three tests.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/actions/opkg.go internal/agent/actions/opkg_test.go
git commit -m "feat(agent): parseOpkgUpdate — tally feed success/failure"
```

---

## Task 2: Add `ConfigRoot` field to `OpkgRunner`

**Files:**
- Modify: `internal/agent/actions/opkg.go`

Rationale: tests need to redirect opkg-config paths off `/opt/etc`. A single configurable root keeps the production path unchanged and makes future tests trivial.

- [ ] **Step 1: Add field + helpers**

In `internal/agent/actions/opkg.go`, extend the `OpkgRunner` struct:

```go
type OpkgRunner struct {
	LockPath string
	LockTTL  time.Duration
	Exec     ExecFunc
	Now      func() time.Time
	// ConfigRoot is the directory containing `opkg.conf` and the `opkg/`
	// subdirectory of per-feed `.conf` files. Defaults to "/opt/etc" when
	// empty — tests substitute t.TempDir() to point at a sandbox.
	ConfigRoot string
}
```

Add two helpers (place just above `now()`):

```go
// configRoot returns ConfigRoot or the production default.
func (o *OpkgRunner) configRoot() string {
	if o.ConfigRoot != "" {
		return o.ConfigRoot
	}
	return "/opt/etc"
}

// opkgConfPaths returns the candidate paths that may declare feeds, in
// scan order: the single-file `opkg.conf` first, then any per-feed
// `opkg/*.conf`. Missing files are silently skipped by callers.
func (o *OpkgRunner) opkgConfPaths() []string {
	root := o.configRoot()
	paths := []string{root + "/opkg.conf"}
	matches, _ := filepath.Glob(root + "/opkg/*.conf")
	paths = append(paths, matches...)
	return paths
}
```

Add `"path/filepath"` to the import block.

- [ ] **Step 2: Verify build**

Run: `go build ./internal/agent/actions/`
Expected: success, no test changes yet.

- [ ] **Step 3: Commit**

```bash
git add internal/agent/actions/opkg.go
git commit -m "feat(agent): OpkgRunner.ConfigRoot — testable opkg config path"
```

---

## Task 3: SmartUpgrade tolerates partial-update failure

**Files:**
- Modify: `internal/agent/actions/opkg.go`
- Test: `internal/agent/actions/opkg_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/actions/opkg_test.go`:

```go
func TestOpkg_SmartUpgrade_PartialUpdateFailure_Continues(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "update":
			return []byte(partialUpdateOutput), errors.New("exit status 1")
		case "list-upgradable":
			return []byte(""), nil // empty → SmartUpgrade exits early with "up to date"
		}
		return nil, nil
	})
	status, output, payload := o.SmartUpgrade(context.Background())
	if status != "ok" {
		t.Fatalf("status=%q, want ok; output=%q", status, output)
	}
	if !strings.Contains(output, "anonym-tsk.github.io") {
		t.Errorf("output should surface dead URL; got %q", output)
	}
	if len(payload.FailedFeeds) != 1 || payload.FailedFeeds[0] != "https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz" {
		t.Errorf("payload.FailedFeeds = %v", payload.FailedFeeds)
	}
}

func TestOpkg_SmartUpgrade_TotalUpdateFailure_Errs(t *testing.T) {
	totalFail := `Downloading http://bin.entware.net/aarch64-k3.10/Packages.gz
*** Failed to download the package list from http://bin.entware.net/aarch64-k3.10/Packages.gz
`
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "update" {
			return []byte(totalFail), errors.New("exit status 1")
		}
		return nil, nil
	})
	status, _, _ := o.SmartUpgrade(context.Background())
	if status != "err" {
		t.Fatalf("status=%q, want err", status)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/actions/ -run TestOpkg_SmartUpgrade_(Partial|Total) -v`
Expected: FAIL — both compile errors (signature mismatch) and behavior mismatch.

- [ ] **Step 3: Change `SmartUpgrade` signature and add partial-tolerance logic**

In `internal/agent/actions/opkg.go`, add import:

```go
"github.com/anex/wg-monitor/pkg/wire"
```

Replace the current `SmartUpgrade` method (lines 60–143) with:

```go
func (o *OpkgRunner) SmartUpgrade(ctx context.Context) (status, output string, payload wire.OpkgUpgradeResult) {
	if held, age, ok := o.lockHeldFresh(); ok {
		return "locked", fmt.Sprintf("opkg lock held by another op (age %v, lock file: %s)", age.Round(time.Second), held), payload
	}
	if err := o.takeLock(); err != nil {
		return "err", "acquire lock: " + err.Error(), payload
	}
	defer o.releaseLock()

	updateOut, updateErr := o.Exec(ctx, "opkg", "update")
	upd := parseOpkgUpdate(string(updateOut))
	payload.FailedFeeds = upd.failedFeeds
	if updateErr != nil && upd.feedsUpdated == 0 {
		return "err", "opkg update failed: " + updateErr.Error() + "\n" + string(updateOut), payload
	}
	// Partial success: ≥1 feed updated. Continue with upgrade flow.

	listing, err := o.Exec(ctx, "opkg", "list-upgradable")
	if err != nil {
		return "err", "opkg list-upgradable failed: " + err.Error() + "\n" + string(listing), payload
	}
	pkgs := parseUpgradablePkgs(string(listing))
	listingHasNoise := strings.Contains(string(listing), "ERROR") || strings.Contains(string(listing), "WARNING")
	if len(pkgs) == 0 && !listingHasNoise {
		out := "✅ Все пакеты актуальны — обновлять нечего."
		out = appendFailedFeedsBlock(out, upd.failedFeeds)
		payload.Output = out
		return "ok", out, payload
	}

	freeKB, totalKB, err := o.dfOpt(ctx)
	if err != nil {
		return "err", "df /opt failed: " + err.Error(), payload
	}
	neededKB := int64(0)
	if len(pkgs) > 0 {
		neededKB = o.estimateInstallSizeKB(ctx, pkgs)
		headroomKB := totalKB / 10
		if freeKB-neededKB < headroomKB {
			return "err", fmt.Sprintf(
				"❌ Не хватит места на /opt.\n"+
					"Пакетов к обновлению: %d\n"+
					"Оценка установки: %s\n"+
					"Свободно сейчас: %s / всего %s\n"+
					"После upgrade осталось бы: %s (порог headroom %s = 10%%)\n"+
					"Освободи /opt и повтори.",
				len(pkgs), humanKB(neededKB), humanKB(freeKB), humanKB(totalKB),
				humanKB(freeKB-neededKB), humanKB(headroomKB),
			), payload
		}
	}

	upgradeOut, err := o.Exec(ctx, "opkg", "upgrade")
	if err != nil {
		return "err", "opkg upgrade failed: " + err.Error() + "\n" + string(upgradeOut), payload
	}
	if upgraded := parseUpgradedFromOutput(string(upgradeOut)); len(upgraded) > 0 {
		pkgs = upgraded
	}
	post, _, _ := o.dfOpt(ctx)

	sizeNote := "~" + humanKB(neededKB)
	if neededKB == 0 {
		sizeNote = "размер не определён"
	}
	pkgListStr := strings.Join(pkgs, ", ")
	if pkgListStr == "" {
		pkgListStr = "(не определён)"
	}
	if len(pkgListStr) > 200 {
		pkgListStr = pkgListStr[:200] + "…"
	}
	out := fmt.Sprintf(
		"✅ Обновлено пакетов: %d (%s)\n"+
			"Список: %s\n"+
			"Свободно после: %s / %s\n\n"+
			"%s",
		len(pkgs), sizeNote, pkgListStr,
		humanKB(post), humanKB(totalKB),
		strings.TrimSpace(string(upgradeOut)),
	)
	out = appendFailedFeedsBlock(out, upd.failedFeeds)
	payload.Output = out
	return "ok", out, payload
}

// appendFailedFeedsBlock appends a "⚠️ Недоступные фиды" section to a SmartUpgrade
// report when at least one feed failed to download during opkg update. No-op
// when the list is empty.
func appendFailedFeedsBlock(report string, failed []string) string {
	if len(failed) == 0 {
		return report
	}
	var b strings.Builder
	b.WriteString(report)
	b.WriteString("\n\n⚠️ Недоступные фиды:")
	for _, u := range failed {
		b.WriteString("\n • ")
		b.WriteString(u)
	}
	return b.String()
}
```

- [ ] **Step 4: Run all opkg tests to verify they pass**

Run: `go test ./internal/agent/actions/ -run TestOpkg -v`
Expected: existing tests still pass (DryRun unchanged); new partial/total tests pass. **Note:** existing build will break in `runner.go` because `OpkgExecutor` interface still has old signature — that's the next task.

Run: `go build ./internal/agent/actions/`
Expected: FAIL with `*OpkgRunner does not implement OpkgExecutor (wrong signature)`.

- [ ] **Step 5: Update `OpkgExecutor` interface and runner.go**

Open `internal/agent/actions/runner.go`. Change the `OpkgExecutor` interface (around line 38):

```go
type OpkgExecutor interface {
	DryRun(ctx context.Context) (status, output string)
	SmartUpgrade(ctx context.Context) (status, output string, payload wire.OpkgUpgradeResult)
}
```

Add `"encoding/json"` to imports if not already (it is — line 26).

Change the `opkg_upgrade` dispatch case (around line 106) to flatten the payload:

```go
	case "opkg_upgrade":
		if r.Opkg == nil {
			return "err", "opkg runner not configured"
		}
		status, output, _ := r.Opkg.SmartUpgrade(ctx)
		return status, output
```

Note: the payload is dropped here because `dispatch` returns only `(status, output)`. We need to thread `payload` out. Instead of widening `dispatch`'s signature, change `Execute` to call `SmartUpgrade` directly when the action is `opkg_upgrade`. **Use this approach instead** — replace the entire `Execute` method (lines 62–71):

```go
func (r *Runner) Execute(ctx context.Context, cmd wire.Command) wire.CommandResult {
	start := r.now()
	status, output, payload := r.dispatchWithPayload(ctx, cmd)
	res := wire.CommandResult{
		ID:         cmd.ID,
		Status:     status,
		Output:     output,
		DurationMs: r.now().Sub(start).Milliseconds(),
	}
	if !payload.IsZero() {
		b, err := json.Marshal(payload)
		if err == nil {
			res.Payload = b
		}
	}
	return res
}
```

And rename the existing `dispatch` to `dispatchWithPayload`, returning a third `payload` value. Most cases just `return status, output, wire.OpkgUpgradeResult{}` (zero value). The `opkg_upgrade` case now returns the real payload:

```go
func (r *Runner) dispatchWithPayload(ctx context.Context, cmd wire.Command) (status, output string, payload wire.OpkgUpgradeResult) {
	switch cmd.Action {
	case "restart_tunnel":
		// ...existing logic...
		return "ok", "all tunnels restarted (awg-manager)", payload
	// ...etc — every existing case gets a trailing payload return...
	case "opkg_upgrade":
		if r.Opkg == nil {
			return "err", "opkg runner not configured", payload
		}
		s, o, p := r.Opkg.SmartUpgrade(ctx)
		return s, o, p
	// ...remaining cases unchanged but return zero payload...
	default:
		return "err", "unknown action: " + cmd.Action, payload
	}
}
```

Add `IsZero` helper to `pkg/wire/types.go` (will be done in Task 4; for now define inline):

```go
// Inline in runner.go (will move to types.go in Task 4):
func payloadIsZero(p wire.OpkgUpgradeResult) bool {
	return p.Output == "" && len(p.FailedFeeds) == 0
}
```

Use `payloadIsZero(payload)` instead of `payload.IsZero()` in `Execute`.

**Compile check:** at this point `wire.OpkgUpgradeResult` does NOT yet exist (defined in Task 4). Add a temporary local definition above `Execute`:

```go
// TEMP: moved to pkg/wire in Task 4.
// (REMOVE in Task 4.)
```

Actually — to avoid build-break churn across two tasks, **swap the order**: do Task 4 (wire types) before this Task 3 finalization. Adjust by completing Task 4 first if you hit the temp-type problem. **Recommended path:** finish Task 3 Step 1–4 (parsing + SmartUpgrade body works in isolation), then do Task 4 to add `wire.OpkgUpgradeResult`, then come back to Task 3 Step 5 to wire runner.go. To keep tasks atomic, **defer Step 5 until after Task 4**.

- [ ] **Step 6: Commit (parser + SmartUpgrade body only)**

```bash
git add internal/agent/actions/opkg.go internal/agent/actions/opkg_test.go
git commit -m "feat(agent): SmartUpgrade tolerates partial opkg-update failure"
```

(Note: package will compile only after Task 4 + Task 3 Step 5 land. CI is OK with a temporarily broken inter-package build only on a feature branch; if working on main, complete Task 4 + return to Step 5 before pushing.)

---

## Task 4: Add `wire.OpkgUpgradeResult` and `CommandResult.Payload`

**Files:**
- Modify: `pkg/wire/types.go`
- Test: `pkg/wire/types_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/wire/types_test.go`:

```go
func TestIsValidCommandAction_OpkgFeedDisable(t *testing.T) {
	if !IsValidCommandAction("opkg_feed_disable") {
		t.Error("opkg_feed_disable should be a valid command action")
	}
}

func TestOpkgUpgradeResult_JSONRoundTrip(t *testing.T) {
	in := OpkgUpgradeResult{
		Output:      "✅ Обновлено: 3 пакета",
		FailedFeeds: []string{"https://dead.example/Packages.gz"},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out OpkgUpgradeResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Output != in.Output {
		t.Errorf("Output: got %q want %q", out.Output, in.Output)
	}
	if len(out.FailedFeeds) != 1 || out.FailedFeeds[0] != in.FailedFeeds[0] {
		t.Errorf("FailedFeeds: got %v want %v", out.FailedFeeds, in.FailedFeeds)
	}
}

func TestOpkgUpgradeResult_OmitsEmptyFailedFeeds(t *testing.T) {
	in := OpkgUpgradeResult{Output: "ok"}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "failed_feeds") {
		t.Errorf("empty failed_feeds should be omitted, got %s", b)
	}
}

func TestCommandResult_PayloadRoundTrip(t *testing.T) {
	payload := OpkgUpgradeResult{FailedFeeds: []string{"https://x/Packages.gz"}}
	pb, _ := json.Marshal(payload)
	in := CommandResult{
		ID:      "cmd-1",
		Status:  "ok",
		Output:  "hello",
		Payload: pb,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out CommandResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(out.Payload) != string(pb) {
		t.Errorf("Payload round-trip mismatch: %s vs %s", out.Payload, pb)
	}
}

func TestCommandResult_OmitsEmptyPayload(t *testing.T) {
	in := CommandResult{ID: "x", Status: "ok"}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "payload") {
		t.Errorf("nil payload should be omitted, got %s", b)
	}
}
```

If `encoding/json` and `strings` aren't already imported in `types_test.go`, add them.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/wire/ -run "TestIsValidCommandAction_OpkgFeedDisable|TestOpkgUpgradeResult|TestCommandResult_(Payload|OmitsEmpty)" -v`
Expected: FAIL — `OpkgUpgradeResult` undefined, `CommandResult.Payload` undefined, action not in map.

- [ ] **Step 3: Add the constant, struct, and field**

In `pkg/wire/types.go`:

Change the imports to add `encoding/json`:

```go
import (
	"encoding/json"
	"time"
)
```

Add `"opkg_feed_disable"` to the `validCommandActions` map (alphabetical-ish placement near `opkg_upgrade`):

```go
var validCommandActions = map[string]bool{
	"restart_tunnel":    true,
	"diag_now":          true,
	"pingcheck_now":     true,
	"opkg_upgrade":      true,
	"opkg_feed_disable": true,
	// ...rest unchanged
}
```

Replace the `CommandResult` struct:

```go
type CommandResult struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Output     string          `json:"output,omitempty"`
	DurationMs int64           `json:"duration_ms"`
	// Payload carries action-specific structured data. Currently used by
	// opkg_upgrade / opkg_feed_disable to surface `failed_feeds` to the
	// backend so it can render repair buttons. Optional: omitted entirely
	// when the action has no structured response.
	Payload json.RawMessage `json:"payload,omitempty"`
}
```

Append at the bottom of `types.go`:

```go
// OpkgUpgradeResult is the structured payload returned by opkg_upgrade and
// opkg_feed_disable. Output mirrors the human-readable text in
// CommandResult.Output (the backend may use either, but stays canonical for
// rendering). FailedFeeds is the list of URLs opkg failed to download during
// the update step; non-empty triggers the repair-button UI.
type OpkgUpgradeResult struct {
	Output      string   `json:"output,omitempty"`
	FailedFeeds []string `json:"failed_feeds,omitempty"`
}

// IsZero reports whether the payload carries no actionable data. Used by the
// agent runner to decide whether to attach the payload to CommandResult.
func (r OpkgUpgradeResult) IsZero() bool {
	return r.Output == "" && len(r.FailedFeeds) == 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/wire/ -v`
Expected: PASS, all wire tests including new ones.

- [ ] **Step 5: Finish Task 3 Step 5 (runner.go wiring)**

Return to `internal/agent/actions/runner.go`. Replace the temporary `payloadIsZero` helper with `payload.IsZero()` from wire. Final shape of `Execute`:

```go
func (r *Runner) Execute(ctx context.Context, cmd wire.Command) wire.CommandResult {
	start := r.now()
	status, output, payload := r.dispatchWithPayload(ctx, cmd)
	res := wire.CommandResult{
		ID:         cmd.ID,
		Status:     status,
		Output:     output,
		DurationMs: r.now().Sub(start).Milliseconds(),
	}
	if !payload.IsZero() {
		if b, err := json.Marshal(payload); err == nil {
			res.Payload = b
		}
	}
	return res
}
```

Rename `dispatch` → `dispatchWithPayload`, add `payload wire.OpkgUpgradeResult` as third named return, and append `, payload` to every existing `return` statement in the switch (each existing case returns zero payload).

The `opkg_upgrade` case returns the real payload from SmartUpgrade:

```go
	case "opkg_upgrade":
		if r.Opkg == nil {
			return "err", "opkg runner not configured", payload
		}
		s, o, p := r.Opkg.SmartUpgrade(ctx)
		return s, o, p
```

Update the `OpkgExecutor` interface (around line 38):

```go
type OpkgExecutor interface {
	DryRun(ctx context.Context) (status, output string)
	SmartUpgrade(ctx context.Context) (status, output string, payload wire.OpkgUpgradeResult)
}
```

- [ ] **Step 6: Run all agent action tests**

Run: `go test ./internal/agent/actions/ -v`
Expected: PASS. If any existing test in `runner_test.go` calls `r.Opkg.SmartUpgrade` directly, update the mock implementation to match the new signature (add a zero-value payload return).

Run: `go build ./...`
Expected: build succeeds project-wide.

- [ ] **Step 7: Commit**

```bash
git add pkg/wire/types.go pkg/wire/types_test.go internal/agent/actions/runner.go internal/agent/actions/runner_test.go
git commit -m "feat(wire): CommandResult.Payload + OpkgUpgradeResult + opkg_feed_disable action"
```

---

## Task 5: `normalizeFeedURL` helper

**Files:**
- Modify: `internal/agent/actions/opkg.go`
- Test: `internal/agent/actions/opkg_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `opkg_test.go`:

```go
func TestNormalizeFeedURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz", "https://anonym-tsk.github.io/nfqws-keenetic/all"},
		{"https://anonym-tsk.github.io/nfqws-keenetic/all/", "https://anonym-tsk.github.io/nfqws-keenetic/all"},
		{"https://anonym-tsk.github.io/nfqws-keenetic/all", "https://anonym-tsk.github.io/nfqws-keenetic/all"},
		{"http://bin.entware.net/aarch64-k3.10", "http://bin.entware.net/aarch64-k3.10"},
		{"https://x.example/Packages.gz/Packages.gz", "https://x.example/Packages.gz"},
	}
	for _, c := range cases {
		got := normalizeFeedURL(c.in)
		if got != c.want {
			t.Errorf("normalizeFeedURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/actions/ -run TestNormalizeFeedURL -v`
Expected: FAIL — `undefined: normalizeFeedURL`.

- [ ] **Step 3: Implement**

Append to `opkg.go` (just above `humanKB`):

```go
// normalizeFeedURL strips a trailing `/Packages.gz` (once) and any trailing
// `/`, returning the base URL form that appears in opkg config files
// (`src/gz <name> <base-url>`).
func normalizeFeedURL(u string) string {
	u = strings.TrimSuffix(u, "/Packages.gz")
	u = strings.TrimRight(u, "/")
	return u
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/actions/ -run TestNormalizeFeedURL -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/actions/opkg.go internal/agent/actions/opkg_test.go
git commit -m "feat(agent): normalizeFeedURL — strip Packages.gz + trailing slash"
```

---

## Task 6: `disableMatchingLine` helper

**Files:**
- Modify: `internal/agent/actions/opkg.go`
- Test: `internal/agent/actions/opkg_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `opkg_test.go`:

```go
func TestDisableMatchingLine_SimpleMatch(t *testing.T) {
	body := []byte("src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	url := "https://anonym-tsk.github.io/nfqws-keenetic/all"
	out, hit := disableMatchingLine(body, url, "2026-05-12T10:00:00Z")
	if !hit {
		t.Fatalf("expected hit")
	}
	want := "# disabled by wg-monitor 2026-05-12T10:00:00Z: src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n"
	if string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestDisableMatchingLine_MultiFeed_OnlyTargetCommented(t *testing.T) {
	body := []byte("src/gz entware http://bin.entware.net/aarch64-k3.10\n" +
		"src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n" +
		"src/gz hoaxisr http://repo.hoaxisr.ru/aarch64-k3.10\n")
	url := "https://anonym-tsk.github.io/nfqws-keenetic/all"
	out, hit := disableMatchingLine(body, url, "T")
	if !hit {
		t.Fatalf("expected hit")
	}
	s := string(out)
	if !strings.Contains(s, "src/gz entware http://bin.entware.net/aarch64-k3.10\n") {
		t.Errorf("entware line should be untouched, got %q", s)
	}
	if !strings.Contains(s, "# disabled by wg-monitor T: src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n") {
		t.Errorf("nfqws line should be commented, got %q", s)
	}
	if !strings.Contains(s, "src/gz hoaxisr http://repo.hoaxisr.ru/aarch64-k3.10\n") {
		t.Errorf("hoaxisr line should be untouched, got %q", s)
	}
}

func TestDisableMatchingLine_SkipsAlreadyCommented(t *testing.T) {
	body := []byte("# src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	out, hit := disableMatchingLine(body, "https://anonym-tsk.github.io/nfqws-keenetic/all", "T")
	if hit {
		t.Errorf("commented line must not be re-disabled")
	}
	if string(out) != string(body) {
		t.Errorf("body must be unchanged, got %q", out)
	}
}

func TestDisableMatchingLine_NoMatch(t *testing.T) {
	body := []byte("src/gz entware http://bin.entware.net/aarch64-k3.10\n")
	out, hit := disableMatchingLine(body, "https://anonym-tsk.github.io/nfqws-keenetic/all", "T")
	if hit {
		t.Errorf("should not match")
	}
	if string(out) != string(body) {
		t.Errorf("body must be unchanged")
	}
}

func TestDisableMatchingLine_SrcWithoutGz(t *testing.T) {
	// Some feeds use `src` (no /gz) for uncompressed Packages files.
	body := []byte("src nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	out, hit := disableMatchingLine(body, "https://anonym-tsk.github.io/nfqws-keenetic/all", "T")
	if !hit {
		t.Fatalf("expected hit on `src` variant")
	}
	if !strings.HasPrefix(string(out), "# disabled by wg-monitor T:") {
		t.Errorf("expected comment prefix, got %q", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/actions/ -run TestDisableMatchingLine -v`
Expected: FAIL — `undefined: disableMatchingLine`.

- [ ] **Step 3: Implement**

Append to `opkg.go` (just above `humanKB`):

```go
// disableMatchingLine scans `body` line-by-line, commenting out any
// uncommented `src` or `src/gz` line whose URL (third whitespace-separated
// field) is equal to or a path-prefix of normalizedURL. Returns the rewritten
// body and true iff at least one line was modified. Already-commented lines
// (leading `#`) are never re-touched.
//
// stamp is the timestamp string baked into the comment prefix; the caller
// owns time-source choice for deterministic tests.
func disableMatchingLine(body []byte, normalizedURL, stamp string) ([]byte, bool) {
	lines := strings.SplitAfter(string(body), "\n")
	hit := false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}
		if fields[0] != "src" && fields[0] != "src/gz" {
			continue
		}
		lineURL := strings.TrimRight(fields[2], "/")
		if lineURL != normalizedURL {
			continue
		}
		// Preserve the original line as-is in the suffix so reverting is
		// just `sed -i 's|^# disabled by wg-monitor [^:]*: ||'`.
		lines[i] = "# disabled by wg-monitor " + stamp + ": " + line
		hit = true
	}
	if !hit {
		return body, false
	}
	return []byte(strings.Join(lines, "")), true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/actions/ -run TestDisableMatchingLine -v`
Expected: PASS, five subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/actions/opkg.go internal/agent/actions/opkg_test.go
git commit -m "feat(agent): disableMatchingLine — comment out src/gz line by URL"
```

---

## Task 7: `OpkgRunner.DisableFeed` full action

**Files:**
- Modify: `internal/agent/actions/opkg.go`
- Test: `internal/agent/actions/opkg_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `opkg_test.go`:

```go
// mkOpkgRunnerWithRoot is a variant of mkOpkgRunner that points ConfigRoot at
// a temp dir holding an opkg.conf and/or opkg/<feed>.conf files. Used by
// DisableFeed tests.
func mkOpkgRunnerWithRoot(t *testing.T, root string, exec ExecFunc) *OpkgRunner {
	t.Helper()
	r := mkOpkgRunner(t, exec)
	r.ConfigRoot = root
	return r
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpkg_DisableFeed_PerFeedFile(t *testing.T) {
	root := t.TempDir()
	confPath := filepath.Join(root, "opkg", "nfqws.conf")
	writeFile(t, confPath, "src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	writeFile(t, filepath.Join(root, "opkg.conf"), "")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "update" {
			return []byte("Updated list of available packages in /opt/var/opkg-lists/x\n"), nil
		}
		if args[0] == "list-upgradable" {
			return []byte(""), nil
		}
		return nil, nil
	})

	status, output, _ := o.DisableFeed(context.Background(), "https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz")
	if status != "ok" {
		t.Fatalf("status=%q output=%q", status, output)
	}
	body, _ := os.ReadFile(confPath)
	if !strings.Contains(string(body), "# disabled by wg-monitor") {
		t.Errorf("expected comment line in %s, got %q", confPath, body)
	}
	// One .bak.* file alongside.
	matches, _ := filepath.Glob(confPath + ".bak.*")
	if len(matches) != 1 {
		t.Errorf("expected 1 backup file, got %v", matches)
	}
}

func TestOpkg_DisableFeed_MultiFeedFile(t *testing.T) {
	root := t.TempDir()
	confPath := filepath.Join(root, "opkg.conf")
	writeFile(t, confPath, "src/gz entware http://bin.entware.net/aarch64-k3.10\n"+
		"src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n"+
		"src/gz hoaxisr http://repo.hoaxisr.ru/aarch64-k3.10\n")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "update" {
			return []byte("Updated list of available packages in /opt/var/opkg-lists/x\n"), nil
		}
		return nil, nil
	})

	status, _, _ := o.DisableFeed(context.Background(), "https://anonym-tsk.github.io/nfqws-keenetic/all")
	if status != "ok" {
		t.Fatalf("status=%q", status)
	}
	body, _ := os.ReadFile(confPath)
	s := string(body)
	if !strings.Contains(s, "src/gz entware http://bin.entware.net/aarch64-k3.10\n") {
		t.Errorf("entware untouched: %q", s)
	}
	if !strings.Contains(s, "# disabled by wg-monitor") || !strings.Contains(s, "src/gz nfqws") {
		t.Errorf("nfqws not commented: %q", s)
	}
}

func TestOpkg_DisableFeed_Idempotent(t *testing.T) {
	root := t.TempDir()
	confPath := filepath.Join(root, "opkg", "nfqws.conf")
	writeFile(t, confPath, "# src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	writeFile(t, filepath.Join(root, "opkg.conf"), "")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil // not used — DisableFeed returns early on no-match
	})

	status, output, _ := o.DisableFeed(context.Background(), "https://anonym-tsk.github.io/nfqws-keenetic/all")
	if status != "ok" {
		t.Errorf("status=%q output=%q (idempotent should be ok)", status, output)
	}
	if !strings.Contains(output, "уже отключён") && !strings.Contains(output, "не найден") {
		t.Errorf("output should explain no-op, got %q", output)
	}
	matches, _ := filepath.Glob(confPath + ".bak.*")
	if len(matches) != 0 {
		t.Errorf("no backup should be created on no-op, got %v", matches)
	}
}

func TestOpkg_DisableFeed_NotFound(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "opkg.conf"), "src/gz entware http://bin.entware.net/aarch64-k3.10\n")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	})

	status, _, _ := o.DisableFeed(context.Background(), "https://nowhere.example/Packages.gz")
	if status != "ok" {
		t.Errorf("status=%q, want ok (no-op)", status)
	}
}

func TestOpkg_DisableFeed_InvalidURL(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "opkg.conf"), "")
	called := 0
	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		called++
		return nil, nil
	})
	for _, bad := range []string{"", "ftp://x", "javascript:alert(1)", "/etc/passwd"} {
		status, _, _ := o.DisableFeed(context.Background(), bad)
		if status != "err" {
			t.Errorf("DisableFeed(%q) status=%q, want err", bad, status)
		}
	}
	if called != 0 {
		t.Errorf("DisableFeed should not invoke exec for invalid URLs; called=%d", called)
	}
}

func TestOpkg_DisableFeed_ThenSmartUpgrade(t *testing.T) {
	root := t.TempDir()
	confPath := filepath.Join(root, "opkg.conf")
	writeFile(t, confPath, "src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "update":
			return []byte("Updated list of available packages in /opt/var/opkg-lists/entware\n"), nil
		case "list-upgradable":
			return []byte(""), nil
		}
		return nil, nil
	})

	status, output, payload := o.DisableFeed(context.Background(), "https://anonym-tsk.github.io/nfqws-keenetic/all")
	if status != "ok" {
		t.Fatalf("status=%q", status)
	}
	if !strings.Contains(output, "🔧 Отключён фид") {
		t.Errorf("combined output should start with disable header, got %q", output)
	}
	if !strings.Contains(output, "Все пакеты актуальны") {
		t.Errorf("combined output should include SmartUpgrade body, got %q", output)
	}
	if len(payload.FailedFeeds) != 0 {
		t.Errorf("payload.FailedFeeds should be empty after repair; got %v", payload.FailedFeeds)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/actions/ -run TestOpkg_DisableFeed -v`
Expected: FAIL — `o.DisableFeed undefined`.

- [ ] **Step 3: Implement `DisableFeed` + `backupAndWrite`**

Append to `internal/agent/actions/opkg.go`:

```go
// DisableFeed comments out the line in opkg config matching the given feed
// URL, writes a timestamped backup of the modified file, then re-runs
// SmartUpgrade so the user sees a single clean report.
//
// Behavior table:
//   - Invalid URL (not http/https): status="err", no FS access.
//   - URL not present in any conf: status="ok", "уже отключён или не найден", no FS writes.
//   - URL matched and commented: status from SmartUpgrade re-run (typically "ok").
//   - Lock held by concurrent op: status="locked".
//
// The opkg lock-file is held during the disable phase, released before the
// SmartUpgrade re-run (which takes its own lock). This means a concurrent
// SmartUpgrade can squeeze in between, which is acceptable: at worst the
// user sees two upgrade reports in quick succession.
func (o *OpkgRunner) DisableFeed(ctx context.Context, rawURL string) (status, output string, payload wire.OpkgUpgradeResult) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "err", "invalid feed URL: " + rawURL, payload
	}
	url := normalizeFeedURL(rawURL)

	if held, age, ok := o.lockHeldFresh(); ok {
		return "locked", fmt.Sprintf("opkg lock held by another op (age %v, lock file: %s)", age.Round(time.Second), held), payload
	}
	if err := o.takeLock(); err != nil {
		return "err", "acquire lock: " + err.Error(), payload
	}

	stamp := o.now().UTC().Format(time.RFC3339)
	backupSuffix := o.now().UTC().Format("20060102-150405")

	var changedFiles []string
	for _, path := range o.opkgConfPaths() {
		body, err := os.ReadFile(path)
		if err != nil {
			// Missing files are normal (opkg.conf may not exist, etc.). Permission
			// or other read errors are reported.
			if os.IsNotExist(err) {
				continue
			}
			o.releaseLock()
			return "err", fmt.Sprintf("read %s: %v", path, err), payload
		}
		newBody, hit := disableMatchingLine(body, url, stamp)
		if !hit {
			continue
		}
		if err := backupAndWrite(path, body, newBody, backupSuffix); err != nil {
			o.releaseLock()
			return "err", fmt.Sprintf("rewrite %s: %v", path, err), payload
		}
		changedFiles = append(changedFiles, path)
	}

	o.releaseLock()

	if len(changedFiles) == 0 {
		return "ok", fmt.Sprintf("🔧 Фид %s уже отключён или не найден в opkg-конфигах.", url), payload
	}

	prefix := fmt.Sprintf("🔧 Отключён фид %s в %s (backup suffix: %s)\n\n",
		url, strings.Join(changedFiles, ", "), backupSuffix)

	s, smartOut, p := o.SmartUpgrade(ctx)
	combined := prefix + smartOut
	p.Output = combined
	return s, combined, p
}

// backupAndWrite writes oldBody to <path>.bak.<suffix>, then atomically
// replaces <path> with newBody. Atomicity via tmp-file + Rename ensures no
// half-written config is ever visible to opkg.
func backupAndWrite(path string, oldBody, newBody []byte, suffix string) error {
	bakPath := path + ".bak." + suffix
	if err := os.WriteFile(bakPath, oldBody, 0o644); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	tmpPath := path + ".tmp." + suffix
	if err := os.WriteFile(tmpPath, newBody, 0o644); err != nil {
		return fmt.Errorf("tmp write: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/actions/ -run TestOpkg_DisableFeed -v`
Expected: PASS, six subtests.

Run: `go test ./internal/agent/actions/ -v` — full action package suite still green.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/actions/opkg.go internal/agent/actions/opkg_test.go
git commit -m "feat(agent): OpkgRunner.DisableFeed — comment out feed + auto-retry"
```

---

## Task 8: Runner dispatch for `opkg_feed_disable`

**Files:**
- Modify: `internal/agent/actions/runner.go`
- Test: `internal/agent/actions/runner_test.go`

- [ ] **Step 1: Extend `OpkgExecutor` interface**

In `internal/agent/actions/runner.go`, update the interface:

```go
type OpkgExecutor interface {
	DryRun(ctx context.Context) (status, output string)
	SmartUpgrade(ctx context.Context) (status, output string, payload wire.OpkgUpgradeResult)
	DisableFeed(ctx context.Context, url string) (status, output string, payload wire.OpkgUpgradeResult)
}
```

(`OpkgRunner` already implements `DisableFeed` from Task 7.)

- [ ] **Step 2: Write the failing test**

Find an existing dispatch test in `runner_test.go` and add a new test next to it (following the same fake-executor pattern):

```go
func TestRunner_OpkgFeedDisable_Dispatch(t *testing.T) {
	r := &Runner{
		Opkg: &fakeOpkg{
			disableFn: func(ctx context.Context, url string) (string, string, wire.OpkgUpgradeResult) {
				if url != "https://x/Packages.gz" {
					t.Errorf("url=%q", url)
				}
				return "ok", "🔧 Отключён", wire.OpkgUpgradeResult{Output: "🔧 Отключён"}
			},
		},
		Now: time.Now,
	}
	cmd := wire.Command{
		ID: "c1", Action: "opkg_feed_disable",
		Args: map[string]any{"url": "https://x/Packages.gz"},
	}
	res := r.Execute(context.Background(), cmd)
	if res.Status != "ok" {
		t.Errorf("status=%q", res.Status)
	}
	if string(res.Payload) == "" {
		t.Errorf("payload should be set")
	}
}

func TestRunner_OpkgFeedDisable_MissingURL(t *testing.T) {
	r := &Runner{Opkg: &fakeOpkg{}, Now: time.Now}
	cmd := wire.Command{ID: "c1", Action: "opkg_feed_disable", Args: map[string]any{}}
	res := r.Execute(context.Background(), cmd)
	if res.Status != "err" {
		t.Errorf("status=%q, want err", res.Status)
	}
	if !strings.Contains(res.Output, "url") {
		t.Errorf("output should mention missing url: %q", res.Output)
	}
}
```

If `fakeOpkg` doesn't exist, add at top of `runner_test.go`:

```go
type fakeOpkg struct {
	smartFn   func(ctx context.Context) (string, string, wire.OpkgUpgradeResult)
	disableFn func(ctx context.Context, url string) (string, string, wire.OpkgUpgradeResult)
}

func (f *fakeOpkg) DryRun(ctx context.Context) (string, string) { return "ok", "" }
func (f *fakeOpkg) SmartUpgrade(ctx context.Context) (string, string, wire.OpkgUpgradeResult) {
	if f.smartFn != nil {
		return f.smartFn(ctx)
	}
	return "ok", "", wire.OpkgUpgradeResult{}
}
func (f *fakeOpkg) DisableFeed(ctx context.Context, url string) (string, string, wire.OpkgUpgradeResult) {
	if f.disableFn != nil {
		return f.disableFn(ctx, url)
	}
	return "ok", "", wire.OpkgUpgradeResult{}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/agent/actions/ -run TestRunner_OpkgFeedDisable -v`
Expected: FAIL — `opkg_feed_disable` falls through to the default `"unknown action"` case.

- [ ] **Step 4: Add dispatch case**

In `runner.go`, in the `dispatchWithPayload` switch, add (immediately after the `opkg_upgrade` case):

```go
	case "opkg_feed_disable":
		if r.Opkg == nil {
			return "err", "opkg runner not configured", payload
		}
		url, _ := cmd.Args["url"].(string)
		if url == "" {
			return "err", "opkg_feed_disable: url is required", payload
		}
		s, o, p := r.Opkg.DisableFeed(ctx, url)
		return s, o, p
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/actions/ -v`
Expected: full action package green.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/actions/runner.go internal/agent/actions/runner_test.go
git commit -m "feat(agent): runner dispatch for opkg_feed_disable"
```

---

## Task 9: Callback grammar `opkg_disable:<uid>:_menu:<token>`

**Files:**
- Modify: `internal/backend/callbacks/parse.go`
- Test: `internal/backend/callbacks/parse_test.go`

Rationale on shape: `Parse` enforces `len(parts) >= 3` and uses `parts[2]` as `CheckName`. Following the convention used by `opkg_upgrade` (which is dispatched from the persistent control panel with `_menu` filler), pack as `opkg_disable:<uid>:_menu:<token>`. Parser strips the suffix and treats the action as a 4-part shape.

- [ ] **Step 1: Write the failing tests**

Append to `internal/backend/callbacks/parse_test.go`:

```go
func TestParse_OpkgDisable_Valid(t *testing.T) {
	a, err := Parse("opkg_disable:12345:_menu:abcd1234")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.Action != "opkg_disable" {
		t.Errorf("Action=%q", a.Action)
	}
	if a.UserID != 12345 {
		t.Errorf("UserID=%d", a.UserID)
	}
	if a.OpkgRepairToken != "abcd1234" {
		t.Errorf("OpkgRepairToken=%q", a.OpkgRepairToken)
	}
}

func TestParse_OpkgDisable_MissingToken(t *testing.T) {
	_, err := Parse("opkg_disable:12345:_menu:")
	if err == nil {
		t.Error("expected error for empty token")
	}
}

func TestParse_OpkgDisable_NoTokenSegment(t *testing.T) {
	_, err := Parse("opkg_disable:12345:_menu")
	if err == nil {
		t.Error("expected error for missing token segment")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backend/callbacks/ -run TestParse_OpkgDisable -v`
Expected: FAIL — `opkg_disable` not in `validActions`, `OpkgRepairToken` field undefined.

- [ ] **Step 3: Implement**

In `internal/backend/callbacks/parse.go`:

Add field to `Args` struct (anywhere; near other `*Token` fields):

```go
	// OpkgRepairToken is the 8-hex confirm token for opkg_disable callbacks
	// originating from the "🔧 Отключить мёртвый фид" inline button.
	OpkgRepairToken string
```

Add `"opkg_disable": true` to the `validActions` map (alphabetical placement OK; add near `opkg_upgrade`).

Add a parse case in the `switch action` block (near `maint_confirm`):

```go
	case "opkg_disable":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("opkg_disable requires token: %q", data)
		}
		a.OpkgRepairToken = parts[3]
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backend/callbacks/ -run TestParse_OpkgDisable -v`
Expected: PASS, three tests.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/parse.go internal/backend/callbacks/parse_test.go
git commit -m "feat(callbacks): parse opkg_disable callback grammar"
```

---

## Task 10: `pendingOpkgRepairStore` and helpers

**Files:**
- Create: `internal/backend/callbacks/opkg_repair.go`
- Create: `internal/backend/callbacks/opkg_repair_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/backend/callbacks/opkg_repair_test.go`:

```go
package callbacks

import (
	"testing"
	"time"
)

func TestPendingOpkgRepair_PutConsume(t *testing.T) {
	s := newPendingOpkgRepairStore()
	p := &pendingOpkgRepair{
		UserID:    42,
		URL:       "https://x/Packages.gz",
		Token:     "tok1",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	s.put(p)
	got, ok := s.consume(42, "tok1")
	if !ok {
		t.Fatal("consume should succeed")
	}
	if got.URL != "https://x/Packages.gz" {
		t.Errorf("URL=%q", got.URL)
	}
	if _, ok := s.consume(42, "tok1"); ok {
		t.Error("second consume should fail (single-use)")
	}
}

func TestPendingOpkgRepair_WrongUser(t *testing.T) {
	s := newPendingOpkgRepairStore()
	s.put(&pendingOpkgRepair{
		UserID: 42, URL: "u", Token: "t",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if _, ok := s.consume(99, "t"); ok {
		t.Error("wrong user must not consume")
	}
	// Token must remain for the rightful owner.
	if _, ok := s.consume(42, "t"); !ok {
		t.Error("owner must still be able to consume")
	}
}

func TestPendingOpkgRepair_Expired(t *testing.T) {
	s := newPendingOpkgRepairStore()
	s.put(&pendingOpkgRepair{
		UserID: 42, URL: "u", Token: "t",
		ExpiresAt: time.Now().Add(-time.Minute), // already expired
	})
	if _, ok := s.consume(42, "t"); ok {
		t.Error("expired must not consume")
	}
	// Expired entries get evicted on consume attempt.
	s.mu.Lock()
	_, present := s.m["t"]
	s.mu.Unlock()
	if present {
		t.Error("expired entry should have been evicted")
	}
}

func TestMakeOpkgRepairToken_Format(t *testing.T) {
	tok := makeOpkgRepairToken()
	if len(tok) != 8 {
		t.Errorf("token len=%d, want 8", len(tok))
	}
	for _, r := range tok {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex char %q in token %q", r, tok)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backend/callbacks/ -run "TestPendingOpkgRepair|TestMakeOpkgRepairToken" -v`
Expected: FAIL — file doesn't exist.

- [ ] **Step 3: Implement**

Create `internal/backend/callbacks/opkg_repair.go`:

```go
package callbacks

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// pendingOpkgRepair is one queued repair request. Created when the backend
// renders a 🔧 button under an opkg_upgrade message; consumed when the user
// taps within the TTL. Single use — replay impossible.
type pendingOpkgRepair struct {
	UserID    int64
	URL       string // already normalised (no /Packages.gz suffix)
	Token     string // 8 hex chars
	ExpiresAt time.Time
}

// pendingOpkgRepairStore is a goroutine-safe map of token → pendingOpkgRepair.
// Lifetimes and consume semantics mirror pendingMaintStore (see maint.go).
type pendingOpkgRepairStore struct {
	mu sync.Mutex
	m  map[string]*pendingOpkgRepair
}

func newPendingOpkgRepairStore() *pendingOpkgRepairStore {
	return &pendingOpkgRepairStore{m: make(map[string]*pendingOpkgRepair)}
}

func (s *pendingOpkgRepairStore) put(p *pendingOpkgRepair) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[p.Token] = p
}

// consume atomically removes-and-returns the entry iff userID matches and the
// entry is unexpired. On UserID mismatch the entry stays — protects against
// a chat member tapping someone else's button.
func (s *pendingOpkgRepairStore) consume(userID int64, token string) (*pendingOpkgRepair, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[token]
	if !ok {
		return nil, false
	}
	if p.UserID != userID {
		return nil, false
	}
	if time.Now().After(p.ExpiresAt) {
		delete(s.m, token)
		return nil, false
	}
	delete(s.m, token)
	return p, true
}

// makeOpkgRepairToken returns 8 lowercase hex chars from crypto/rand.
func makeOpkgRepairToken() string {
	var b [4]byte
	_, _ = cryptoRand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// OpkgRepairAction implements the Action interface for opkg_disable callbacks.
// It consumes the pendingOpkgRepair by token and enqueues an
// opkg_feed_disable wire.Command for the agent.
type OpkgRepairAction struct {
	sink  CommandEnqueuer
	store *pendingOpkgRepairStore
	idGen func() string
}

func NewOpkgRepairAction(sink CommandEnqueuer, store *pendingOpkgRepairStore, idGen func() string) *OpkgRepairAction {
	if idGen == nil {
		idGen = defaultCmdID
	}
	return &OpkgRepairAction{sink: sink, store: store, idGen: idGen}
}

func (a *OpkgRepairAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	if a.sink == nil {
		return "", errors.New("command channel disabled (no sink configured)")
	}
	p, ok := a.store.consume(args.UserID, args.OpkgRepairToken)
	if !ok {
		return "", errors.New("сессия истекла или не найдена; запусти проверку обновлений заново")
	}
	cmd := wire.Command{
		ID:     a.idGen(),
		Action: "opkg_feed_disable",
		Args:   map[string]any{"url": p.URL},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
	}
	if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
		return "", fmt.Errorf("enqueue opkg_feed_disable: %w", err)
	}
	return "🔧 отключаем фид…", nil
}

var _ Action = (*OpkgRepairAction)(nil)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backend/callbacks/ -run "TestPendingOpkgRepair|TestMakeOpkgRepairToken" -v`
Expected: PASS, four tests.

Also run full package: `go test ./internal/backend/callbacks/ -v` — should remain green.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/opkg_repair.go internal/backend/callbacks/opkg_repair_test.go
git commit -m "feat(callbacks): pendingOpkgRepair store + OpkgRepairAction"
```

---

## Task 11: Wire `OpkgRepairAction` into `Router`

**Files:**
- Modify: `internal/backend/callbacks/router.go`
- Test: `internal/backend/callbacks/router_test.go`

- [ ] **Step 1: Write the failing test**

Find existing tests that exercise `handleCallback` (search for `TestRouter_` or `TestHandleCallback`). Append a new test that:
1. Builds a router with a fake `CommandEnqueuer`.
2. Calls a new `Router.SetOpkgRepair(store, action)` setter (to be added).
3. Pre-populates `store` with a pending entry.
4. Drives a callback `opkg_disable:42:_menu:tok1`.
5. Asserts the enqueuer captured an `opkg_feed_disable` command with the expected URL.

```go
func TestRouter_OpkgDisable_DispatchesEnqueue(t *testing.T) {
	d := newTestDB(t)  // existing helper in this package
	sink := &fakeEnqueuer{}
	r := NewRouterWithSink(d, &fakeTG{}, sink, Config{ChatID: 100, AdminUserID: 42})

	store := newPendingOpkgRepairStore()
	store.put(&pendingOpkgRepair{
		UserID:    42,
		URL:       "https://anonym-tsk.github.io/nfqws-keenetic/all",
		Token:     "tok1",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	r.SetOpkgRepair(store, NewOpkgRepairAction(sink, store, func() string { return "cmd-1" }))

	cb := &tg.CallbackQuery{
		ID:   "q1",
		From: &tg.User{ID: 42},
		Data: "opkg_disable:42:_menu:tok1",
		Message: &tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 555},
	}
	r.handleCallback(context.Background(), cb)

	if len(sink.captured) != 1 {
		t.Fatalf("captured %d cmds, want 1", len(sink.captured))
	}
	got := sink.captured[0]
	if got.cmd.Action != "opkg_feed_disable" {
		t.Errorf("Action=%q", got.cmd.Action)
	}
	if got.cmd.Args["url"] != "https://anonym-tsk.github.io/nfqws-keenetic/all" {
		t.Errorf("Args[url]=%v", got.cmd.Args["url"])
	}
}
```

If `fakeEnqueuer` / `fakeTG` / `newTestDB` don't already exist in `router_test.go`, find the actual existing test helper and pattern-match — do **not** invent new ones.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/callbacks/ -run TestRouter_OpkgDisable -v`
Expected: FAIL — `Router.SetOpkgRepair` undefined, `handleCallback` has no opkg_disable branch.

- [ ] **Step 3: Add Router field + setter + dispatch case**

In `internal/backend/callbacks/router.go`:

Add two fields to the `Router` struct (near other pending* fields):

```go
	// OPKG feed repair plumbing — populated by SetOpkgRepair from cmd/backend.
	pendingOpkgRepair *pendingOpkgRepairStore
	opkgRepairAction  Action
```

Add setter (next to `SetRoutesCache`):

```go
// SetOpkgRepair attaches the pendingOpkgRepair store and the OpkgRepairAction
// handler. Called from cmd/backend at startup; both must be wired together
// because handler relay in backend/handler.go creates pending entries and the
// action consumes them.
func (r *Router) SetOpkgRepair(store *pendingOpkgRepairStore, action Action) {
	r.pendingOpkgRepair = store
	r.opkgRepairAction = action
}

// OpkgRepairStore exposes the store for the backend handler relay path,
// which needs to register pending entries when rendering 🔧 buttons.
func (r *Router) OpkgRepairStore() *pendingOpkgRepairStore {
	return r.pendingOpkgRepair
}
```

Find `handleCallback` (a long method dispatching on `args.Action`). Add a case before the `default`:

```go
		case "opkg_disable":
			if r.opkgRepairAction == nil {
				_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "ремонт фидов не настроен")
				return
			}
			status, err := r.opkgRepairAction.Apply(ctx, q, args)
			if err != nil {
				_ = r.tg.AnswerCallbackQuery(ctx, q.ID, err.Error())
				return
			}
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, status)
```

(Pattern-match this branch to how `maint_confirm` is handled in the same function — match the surrounding error/ack conventions exactly.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backend/callbacks/ -v`
Expected: PASS, including new test.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/router.go internal/backend/callbacks/router_test.go
git commit -m "feat(callbacks): wire OpkgRepairAction into Router dispatch"
```

---

## Task 12: Render 🔧 buttons in `opkg_upgrade` / `opkg_feed_disable` reports

**Files:**
- Modify: `internal/backend/handler.go` (`cmdResultHandler`)
- Test: `internal/backend/handler_test.go`

Goal: when a `CommandResult` for `opkg_upgrade` or `opkg_feed_disable` arrives and its `Payload` decodes to `OpkgUpgradeResult` with non-empty `FailedFeeds`, the backend's relay path attaches an inline keyboard (one button per URL) to the TG message it sends/edits.

Pre-step — read the existing relay implementation:

- [ ] **Pre-step: Locate relay code path**

Search for where `CommandResult` is converted into a TG message. Likely in `cmdResultHandler` or a helper called from it. Look for code that calls `tg.SendMessage` / `tg.EditMessageText` keyed on action name.

```bash
grep -n 'CommandResult' internal/backend/handler.go
grep -n 'opkg_upgrade' internal/backend/
```

Identify the function where the human-readable reply for `opkg_upgrade` is constructed today (probably `formatCommandResult` in `internal/backend/alerts/command_result.go`).

- [ ] **Step 1: Write the failing test**

In `internal/backend/handler_test.go` (or the existing format-tests file in `alerts/`), add a test that simulates a `CommandResult` with `Payload` carrying `FailedFeeds`. The test must produce an `InlineKeyboardMarkup` containing one button per URL, with `callback_data` matching the pattern `opkg_disable:<uid>:_menu:<8-hex>`.

```go
func TestRenderOpkgResult_WithFailedFeeds_AddsRepairButtons(t *testing.T) {
	store := callbacks.NewPendingOpkgRepairStoreForTest()  // see Task 12 Step 3
	payload, _ := json.Marshal(wire.OpkgUpgradeResult{
		FailedFeeds: []string{"https://dead.example/Packages.gz", "https://other.example/all/Packages.gz"},
	})
	res := wire.CommandResult{
		ID: "c1", Status: "ok",
		Output:  "✅ Все пакеты актуальны...",
		Payload: payload,
	}
	text, markup := renderOpkgUpgradeReply(res, 42, store, func() string { return "tok-fixed" })
	if !strings.Contains(text, "Все пакеты актуальны") {
		t.Errorf("text missing upgrade body: %q", text)
	}
	if markup == nil {
		t.Fatalf("markup should not be nil with FailedFeeds present")
	}
	if len(markup.InlineKeyboard) != 2 {
		t.Errorf("expected 2 rows (one per feed), got %d", len(markup.InlineKeyboard))
	}
	for _, row := range markup.InlineKeyboard {
		if len(row) != 1 {
			t.Errorf("row must have 1 button, got %d", len(row))
		}
		btn := row[0]
		if !strings.HasPrefix(btn.CallbackData, "opkg_disable:42:_menu:") {
			t.Errorf("callback_data=%q", btn.CallbackData)
		}
	}
}

func TestRenderOpkgResult_NoFailedFeeds_NoButtons(t *testing.T) {
	store := callbacks.NewPendingOpkgRepairStoreForTest()
	res := wire.CommandResult{ID: "c1", Status: "ok", Output: "✅ ok"}
	_, markup := renderOpkgUpgradeReply(res, 42, store, func() string { return "tok" })
	if markup != nil {
		t.Errorf("markup should be nil with no FailedFeeds")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backend/ -run TestRenderOpkgResult -v`
Expected: FAIL — `renderOpkgUpgradeReply` undefined, `NewPendingOpkgRepairStoreForTest` undefined.

- [ ] **Step 3: Add the renderer + test helper**

In `internal/backend/callbacks/opkg_repair.go`, append a test-only constructor:

```go
// NewPendingOpkgRepairStoreForTest is a constructor exported for use by the
// backend package's render tests. Regular production code uses
// newPendingOpkgRepairStore() from inside this package.
func NewPendingOpkgRepairStoreForTest() *pendingOpkgRepairStore {
	return newPendingOpkgRepairStore()
}

// PutForRender registers a pending entry from the backend handler relay path.
// Exported because the renderer lives in package `backend`, not `callbacks`.
func (s *pendingOpkgRepairStore) PutForRender(userID int64, url, token string, ttl time.Duration) {
	s.put(&pendingOpkgRepair{
		UserID:    userID,
		URL:       url,
		Token:     token,
		ExpiresAt: time.Now().Add(ttl),
	})
}
```

In `internal/backend/handler.go` (or a new sibling file `internal/backend/opkg_render.go`), add:

```go
import (
	"encoding/json"
	"fmt"
	"github.com/anex/wg-monitor/internal/backend/callbacks"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// renderOpkgUpgradeReply returns the message text and inline keyboard for an
// opkg_upgrade / opkg_feed_disable CommandResult. When the payload carries
// FailedFeeds, one inline button is appended per URL — tapping enqueues an
// opkg_feed_disable command for the agent (via OpkgRepairAction).
//
// store is the per-router pendingOpkgRepairStore; the renderer registers a
// new pending entry per URL with a 5-minute TTL. tokenGen is injectable so
// tests can pin a deterministic token.
func renderOpkgUpgradeReply(res wire.CommandResult, userID int64, store *callbacks.PendingOpkgRepairStore, tokenGen func() string) (string, *tg.InlineKeyboardMarkup) {
	text := res.Output
	if len(res.Payload) == 0 {
		return text, nil
	}
	var payload wire.OpkgUpgradeResult
	if err := json.Unmarshal(res.Payload, &payload); err != nil {
		return text, nil
	}
	if len(payload.FailedFeeds) == 0 {
		return text, nil
	}
	var rows [][]tg.InlineKeyboardButton
	for _, rawURL := range payload.FailedFeeds {
		token := tokenGen()
		store.PutForRender(userID, normalizeFeedURLBackend(rawURL), token, 5*time.Minute)
		host := hostFromURL(rawURL)
		btn := tg.InlineKeyboardButton{
			Text:         fmt.Sprintf("🔧 Отключить мёртвый фид (%s)", host),
			CallbackData: fmt.Sprintf("opkg_disable:%d:_menu:%s", userID, token),
		}
		rows = append(rows, []tg.InlineKeyboardButton{btn})
	}
	return text, &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// normalizeFeedURLBackend mirrors the agent's normalizeFeedURL — strips
// trailing /Packages.gz and /.
func normalizeFeedURLBackend(u string) string {
	const suffix = "/Packages.gz"
	if len(u) > len(suffix) && u[len(u)-len(suffix):] == suffix {
		u = u[:len(u)-len(suffix)]
	}
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}

// hostFromURL returns the bare host for the button label. Falls back to the
// raw URL truncated if parsing fails.
func hostFromURL(u string) string {
	// minimal manual extract — net/url import is fine if you prefer; here we
	// keep it dep-free for one-shot use.
	const schemeSep = "://"
	idx := indexOf(u, schemeSep)
	if idx < 0 {
		return truncate(u, 40)
	}
	rest := u[idx+len(schemeSep):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == ':' {
			return rest[:i]
		}
	}
	return rest
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
```

You'll also need to add a typed export of `pendingOpkgRepairStore` so `backend` can name it:

In `internal/backend/callbacks/opkg_repair.go`, add at end:

```go
// PendingOpkgRepairStore is the exported type alias used by package backend.
type PendingOpkgRepairStore = pendingOpkgRepairStore
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backend/ -run TestRenderOpkgResult -v`
Expected: PASS, two tests.

Run: `go build ./...`
Expected: build succeeds.

- [ ] **Step 5: Plug renderer into the actual CommandResult relay path**

Find the function in `handler.go` that today produces the text for `opkg_upgrade` results and sends/edits the TG message (likely calls `formatCommandResult` and `SendMessage` / `EditMessageText`). Replace the call site to use `renderOpkgUpgradeReply` instead when `res.Action` (or the dispatched action name available in scope) is `opkg_upgrade` or `opkg_feed_disable`.

Look for code shaped like:

```go
text := alerts.FormatCommandResult(res, ...)
err := h.tg.SendMessage(ctx, chatID, threadID, text, "", &msgID)
```

Wrap it:

```go
var markup *tg.InlineKeyboardMarkup
if res.Action == "opkg_upgrade" || res.Action == "opkg_feed_disable" {
    text, markup = renderOpkgUpgradeReply(res, userID, h.opkgRepairStore, makeOpkgRepairTokenForRender)
}
// ... then send with markup
```

Where `h.opkgRepairStore` is plumbed through from `cmd/backend/main.go` (Task 13).

Use `tg.EditMessageText` with markup when this is an in-place edit (`originRef` was set on Enqueue), else `SendMessage` with markup.

The exact wiring depends on the relay shape. Read the existing `cmdResultHandler` carefully before editing — patterns must match `RoutesPanelNotifier` / `MaintPanelNotifier` (find which one most closely models a "fresh send vs edit-in-place" decision and copy it).

- [ ] **Step 6: Add an integration-shaped test**

In `handler_test.go`, append a test that drives `cmdResultHandler` end-to-end with a fake TG and asserts the resulting `SendMessage` / `EditMessageText` call carries the right markup. Pattern-match existing handler tests (e.g. for `route_rebind` result handling).

- [ ] **Step 7: Run all backend tests**

Run: `go test ./... -v`
Expected: PASS, full project.

- [ ] **Step 8: Commit**

```bash
git add internal/backend/handler.go internal/backend/handler_test.go internal/backend/callbacks/opkg_repair.go
git commit -m "feat(backend): render 🔧 buttons under opkg-upgrade results with FailedFeeds"
```

---

## Task 13: Wire pendingOpkgRepairStore in `cmd/backend/main.go`

**Files:**
- Modify: `cmd/backend/main.go`

- [ ] **Step 1: Read the section that builds Router**

```bash
grep -n 'SetRoutesCache\|SetMaintAction\|RoutesCache' cmd/backend/main.go
```

Find where `RoutesCache` is constructed and passed via `r.SetRoutesCache(rc)`. Maintenance equivalent should be visible nearby.

- [ ] **Step 2: Construct and wire the store**

Inside `main()`, after the existing router/maintenance wiring (look for `r.SetMaintAction(...)` or equivalent), add:

```go
opkgRepairStore := callbacks.NewPendingOpkgRepairStoreForTest()  // same constructor exported in Task 12
opkgRepairAction := callbacks.NewOpkgRepairAction(cmdQueue, opkgRepairStore, nil)
r.SetOpkgRepair(opkgRepairStore, opkgRepairAction)
```

(`NewPendingOpkgRepairStoreForTest` is misnamed; it's also our production constructor. Rename to `NewPendingOpkgRepairStore` and update its single Task-12 caller. Done as a small inline fix.)

The handler relay code needs access to the store. Pass it into whatever struct currently holds `tg` / `db` (probably `Deps` or `Server`):

```go
// Where Deps / Server is built:
deps.OpkgRepairStore = opkgRepairStore
```

And in `handler.go`'s relay, dereference via `h.opkgRepairStore` (or `deps.OpkgRepairStore` if there's no `h` receiver).

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 4: Manual TG smoke test (optional — see Task 14 for full smoke)**

Spin up local backend with a test bot:
```bash
go run ./cmd/backend -config testdata/local-backend.yaml
```
Trigger `⬆ Обновить пакеты` from a topic. Inspect the TG message — if you have a real agent with a dead feed, the 🔧 button appears.

- [ ] **Step 5: Commit**

```bash
git add cmd/backend/main.go
git commit -m "feat(backend): wire pendingOpkgRepairStore + OpkgRepairAction in main"
```

---

## Task 14: Smoke test on testkeen

**Files:**
- None (manual test + documentation update).

- [ ] **Step 1: Build the RC tag**

Push the branch, let CI cut a new RC tag (`v0.11.0-rcN+1`). Or locally:

```bash
git tag -a v0.11.0-rcN -m "opkg feed repair"
git push origin v0.11.0-rcN
```

Wait for GitHub Actions to publish binaries.

- [ ] **Step 2: Deploy via wizard**

```bash
./wg-monitor-deploy update-backend
./wg-monitor-deploy update-agent --nickname testkeen
```

Verify versions match via `wizard status`.

- [ ] **Step 3: Reproduce the bug**

In TG, tap `⬆ Обновить пакеты` in testkeen's per-router topic.

Expect: bot reply contains
- `✅ Обновлено пакетов: N` (or `✅ Все пакеты актуальны`),
- `⚠️ Недоступные фиды:` with `https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz`,
- inline button `🔧 Отключить мёртвый фид (anonym-tsk.github.io)`.

- [ ] **Step 4: Tap the button**

Expect: TG callback toast `🔧 отключаем фид…`. After a few seconds the original message edits in place to the new clean report (no failed feeds, no buttons). Updates listing now shows pkgs from the four remaining feeds only.

- [ ] **Step 5: SSH-verify on the router**

```bash
ssh -p 222 root@192.168.31.1 'grep -n nfqws /opt/etc/opkg.conf /opt/etc/opkg/*.conf 2>/dev/null'
```

Expect:
- The previously-active `src/gz nfqws ...` line prefixed with `# disabled by wg-monitor <RFC3339>:`.
- A backup file `<path>.bak.<YYYYMMDDHHMMSS>` exists.

- [ ] **Step 6: Tap again (idempotency check)**

Trigger another `⬆ Обновить пакеты`. Expect: no failed feeds, no buttons (the line is now commented; opkg doesn't try to fetch it).

- [ ] **Step 7: Document**

Append a one-line bullet to `README.md` under existing feature list:
```
- Auto-detect dead opkg feeds + one-tap disable from Telegram.
```

Commit:
```bash
git add README.md
git commit -m "docs: README — opkg feed repair feature line"
```

---

## Self-Review

**Spec coverage check:**

| Spec section | Covered by task(s) |
|---|---|
| Partial-success tolerance in SmartUpgrade | Task 1, 3 |
| `OpkgFeedDisable` action constant | Task 4 |
| `OpkgUpgradeResult` payload struct | Task 4 |
| `DisableFeed` algorithm (normalize, scan, backup, atomic write, retry) | Tasks 5, 6, 7 |
| `parseOpkgUpdate` line scanning | Task 1 |
| Idempotency / not-found / invalid-URL handling | Task 7 |
| Skips already-commented lines | Task 6 |
| Runner dispatch case | Task 8 |
| Callback grammar `opkg|disable|<token>` | Task 9 (note: shape adapted to `opkg_disable:<uid>:_menu:<token>` to fit existing `Parse` 3-segment minimum) |
| `pendingOpkgRepair` store + token TTL | Task 10 |
| `OpkgRepairAction` | Task 10 |
| Router wiring | Task 11 |
| Inline button render on opkg result | Task 12 |
| Backend main wiring | Task 13 |
| ACL: user-bound consume | Task 10 (consume checks UserID) |
| Concurrency: file-lock gates SmartUpgrade + DisableFeed | Task 7 |
| Mixed-version compat (new agent / old backend and vice versa) | Implicit: omitempty Payload field + new action constant only appears in new code paths. No test covers this but it's a wire-stability property, not a feature. |
| Manual smoke test | Task 14 |

**Placeholder scan:** No "TBD" / "TODO" / "implement later" / vague "handle edge cases" remain. Each step has concrete code or commands.

**Type consistency:**
- `parseOpkgUpdate` returns `opkgUpdateOutcome{feedsUpdated int, failedFeeds []string}` — consistent across Tasks 1 and 3.
- `wire.OpkgUpgradeResult` has `Output string` and `FailedFeeds []string` — same in agent, wire, backend (Tasks 3, 4, 12).
- `SmartUpgrade(ctx) (status, output string, payload wire.OpkgUpgradeResult)` — same in opkg.go (Task 3), runner.go interface (Tasks 4, 8), fakeOpkg (Task 8).
- `DisableFeed(ctx, url string) (status, output string, payload wire.OpkgUpgradeResult)` — same shape in opkg.go (Task 7), runner.go interface (Task 8), fakeOpkg (Task 8).
- Callback shape: `opkg_disable:<uid>:_menu:<token>` is what Task 9 parses, Task 12 emits, Task 11 dispatches.
- Store API: `put` / `consume` / `PutForRender` (exported) — used consistently in Tasks 10, 11, 12.
- Token helper: `makeOpkgRepairToken()` returns 8 hex chars — verified by Task 10 test, generated in Task 12 (`tokenGen` injectable).
- Backup file shape: `<path>.bak.<YYYYMMDD-HHMMSS>` — verified in Task 7 tests with `filepath.Glob(confPath + ".bak.*")`.

**One refinement noted, not blocking:** Task 9 callback shape uses `_menu` filler segment to satisfy the existing `Parse` 3-segment minimum (and follows the same convention as `opkg_upgrade` from the control panel). The spec abstractly said `opkg|disable|<url-b64>` then narrowed to `opkg|disable|<token>` — the colon-separated shape in this plan is the concrete on-the-wire form within the existing parser's grammar. Same information, same security properties.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-05-12-opkg-feed-repair.md`. Two execution options:**

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
