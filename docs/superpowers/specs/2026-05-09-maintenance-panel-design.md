# Maintenance Panel — Design

**Date:** 2026-05-09
**Status:** Draft
**Targets:** v0.11.0 (post `v0.10.0` GA)

## Summary

Add a `🛠 Обслуживание` (Maintenance) inline panel to the per-router topic in
the Telegram bot, exposing operational actions previously available only via
SSH:

1. **Restart HydraRoute-Neo** (hrneo).
2. **Restart awg-manager** daemon.
3. **Reboot the Keenetic router** itself.
4. **Show installed and available KeeneticOS firmware** + install it.
5. **Soft warnings about outdated software** (firmware / awg-manager / hrneo)
   surfaced inside the existing `📊 Что происходит?` smart-reply, plus an
   on-demand "Проверить апдейты" button in the Maintenance panel.

All restart/reboot/install operations require a confirm screen with a 5-minute
token (mirroring the routes_confirm pattern). Router reboot and firmware
install additionally enter a 5-minute cooldown to prevent double-fire.

## Why now

- Operator currently SSHes to the router for every restart of hrneo/awg-mgr,
  for every firmware update, and for the router-reboot itself. Routine ops
  through the bot saves time.
- Several incidents in the past month traced back to "outdated awg-manager"
  or "outdated hrneo" — there is no signal in the bot today that an update
  exists, so they accumulate silently until something breaks.

## Non-Goals

- **No package-level update beyond firmware** in this iteration. Awg-manager
  and hrneo updates are surfaced as warnings only — installing them is still
  manual `opkg upgrade <pkg>` for now (covered partially by the existing
  `⬆ Обновить пакеты` reply-button, which runs `opkg smart upgrade`).
- **No firmware channel switching** (release/preview/draft). KeeneticOS users
  almost always stay on `release`; channel toggling is a once-a-year op done
  via web UI.
- **No firmware changelog** in the panel. Changelog parsing requires extra
  ndmc calls and HTML scraping — out of scope. User can read changelog on
  Keenetic's website if curious.
- **No daily upgrade-available push.** Smart-reply (pull) + on-demand button
  (force-pull) are sufficient. Daily push risks fatigue.

## Architecture

Reuses the established pattern: reply-keyboard button → inline panel → cmd-queue
action on agent → CommandResult edits the panel in place (identical to the
Routes panel introduced in v0.10.0).

### New components

| Component | Location | Purpose |
|---|---|---|
| `🛠 Обслуживание` reply-button | `internal/backend/tg/reply_keyboard.go` | Third row of `per_router` reply-keyboard. |
| `MaintPanel` renderer | `internal/backend/tg/maint_panel.go` (new) | Renders 4 screens: status / restart-confirm / firmware / firmware-confirm. |
| Maint callback handlers | `internal/backend/callbacks/router.go` (`handleMaint*`) | Open / restart / confirm / firmware sub-flows. |
| `pendingMaint` map | `internal/backend/callbacks/maint.go` (new) | 5-min token TTL per pending confirm; `cooldown` map for router/firmware. |
| `MaintConfirmAction` | `internal/backend/callbacks/maint.go` | Consumes token, enqueues cmd, applies cooldown. |
| `MaintPanelNotifier` | `internal/backend/callbacks/maint_notifier.go` (new) | Edits panel in place when CommandResult arrives. |
| `service_restart` cmd | `internal/agent/actions/runner.go` (new case) | Dispatches to hrneo / awgmgr / router. |
| `firmware_status` cmd | `internal/agent/actions/maintenance.go` (new) | Parses `ndmc -c "show version"`. |
| `firmware_install` cmd | `internal/agent/actions/maintenance.go` | `ndmc -c "components commit"` (auto-reboots). |
| `version_audit` cmd | `internal/agent/actions/maintenance.go` | Compact payload of installed versions for the panel + smart-reply. |
| `RestartSelf()` awgmgr method | `internal/agent/awgmgr/system.go` (extend) | TBD: API endpoint or init.d fallback (curl-verify). |
| `upstream.Cache` | `internal/backend/upstream/versions.go` (new) | GitHub releases fetcher with 12h TTL, queried on smart-reply render. |
| Smart-reply update section | `internal/backend/alerts/smart_reply.go` + `format.go` | Append "🟡 Доступны обновления" if `Updates` non-empty. |

### Agent ↔ backend boundary

