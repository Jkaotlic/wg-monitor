# OPKG Feed Repair — Design

**Date:** 2026-05-12
**Status:** Draft
**Targets:** v0.11.0 (next RC)

## Summary

When `opkg update` fails because one of the configured package feeds returns
HTTP error (e.g. `wget returned 8` for a GitHub Pages source that was deleted
upstream), the agent must:

1. **Treat partial-success update as success.** If at least one feed downloaded
   its `Packages.gz`, the SmartUpgrade flow continues to `list-upgradable` /
   `upgrade` instead of bailing out. Feeds that failed are surfaced in the
   final report.
2. **Offer a one-tap repair button** in the Telegram message reporting the
   upgrade. Tapping `🔧 Отключить мёртвый фид` removes the offending feed line
   from the router's opkg config (commenting it out with a timestamped
   backup), then immediately re-runs the full SmartUpgrade so the user sees a
   single clean result.

Trigger reality: on testkeen the feed `https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz`
returns cached HTTP 404 — the upstream GitHub Pages site is gone. Current
agent reports `❌ Не удалось: opkg update failed: exit status 1`, hiding
the four feeds that actually updated and blocking all upgrades.

## Why now

- The flow has caused at least one user-visible failure on a real router
  already.
- The class of bug (dead third-party feed) is inevitable for a fleet that
  depends on community opkg sources (`hoaxisr`, `ground-zerro`, `nfqws`,
  ...) — any of them can disappear without notice.
- Repair via SSH is straightforward (`sed -i '|<url>|s|^|# |' /opt/etc/opkg/*.conf`)
  but operator-only; a TG button delegates this to anyone authorized for the
  router topic.

## Non-Goals