- **Installed versions:** agent (already in `awg_manager` check; extended
  `version_audit` returns hrneo + firmware too).
- **Upstream versions:** backend (one GitHub API hit covers entire fleet,
  cached 12h). Avoids burning per-router GitHub anonymous quota.
- **Comparison:** backend (during smart-reply render and panel render).

## Data flow

### Open Maintenance panel

```
[reply] 🛠 Обслуживание  →  router.HandleMessage("🛠 Обслуживание")
  → openMaintPanelMessage():
      SendMessage("🛠 Обслуживание — обновляется…")
      enqueue version_audit cmd with MessageRef
  → agent runs version_audit:
      VersionAudit returns {awgmgr, hrneo, firmware_current, firmware_available}
  → MaintPanelNotifier.NotifyCommandResult:
      EditMessageText(maint panel render with installed + cooldown state)
```

### Restart hrneo/awgmgr/router

```
[tap] 🔁 Restart hrneo
  → maint_restart:UID:hrneo
  → handleMaintRestart: render confirm screen, store pendingMaint{token, name=hrneo}
  → user taps ✅ Подтвердить
  → maint_confirm:UID:hrneo:TOK
  → MaintConfirmAction: consume token, enqueue service_restart{name=hrneo}
  → if name ∈ {router}: applyCooldown(userID, 5m)
  → agent runs r.AwgClient.HydraRouteControl(ctx, "restart")
  → MaintPanelNotifier edits panel: "✅ hrneo restarted (1.2s)"
  → next agent tick (~60s) refreshes uptime; or user taps 🔄 Проверить
```

### Firmware install

```
[tap] 📦 Прошивка → maint_fw_open
  → handleMaintFwOpen: render firmware screen using cached version_audit
[tap] ⬆ Установить и перезагрузить → maint_fw_install:UID
  → handleMaintFwInstall: render confirm with token + cooldown warning
[tap] ✅ Подтвердить → maint_fw_confirm:UID:TOK
  → MaintConfirmAction: consume, enqueue firmware_install
  → applyCooldown(userID, 5m)
  → agent fires `ndmc -c "components commit"` and exits within seconds
  → router reboots; agent loses connection; CommandResult never arrives (ok-ish)
  → backend renders panel with "🔄 Установка началась — роутер уйдёт в reboot…"
  → next heartbeat (post-reboot) confirms recovery
```

### Soft warnings in smart-reply

```
[reply] 📊 Что происходит?  →  dispatchSmartReply
  → collectTunnelViews + collectActiveIncidents (existing)
  → upstream.Cache.LatestAll(ctx) → {awgmgr_latest, hrneo_latest}
  → versionAudit(installedFromLatestEvent) → diff vs upstream
  → SmartReplyArgs.Updates = [{Name, Installed, Available}]
  → FormatSmartReply appends "🟡 Доступны обновления:" section if non-empty
```

## Callback grammar

Format: `action:user_id:arg[:arg…]`. All actions added to `validActions`.

| Action | Args | Notes |
|---|---|---|
| `maint_open` | (none) | Force re-fetch (re-enqueues `version_audit`). |
| `maint_close` | (none) | Clear keyboard, leave text. |
| `maint_restart` | `name` ∈ {hrneo, awgmgr, router} | Render confirm screen. |
| `maint_confirm` | `name`, `token` | Consume token (5-min TTL). |
| `maint_fw_open` | (none) | Render firmware screen. |
| `maint_fw_check` | (none) | Force re-fetch firmware_status. |
| `maint_fw_install` | (none) | Render install-confirm. |
| `maint_fw_confirm` | `token` | Consume and enqueue firmware_install. |

Token = 8 hex chars (matches existing `makeRebindToken` style). 5-min TTL.

## Pending state

```go
type pendingMaint struct {
    UserID    int64
    Name      string  // "hrneo" | "awgmgr" | "router" | "firmware"
    Token     string  // 8 hex
    ExpiresAt time.Time
}

type cooldownEntry struct {
    Until  time.Time
    Action string  // "router_reboot" | "firmware_install"
}
```

Both maps are in-memory on the backend (`Router.pendingMaintMu` + map). Bot
restart loses them — acceptable: tokens are short-lived; cooldown after a
reboot just means panel re-render shows no cooldown which is correct (router
already came back).

## Cooldown semantics

`router_reboot` and `firmware_install` apply a per-user 5-minute cooldown.
While active:

- The corresponding action's button renders as `🕒 Cooldown HH:MM:SS` and
  is disabled (callback returns "🕒 кулдаун ещё N сек" toast).
- `maint_open`/`maint_fw_open` re-renders include the cooldown state.

`hrneo` and `awgmgr` restarts do **not** trigger cooldown — they're cheap and
don't disconnect the operator.

## Agent: new awgmgr methods

```go
// internal/agent/awgmgr/system.go (extend)

// RestartSelf restarts the awg-manager daemon.
// TBD via curl-verify on testkeen: prefer POST /api/system/restart-self
// or similar. If no API exists, fall back to running
// `/opt/etc/init.d/S52awg-manager restart` via Exec.
func (c *Client) RestartSelf(ctx context.Context) error
```

```go
// internal/agent/actions/maintenance.go (new)

// FirmwareStatus parses `ndmc -c "show version"` output.
type FirmwareStatus struct {
    Current   string  // "4.2.6" / etc.
    Available string  // "5.0.1" if update; "" otherwise.
    Hint      string  // "system upgrade is available" / etc. — raw hint.
    Channel   string  // "release" / "preview" / etc.
}

func GetFirmwareStatus(ctx context.Context, exec ExecFunc) (FirmwareStatus, error)
func InstallFirmware(ctx context.Context, exec ExecFunc) error
func VersionAudit(ctx context.Context, awg *awgmgr.Client, exec ExecFunc) (wire.VersionAudit, error)
```

```go
// pkg/wire/maintenance.go (new)

type VersionAudit struct {
    AwgmgrVersion   string
    HrneoVersion    string  // empty if not installed
    FirmwareCurrent string
    FirmwareAvail   string
    HrneoUptime     string  // for panel render; "3д 4ч"
    AwgmgrUptime    string
}
```

## Agent: new runner cases

```go
case "service_restart":
    name, _ := cmd.Args["name"].(string)
    switch name {
    case "hrneo":
        if err := r.AwgClient.HydraRouteControl(ctx, "restart"); err != nil { ... }
        return "ok", "hrneo restarted"
    case "awgmgr":
        if err := r.AwgClient.RestartSelf(ctx); err != nil { ... }
        return "ok", "awg-manager restarted"
    case "router":
        if !r.AllowRouterReboot { return "err", "disabled by agent config" }
        if _, err := r.Exec(ctx, "ndmc", "-c", "system reboot"); err != nil { ... }
        return "ok", "reboot scheduled"
    default:
        return "err", "unknown service: " + name
    }

case "firmware_status":
    fs, err := actions.GetFirmwareStatus(ctx, r.Exec)
    if err != nil { return "err", err.Error() }
    payload, _ := json.Marshal(fs)
    return "ok", string(payload)

case "firmware_install":
    if !r.AllowFirmwareInstall { return "err", "disabled by agent config" }
    if err := actions.InstallFirmware(ctx, r.Exec); err != nil { ... }
    return "ok", "firmware install kicked; router will reboot"

case "version_audit":
    va, err := actions.VersionAudit(ctx, r.AwgClient, r.Exec)
    if err != nil { return "err", err.Error() }
    payload, _ := json.Marshal(va)
    return "ok", string(payload)
```

## Agent config

```yaml
# /opt/etc/wg-monitor/config.yaml
maintenance:
  allow_router_reboot:    true   # default false; wizard sets true on install
  allow_firmware_install: true   # default false; opt-in
```

Wizard's `add-router` step sets both to `true` by default (the operator
explicitly onboarded this router for ops; if they didn't want destructive
buttons they'd say so). Manual edit supported for paranoid setups.

## Backend: upstream cache

```go
// internal/backend/upstream/versions.go (new)

type Source struct {
    Name       string  // "awg-manager" | "hrneo"
    GitHubRepo string  // "<owner>/<repo>" — e.g. "Slava-Shchipunov/awg-keenetic"
}

type Entry struct {
    Latest    string
    FetchedAt time.Time
    Err       error
}

type Cache struct {
    TTL     time.Duration
    sources []Source
    http    *http.Client
    mu      sync.RWMutex
    data    map[string]Entry
}

func NewCache(ttl time.Duration, sources []Source) *Cache
func (c *Cache) Latest(ctx context.Context, name string) (string, error)
func (c *Cache) LatestAll(ctx context.Context) map[string]Entry
```