- **No "re-enable feed" button.** Backup files remain on disk; restoration is
  manual SSH for now. Re-enabling requires intent ("did the source come back?
  do I still want this package set?") that doesn't fit one tap.
- **No general feed management UI.** No list / add / rename feeds. Out of
  scope until needed.
- **No auto-disable on N consecutive failures.** Disabling a feed is a
  human-judgment call (maybe it's a transient outage); we surface it and
  let the operator decide.
- **No race-protection against external concurrent edits** of `opkg.conf`
  while we're rewriting it. Atomic write (`os.WriteFile` to temp + rename)
  minimizes the window; we don't try to lock against unrelated SSH sessions.
- **No retry/backoff for `opkg update`.** A repair is a single attempt — if
  the followup `opkg update` still fails (e.g. another dead feed), the user
  sees the new report with new buttons (one per remaining dead feed).

## Architecture

Reuses the established command-queue + edit-in-place pattern.

### Flow

```
1. User taps "⬆ Обновить пакеты"  (existing reply-keyboard button)
   → backend enqueues opkg_smart_upgrade for agent
   → agent runs SmartUpgrade
       * opkg update — captures stdout+exit code
       * parseOpkgUpdate(out) → {feedsUpdated: 4, failedFeeds: [<url>]}
       * if feedsUpdated == 0 → return status="err"
       * else continue with list-upgradable + upgrade
   → agent returns CommandResult with structured payload
       {Output: "...", FailedFeeds: ["<url>"]}
2. Backend handler sees CommandResult.FailedFeeds non-empty
   → for each URL, generate token, store in pendingOpkgRepair (TTL 5m)
   → render message with one inline button per URL
3. User taps "🔧 Отключить мёртвый фид"
   → callback opkg|disable|<token>
   → backend consumes token, enqueues opkg_feed_disable{url} for agent
   → agent runs DisableFeed:
       * normalize URL (strip /Packages.gz and trailing /)
       * take opkg lock
       * scan /opt/etc/opkg.conf and /opt/etc/opkg/*.conf
       * find uncommented src/gz <name> <url-prefix> line(s)
       * backup file, comment out match, write atomically
       * release lock
       * call SmartUpgrade(ctx) again (re-acquires lock)
   → agent returns CommandResult with both reports concatenated
4. Backend MaintPanelNotifier-style edit replaces original message in place
   with the new (now clean) report.
```

### New components

| Component | Location | Purpose |
|---|---|---|
| `parseOpkgUpdate` | `internal/agent/actions/opkg.go` | Parse `opkg update` stdout into `{feedsUpdated, failedFeeds}`. |
| `OpkgRunner.SmartUpgrade` (modified) | same | Tolerate partial-success update (≥1 feed). Append failed-feeds block to report. Return structured `OpkgUpgradeResult` alongside text. |
| `OpkgRunner.DisableFeed(ctx, url)` | same | Find & comment out src/gz line, backup, atomic write, then call SmartUpgrade. |
| `OpkgFeedDisable` action constant | `pkg/wire/types.go` | `"opkg_feed_disable"` + entry in `IsValidCommandAction`. |
| `OpkgUpgradeResult` payload | `pkg/wire/types.go` | `{Output string, FailedFeeds []string}` carried in `CommandResult.Payload`. |
| Runner dispatch case | `internal/agent/actions/runner.go` | Route `opkg_feed_disable` → `OpkgRunner.DisableFeed`. Shares `opkgMu` (or existing lock) with smart_upgrade. |
| Inline button renderer | `internal/backend/alerts/format.go` (or new helper in `callbacks/`) | Append `🔧 Отключить мёртвый фид (<host>)` button for each failed URL. |
| `pendingOpkgRepair` map | `internal/backend/callbacks/opkg_repair.go` (new) | `token → url` with 5-minute TTL. Same pattern as `pendingRebind` / `pendingMaint`. |
| Callback grammar | `internal/backend/callbacks/parse.go` | Parse `opkg|disable|<token>`. |
| Handler | `internal/backend/callbacks/router.go` | Consume token, enqueue `opkg_feed_disable` cmd, ACK to user. |
| Notifier dispatch | `internal/backend/handler.go` | Route `CommandResult` for `opkg_smart_upgrade` and `opkg_feed_disable` to the same edit-in-place renderer (whichever message ID is in flight). |

### Files modified, not added

- `internal/agent/actions/opkg.go` — partial-failure tolerance + DisableFeed.
- `internal/agent/actions/opkg_test.go` — new tests (see Testing).
- `internal/agent/actions/runner.go` — new dispatch case.
- `pkg/wire/types.go`, `pkg/wire/types_test.go` — action constant + payload struct.
- `internal/backend/callbacks/parse.go`, `parse_test.go` — `opkg|...` grammar.
- `internal/backend/callbacks/router.go`, `router_test.go` — handler.
- `internal/backend/handler.go`, `handler_test.go` — payload routing.
- `internal/backend/alerts/format.go` (or `command_result.go`) — render failed-feeds + buttons.

## Detailed design

### `parseOpkgUpdate`

```go
type opkgUpdateOutcome struct {
    feedsUpdated int
    failedFeeds  []string // URLs as printed by opkg
}
```

Iterates lines, recognizes two prefixes (case-sensitive — busybox `opkg`
prints them deterministically):

- `"Updated list of available packages in "` → `feedsUpdated++`
- `"*** Failed to download the package list from "` → append URL (trimmed)

`Collected errors:` block is ignored — same URLs appear there in a different
format and we'd double-count.

### `OpkgRunner.SmartUpgrade` tolerance change

```go
updateOut, updateErr := o.Exec(ctx, "opkg", "update")
upd := parseOpkgUpdate(string(updateOut))
if updateErr != nil && upd.feedsUpdated == 0 {
    return "err", "opkg update failed: " + updateErr.Error() + "\n" + string(updateOut)
}
// Partial-success continues. Failed feeds surface in the final report.
```

Return signature stays `(status, output string)` but **additionally writes**
the structured `wire.OpkgUpgradeResult{Output, FailedFeeds}` to a field on
`OpkgRunner` (or — preferred — change `SmartUpgrade` to return the result
struct directly and let `runner.go` flatten `output` for the human report
and pass the struct through `CommandResult.Payload`).

**Chosen:** change signature to `SmartUpgrade(ctx) (status string, result wire.OpkgUpgradeResult)`.
`result.Output` is the human report (same content as old `output`);
`result.FailedFeeds` is the structured list.

Final report (when partial-success and `pkgs` upgraded):

```
✅ Обновлено пакетов: N (~XX.X MB)
Список: pkg-a, pkg-b
Свободно после: XXX MB / YYY MB

⚠️ Недоступные фиды:
 • https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz

<opkg upgrade stdout, trimmed>
```

If no upgrades needed: `✅ Все пакеты актуальны — обновлять нечего.\n\n⚠️ Недоступные фиды:\n • ...`.

### `OpkgRunner.DisableFeed(ctx, rawURL string)`

Pseudocode:

```go
url := normalizeFeedURL(rawURL)  // strip trailing /Packages.gz and /
if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
    return "err", "invalid feed URL: " + rawURL, OpkgUpgradeResult{}
}

if locked { return "locked", ..., _ }
takeLock(); defer releaseLock()

candidates := []string{"/opt/etc/opkg.conf"}
matches, _ := filepath.Glob("/opt/etc/opkg/*.conf")
candidates = append(candidates, matches...)

var (
    changedFiles []string
    matched      bool
)
for _, path := range candidates {
    body, err := os.ReadFile(path); if err != nil { /* skip non-existent */ continue }
    out, hit := disableMatchingLine(body, url)
    if !hit { continue }
    matched = true
    if err := backupAndWrite(path, body, out); err != nil { return "err", ..., _ }
    changedFiles = append(changedFiles, path)
}

releaseLockEarly()  // so SmartUpgrade can retake

if !matched {
    // Idempotent no-op.
    return "ok", "🔧 Фид <url> уже отключён или не найден в opkg-конфигах.", OpkgUpgradeResult{}
}

// Now re-run the full upgrade flow.
status, result := o.SmartUpgrade(ctx)
prefix := fmt.Sprintf("🔧 Отключён фид %s в %s (backup: %s)\n\n", url, changedFiles, backupSuffix)
result.Output = prefix + result.Output
return status, prefix + ..., result
```

Helpers:

- `normalizeFeedURL(s)` — `TrimSuffix("/Packages.gz")`, then `TrimRight("/")`.
- `disableMatchingLine(body []byte, urlPrefix string)` — line-by-line:
  - Skip lines starting with `#` (already commented).
  - For lines matching `^src(/gz)?\s+\S+\s+<urlPrefix>(/.*)?\s*$` → prepend
    `# disabled by wg-monitor <RFC3339>: `.
  - Return new bytes + bool indicating any change.
- `backupAndWrite(path, oldBody, newBody)` — write `<path>.bak.<YYYYMMDDHHMMSS>`,
  then atomic write to `<path>.tmp` + `os.Rename(tmp, path)`.

### Wire types

```go
// pkg/wire/types.go
const OpkgFeedDisable CommandAction = "opkg_feed_disable"

type OpkgUpgradeResult struct {
    Output      string   `json:"output"`
    FailedFeeds []string `json:"failed_feeds,omitempty"`
}
```

`OpkgFeedDisable` joins the existing `IsValidCommandAction` registry; the
test `TestIsValidCommandAction` (already covers other actions) gets the new
case added.

`CommandResult.Payload` is already `json.RawMessage` (see existing
maintenance/routes payloads). For `opkg_smart_upgrade` and `opkg_feed_disable`
we marshal `OpkgUpgradeResult` into `Payload`.

### Callback grammar

```
opkg|disable|<token>
```

- `<token>` is a 16-hex char ID, mirrors `makeRebindToken` / `makeMaintToken`.
- `pendingOpkgRepair` is `map[string]opkgRepairEntry{ url, userID, routerID, expiresAt }`
  guarded by a mutex. 5-minute TTL, expiry checked on consume (no sweeper
  goroutine — map stays small, dead entries pruned opportunistically).
- Token is single-use — consuming removes the entry.
- `userID` + `routerID` are checked on consume against the clicker — guards
  against another user racing a copied callback in a shared topic.

### Inline-button render

In `alerts/command_result.go` (or the rendering path used by the
`opkg_smart_upgrade` reply), when `OpkgUpgradeResult.FailedFeeds` is
non-empty:

- For each URL, register a `pendingOpkgRepair` entry, get a token.
- Append an inline keyboard row: `[🔧 Отключить мёртвый фид (<host>)]`
  (where `<host>` is `net/url.Parse(url).Hostname()` truncated for label
  fit, full URL stays in the token entry).
- If 2+ failed feeds, 2+ rows (one button per row for readability — labels
  with host are long enough).

### Concurrency / locking

- `OpkgRunner.LockPath` (existing lock-file) gates both `SmartUpgrade` and
  `DisableFeed`. A second tap during in-progress repair returns `locked`.
- The file-lock is sufficient — no in-process mutex needed. Agent loop is
  single-threaded for command dispatch; the file-lock additionally protects
  against an out-of-band SSH operator running `opkg upgrade` at the same
  moment.

### ACL / safety

- Same callback ACL as other inline buttons: only authorized users for the
  per-router topic can trigger. Unauthorized callback → silent drop (the
  existing middleware).
- Rate-limit: 1 disable per minute per (user, router) tuple. Implemented
  with the existing `ratelimit` package or a simple cooldown map. Not
  strictly necessary (the action is fast and idempotent) — included for
  parity with `MaintConfirmAction` and to defang button-mash.
- URL validation in agent rejects anything that doesn't parse as
  `http://`/`https://` URL → impossible to coerce into FS-traversal.

## Testing

### Unit tests

**`internal/agent/actions/opkg_test.go`** (some already drafted):

- `TestParseOpkgUpdate` — fixture with 4 success + 1 failure lines.
- `TestOpkg_SmartUpgrade_PartialUpdateFailure_Continues` — partial failure +
  empty list-upgradable → status ok, output mentions dead URL.
- `TestOpkg_SmartUpgrade_TotalUpdateFailure_Errs` — no `Updated list` lines →
  status err.
- `TestOpkg_DisableFeed_PerFeedFile` — match in `tmp/opkg/anonym.conf` →
  line commented + backup file present.
- `TestOpkg_DisableFeed_MultiFeedFile` — `opkg.conf` with three `src/gz`
  lines, only target gets commented; other two untouched.
- `TestOpkg_DisableFeed_Idempotent` — second call returns ok, no second
  backup file.
- `TestOpkg_DisableFeed_NotFound` — URL absent → ok no-op, no FS writes.
- `TestOpkg_DisableFeed_InvalidURL` — `"' rm -rf /"` / `""` → err, no FS
  access at all.
- `TestOpkg_DisableFeed_SkipsCommentedLines` — line already `# src/gz ...`
  not re-disabled.
- `TestOpkg_DisableFeed_ThenSmartUpgrade` — fake exec returns clean update
  after disable → combined report contains both "🔧 Отключён" and the
  SmartUpgrade success.

Use `t.TempDir()` to redirect opkg-config paths; `OpkgRunner` gains a
`ConfigRoot string` field defaulting to `/opt/etc` so tests can point it
at the temp dir.

**`pkg/wire/types_test.go`:**

- `OpkgFeedDisable` in `IsValidCommandAction`.
- Round-trip JSON for `OpkgUpgradeResult` payload (including empty
  `FailedFeeds`).

**`internal/backend/callbacks/parse_test.go`:**

- Parses `opkg|disable|<token>` to the right `Action`.
- Rejects `opkg|disable|` (missing token) and `opkg|unknown|x`.

**`internal/backend/callbacks/router_test.go`** (new test cases):

- Token-consume flow: register pending entry → callback → enqueue cmd with
  right URL → entry removed.
- Token TTL expiry: pre-expired entry → callback returns "сессия истекла"
  alert, no enqueue.
- Unauthorized user → silent drop.

**`internal/backend/handler_test.go`:**

- `CommandResult` for `opkg_smart_upgrade` with `FailedFeeds` → produces
  message text containing the URL and inline button with token format.
- `CommandResult` with empty `FailedFeeds` → no buttons.

### Manual test on testkeen

After RC build:
1. Trigger `⬆ Обновить пакеты` reply-button.
2. Verify partial-success report appears with `⚠️ Недоступные фиды: anonym-tsk.github.io...`.
3. Verify `🔧 Отключить мёртвый фид` button is present.
4. Tap. Verify message edits in place to clean report (no failed feeds).
5. SSH to router, confirm:
   - `/opt/etc/opkg.conf` (or `/opt/etc/opkg/anonym.conf`) has line prefixed `# disabled by wg-monitor ...`.
   - `<file>.bak.<timestamp>` exists alongside.
6. Tap again (or re-trigger upgrade) → no button appears (feed already
   disabled).

## Rollout

- Land in the next v0.11.0 RC after rc24.
- Wizard `update-agent` + `update-backend` propagate to fleet.
- Mixed-version windows:
  - **New backend + old agent.** Old agent returns `CommandResult` with no
    `FailedFeeds` field (omitempty → absent on the wire) → backend sees
    empty list → no repair button shown. Smart-upgrade flow degrades to
    its current behavior. If a button somehow leaks through (e.g.
    backend was rolled back), the old agent rejects `opkg_feed_disable`
    with `unknown action` and the user sees one loud TG error, no router
    state change.
  - **Old backend + new agent.** New agent's `OpkgUpgradeResult` payload
    is ignored by the old backend (json-extra fields silently dropped) —
    no regression.

## Open questions

None at this point. All four design questions resolved in brainstorming
session (action type, UI placement, confirm flow, after-action). Items
listed in Non-Goals are deliberately deferred.