Backend.yaml config:

```yaml
upstream:
  awgmgr_repo:  "Slava-Shchipunov/awg-keenetic"   # TBD: verify on first install
  hrneo_repo:   "Mihaylov-Sergei/HydraRoute-Neo"  # TBD: verify on first install
  cache_ttl:    "12h"
```

If a repo is empty or unreachable: `Latest` returns "" (no warning). Logged
once per failure (debounced 1h) so ops can see a misconfig.

## Smart-reply integration

Extend `alerts.SmartReplyArgs`:

```go
type SmartReplyArgs struct {
    // ... existing fields
    Updates []UpdateAvailable
}

type UpdateAvailable struct {
    Name      string  // "KeeneticOS" | "awg-manager" | "HydraRoute-Neo"
    Installed string
    Available string
}
```

`FormatSmartReply` appends section if `len(Updates) > 0`:

```
🟡 Доступны обновления:
  • KeeneticOS 4.2.6 → 5.0.1
  • awg-manager 2.8.2 → 2.9.0
```

Source for `Updates`: backend reads latest `awg_manager` event (has `firmware`,
`version`, etc.) and latest `hydraroute` event (has `version` once we extend
that check to surface it), plus `upstream.Cache.LatestAll`. Diff via:

- Firmware: custom `firmware.NewerThan(installed, candidate string) bool`
  (Keenetic versions are dotted but not strict semver — extract numeric parts
  + lex-compare).
- Software: `golang.org/x/mod/semver` after stripping leading `v`/`V`.

If a comparison fails (parse error), skip silently — don't false-warn.

## UI mocks

### Maint status screen

```
🛠 Обслуживание — testkeen

Сервисы:
  • HydraRoute-Neo  ✅ running, v2.4.0  uptime 3д 4ч
  • awg-manager     ✅ running, v2.8.2  uptime 7д 12ч
  • Keenetic OS     KN-1811, v4.2.6  uptime 14д

Обновления:
  • KeeneticOS 4.2.6 → 5.0.1   ⬆
  • awg-manager 2.8.2 → 2.9.0  ⬆
  (hrneo актуальна)

[🔁 Restart hrneo]    [🔁 Restart awg-mgr]
[🔁 Reboot router]    [📦 Прошивка]
[🔄 Проверить апдейты]  [✖ Закрыть]
```

If cooldown active for router-reboot:

```
[🔁 Restart hrneo]    [🔁 Restart awg-mgr]
[🕒 Cooldown 04:23]   [📦 Прошивка]
```

### Restart confirm screen

```
🛠 Обслуживание — testkeen

⚠️ Перезапустить HydraRoute-Neo?

Что произойдёт:
  • DNS-routes на короткое время (~5 сек) перестанут резолвиться по правилам.
  • Static-routes продолжат работать.
  • Кратковременная просадка на доменах из ip.list.

Token: a1b2c3d4 (TTL 5 мин)

[✅ Подтвердить]  [↩ Отмена]
```

### Reboot router confirm

```
🛠 Обслуживание — testkeen

⚠️ Перезагрузить роутер?

Что произойдёт:
  • Все туннели разорвутся (~1-2 мин downtime).
  • Если ты сейчас сидишь через VPN, TG отвалится до восстановления.
  • Алерты придут сразу после reboot — это нормально.
  • Кулдаун: 5 мин (повторное нажатие заблокировано).

Token: 9e8f7a6b (TTL 5 мин)

[✅ Подтвердить]  [↩ Отмена]
```

### Firmware screen

```
📦 Прошивка — testkeen

Текущая:    KeeneticOS 4.2.6 (KN-1811)
Доступная:  KeeneticOS 5.0.1   ⬆ обновление

Канал: release

[⬆ Установить и перезагрузить]
[🔄 Перепроверить] [↩ Назад]
```

If no update:

```
📦 Прошивка — testkeen

Текущая:    KeeneticOS 5.0.1 (KN-1811)
Доступная:  актуальная

Канал: release

[🔄 Перепроверить] [↩ Назад]
```

## Testing

### Unit

| File | Coverage |
|---|---|
| `internal/backend/callbacks/parse_test.go` | All 8 new callback grammars (round-trip + invalid args). |
| `internal/backend/callbacks/maint_test.go` | pendingMaint TTL, cooldown apply/check, MaintConfirmAction enqueues correct cmd. |
| `internal/backend/tg/maint_panel_test.go` | Golden render: status (with/without updates / with/without cooldown), restart-confirm, firmware (update/no-update). |
| `internal/backend/upstream/versions_test.go` | Mock GitHub server, verify cache hit/miss/TTL/error swallowing. |
| `internal/agent/actions/maintenance_test.go` | Parse `ndmc show version` golden output (capture from real router); VersionAudit aggregator with mock Awg+Exec. |
| `internal/agent/actions/runner_test.go` | service_restart all branches; allow-flag enforcement. |
| `internal/backend/alerts/smart_reply_test.go` | Updates section render (empty / one item / all three). |

### Integration / smoke (manual)

1. Tap `🔁 Restart hrneo` → confirm → bot reports OK within 2 sec; `ssh root@router 'pidof hrneo'` returns new PID.
2. Tap `🔁 Restart awg-mgr` → confirm → awgmgr web UI 502s for ~3 sec, then 200.
3. Tap `🔁 Reboot router` → confirm → router pings stop, ~90 sec later all tunnels recover; alert RECOVERY arrives.
4. Tap `📦 Прошивка` while no update available → screen says "актуальная", install button hidden.
5. Verify cooldown: tap reboot, confirm, then re-open panel within 5 min — Reboot button shows `🕒 Cooldown HH:MM`.
6. Verify smart-reply: artificially patch installed version older than upstream, tap `📊 Что происходит?` → "🟡 Доступны обновления" section appears.

### Safety guards (unit)

- `service_restart router` returns `err "disabled by agent config"` when
  `allow_router_reboot=false`.
- `firmware_install` returns same error when `allow_firmware_install=false`.

## Pre-implementation TBDs (verify on first SSH session)

1. **Awg-manager self-restart endpoint:** does `/api/system/restart` (or
   similar) exist? If not, use `/opt/etc/init.d/S52awg-manager restart` via
   Exec.
2. **Upstream repo identifiers:** confirm GitHub repo names for awg-manager
   (`Slava-Shchipunov/awg-keenetic`?) and HydraRoute-Neo
   (`Mihaylov-Sergei/HydraRoute-Neo`?). Check what tag format they use.
3. **`ndmc -c "show version"` output:** capture exact format on testkeen so
   `GetFirmwareStatus` parser handles real data, not assumed shape.
4. **`ndmc -c "components commit"` behavior:** confirm it actually triggers
   firmware install + reboot (vs needing separate `system reboot` after).
5. **HydraRoute version source:** does `/api/system/hydraroute-status` return
   version? If not, parse `/opt/etc/HydraRoute/VERSION` or similar via Exec.
6. **Daemon uptime source:** how to collect hrneo / awgmgr uptime for the
   panel. Candidates: `pidof <name>` + `stat -c %Y /proc/$pid` for start time,
   or `ps -o etime= -p $pid`. Pick whatever Entware's busybox supports.

These six SSH curls / commands at the start of implementation. Adjust code
to fit reality, but design holds.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| User accidentally taps reboot, loses TG-via-VPN. | Confirm screen + 5-min cooldown + explicit warning text. |
| GitHub anonymous rate limit (60/h) blocks upstream check. | 12h TTL cache, debounced error log, graceful "" fallback. |
| `ndmc components commit` mid-call gets agent killed. | We expect this — return "ok" before reboot. Recovery confirmed via post-reboot heartbeat. |
| Awg-manager restart kills the API call mid-request. | If API endpoint exists: accept that response may be cut off (treat conn-reset as OK). If init.d fallback: spawn detached so Exec returns immediately. |
| Firmware version comparison gives false-positive on dev versions. | `firmware.NewerThan` returns `false` on parse errors → no warning rather than wrong warning. |
| Cooldown lost on backend restart, user double-taps reboot during outage window. | Acceptable: ndmc itself rejects concurrent reboot; second reboot returns err in toast. |

## Rollout

- Single PR, behind no flag (additive UI; agent flags default-off but wizard
  enables them).
- Agent change requires new agent binary deploy → wizard's `update-agent`
  flow (already exists from v0.10.0).
- Backend change requires backend redeploy → wizard's `update-backend` flow.
- Tag as `v0.11.0-rc1` for testkeen smoke, then `v0.11.0` after.
- Documentation: README "Maintenance Panel" feature line + brief usage para.
