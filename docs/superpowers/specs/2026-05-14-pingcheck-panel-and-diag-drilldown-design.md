# PingCheck Panel + Diag Drill-down

**Date:** 2026-05-14
**Target version:** v0.13.0
**Scope:** A (PingCheck Monitor Panel) + B-toggle (per-tunnel PingCheck enable/disable) + C-drill-down (tap-to-expand failing diag tests)

## Goal

Surface in Telegram three pieces of operational data that today live only in the awg-manager web UI at `192.168.31.1:2222`:

1. **PingCheck health per tunnel** — current alive/dead state, last latency, fail/success counts, restart counter — so the operator can spot an unstable tunnel without opening the web UI.
2. **Per-tunnel PingCheck on/off toggle** — disable the watchdog for a tunnel temporarily (e.g. while testing) without touching the router.
3. **Drill-down into failing diagnostic tests** — tap a `❌` row in the diag summary to see the per-tunnel reason text, instead of dumping raw JSON.

## Non-goals

- **No PingCheck config editor** (host / method / interval / threshold). The awg-manager modal itself notes "Пределы заданы компонентом ping-check Keenetic NDMS"; tuning these would require a multi-step text-input flow over Telegram for a setting changed once every few months. Out of scope; can be revisited if demand appears.
- **No "restart on dead" toggle** (the second slider in the awg-manager modal). Same rationale: rarely changed, separate scope.
- **No single-test diag retry.** Probing showed `/api/diagnostics/run` accepts no test filter — running one test would be a full re-run. The existing `[🔄 Diag]` button already covers that.
- **No new monitoring metrics, no auto-refresh polling.** All refresh is on-demand.
- **No agent-side retry logic** for ndmc / awg-mgr failures. A failure surfaces to the operator; auto-retry hides root cause.
- **No new wire-protocol shape.** New actions reuse the existing `wire.Command` / `wire.CommandResult` envelope.

## API surface confirmed (probed 2026-05-14)

- `GET /api/pingcheck/status` — works. Returns `{enabled, tunnels[]}` with all fields needed for A: `tunnelId`, `tunnelName`, `enabled` (per-tunnel watchdog), `backend`, `status` (`alive`/`dead`), `method`, `lastCheck`, `lastLatency`, `failCount`, `successCount`, `failThreshold`, `restartCount`, `tunnelRunning`. Already wired in [`internal/agent/awgmgr/client.go:114`](../../../internal/agent/awgmgr/client.go#L114).
- `POST /api/pingcheck/check-now` — works (already used as `pingcheck_now`).
- `GET /api/diagnostics/result` — works (already used).
- `POST /api/diagnostics/run` — works, no per-test filter.

**Not in API** (returned SvelteKit shell when probed): `/api/pingcheck/{config,settings,configure,list,enable,disable,update,save}`, `/api/tunnels/<id>/pingcheck`, `/api/diagnostics/tests`. Per-tunnel watchdog toggle path is *not* exposed via the GET probes; **a final mutation-endpoint check is the first task in implementation** (see Section 6 — Implementation order).

---

## Section 1 — Architecture

```
┌───────────── BACKEND (TG bot) ─────────────┐         ┌──── AGENT (router) ────┐
│                                            │         │                        │
│  panel hub (callbacks/panel_hub.go)        │         │  actions.Runner        │
│   ├ kind=maint                             │         │   ├ pingcheck_status   │ NEW
│   ├ kind=routes                            │         │   ├ pingcheck_toggle   │ NEW
│   ├ kind=status                            │         │   └ existing actions…  │
│   └ kind=pingcheck   ← NEW                 │         │                        │
│                                            │         │  awgmgr.Client         │
│  callbacks/pingcheck_panel.go ← NEW        │         │   └ PingCheckStatus()  │ already exists
│   ├ pingcheck_open handler                 │         │                        │
│   ├ pingcheck_toggle handler               │         │  ndmc exec             │
│   └ PingCheckPanelNotifier                 │         │   (for toggle fallback)│
│                                            │         │                        │
│  callbacks/diag_drilldown.go  ← NEW        │         └──────────┬─────────────┘
│   └ DiagTestExpandAction                   │                    │
│                                            │                    │
│  tg/pingcheck_panel.go  ← NEW              │                    │
│  tg/diag_keyboard.go    ← EXTEND           │                    │
│  alerts/diag_parse.go   ← EXTEND           │                    │
└────────────────────────────────────────────┘                    │
                       │ wire.Command + wire.CommandResult        │
                       └──────────────────────────────────────────┘
```

**Reuse principles:**
- New panel slot rides on the existing `panel:0:kind:<x>` hub mechanism.
- Toggle action follows the `tunnel_enable/disable` pattern in [`internal/agent/actions/runner.go:196-212`](../../../internal/agent/actions/runner.go#L196-L212): wire-action with `ndms_name` packed in args, fallback to ndmc CLI.
- Drill-down lives entirely on the backend — uses the already-wired `diagCache` ([`internal/backend/callbacks/diag_cache.go`](../../../internal/backend/callbacks/diag_cache.go)). No agent changes for C.
- Errors render through `alerts.Card` + `alerts.HintFor()` (canonical card from the TG UX polish spec).

---

## Section 2 — Data flow: PingCheck Monitor Panel (A)

```
admin: /panel
 └─→ Home screen (panel_hub.panelHomeMessage)
      └─→ tap [📡 PingCheck]                                      [callback: panel:0:kind:pingcheck]
           └─→ panelEditToKindPick (router list)
                └─→ tap router → panelHandlePush
                     └─→ publish into per_router topic
                          │
                          ▼
                     PingCheckPanel message in topic               [callback root: pingcheck_open:<uid>:_panel_]
                      ┌──────────────────────────────────────────┐
                      │ 📡 PingCheck — keenetic-prod             │
                      │                                          │
                      │ 🟢 awg10  82ms  ✓417  ✗0/3   restart×0  │
                      │ 🟢 awg11  78ms  ✓1.2k ✗0/3   restart×0  │
                      │ 🔴 awg12  ---   ✓5    ✗3/3   restart×7 ⚠│
                      │                                          │
                      │ Глобально: ✅ enabled                    │
                      └──────────────────────────────────────────┘
                      [⏸ awg10] [⏸ awg11] [▶ awg12]
                      [▶ Проверить сейчас]  [🔄 Обновить]
                      [ℹ Помощь]            [✖ Закрыть]
```

**Lifecycle:**
1. On `pingcheck_open` callback: backend `EnqueueWithRef(wire.Command{Action: "pingcheck_status"})`.
2. Agent `actions.Runner` dispatches `pingcheck_status` → `awgmgr.PingCheckStatus()` → returns the JSON envelope as `wire.CommandResult{Output: <json>}`.
3. Backend `cmdResultHandler` routes to new `PingCheckPanelNotifier.NotifyCommandResult` (action allow-list extended).
4. Notifier parses JSON into `[]PingCheckTunnel`, builds text via `tg.PingCheckPanelText`, builds keyboard via `tg.PingCheckPanelKeyboard`, calls `EditMessageText`.

**No cache.** Each open / refresh fetches fresh state.

**Refresh triggers:**
- `[🔄 Обновить]` → re-enqueue `pingcheck_status`.
- `[▶ Проверить сейчас]` → enqueue `pingcheck_now`, then immediately re-enqueue `pingcheck_status` (sequence guaranteed by per-user FIFO queue). Latency / counters update in the next render.
- Per-tunnel toggle → on success, automatic `pingcheck_status` re-enqueue (see Section 3).

**Render rules** (one row per tunnel):
```
<icon> <name>  <lat>  ✓<succ>  ✗<fail>/<thr>   restart×<n> <warn?>
```
- `<icon>`: `🟢` if `status=alive` and `enabled=true`; `🔴` if `status=dead`; `⏸` if per-tunnel `enabled=false`; `❓` for any other / unknown.
- `<lat>`: `<lastLatency>ms` if non-zero, else `---`.
- `<succ>`: integer if `<10000`, else `9.9k` form (one fractional digit).
- `<warn?>`: `⚠` suffix if `restartCount > 5` (heuristic — no historical decay yet; reset on watchdog restart anyway).

**Special states:**
- Empty `tunnels[]` → "Туннелей не обнаружено — PingCheck не отчитался."
- Global `enabled=false` → grey-out icon for all rows, banner line `Глобально: ⏸ disabled`. Toggle buttons still rendered (disabling watchdog per-tunnel still has meaning when it gets re-enabled globally).

---

## Section 3 — Data flow: per-tunnel PingCheck toggle (B-toggle)

**Callback shape:** `pingcheck_toggle:<userID>:<tunnel_id>:<ndms_name>:<enable>` where `enable` is `1` or `0`.
Example: `pingcheck_toggle:42:awg10:Wireguard0:0`.

**Wire-action shape:**
```go
wire.Command{
    Action: "pingcheck_toggle",
    Args: map[string]any{
        "tunnel_id": "awg10",
        "ndms_name": "Wireguard0",
        "enable":    false,
    },
}
```

**Agent execution** (`actions/pingcheck.go::PingCheckToggle`):

```
1. Try POST /api/pingcheck/toggle?id=<tid>&enable=<0|1>     [primary path]
   - If 2xx → return ok
   - If 404 / SPA-shell response → fall through
   - If other 4xx/5xx → fall through (NDMS path may still work)
2. ndmc -c "interface <ndms_name> ping-check"               [enable]
   ndmc -c "no interface <ndms_name> ping-check"            [disable]
   - If exit=0 → return ok
   - If exit≠0 → return aggregated err: "POST: <a>; ndmc: <b>"
```

The exact awg-mgr POST endpoint and exact ndmc syntax must be confirmed before coding — see Section 6 step 1.

**Backend post-toggle UX:**
1. On `CommandResult{Status: "ok"}`: re-enqueue `pingcheck_status` automatically (no extra user tap), then render fresh panel.
2. On `CommandResult{Status: "err"}`: keep panel as-is, prepend a sticky banner row to the panel text via `alerts.Card{Badge:"❌", Label:"Не удалось переключить awg10", Summary:<err>, Hint:<HintFor("pingcheck_toggle", err)>}`. Banner persists until next edit.

**Idempotency / dupe-tap protection:**
- Per-user, per-(action, tunnel_id) in-flight set with 5-second TTL, in-memory.
- Pattern mirrors [`cooldownStore`](../../../internal/backend/callbacks/maint.go#L84-L121) but keyed differently. Store: `pingcheckInflightStore` in `callbacks/pingcheck_panel.go`.
- Dup tap during window → `tg.AnswerCallbackQuery(ctx, q.ID, "⏳ команда уже выполняется")`, no enqueue.

---

## Section 4 — Data flow: diag drill-down (C)

**Where the data already is:**
- `/api/diagnostics/result` JSON cached in `diagCache` keyed by a short token. The token is already embedded in the existing `📊 raw` button's `callback_data`.

**New keyboard layout** (extend `tg/diag_keyboard.go::DiagResultKeyboard`):

```
existing:                          new:
[📊 raw] [🔄 Diag]                  [❌ Host route] [❌ MTU] [❌ DNS leak]   ← row of failing tests
                                   [📊 raw] [🔄 Diag]
```

- Buttons rendered **only for failing tests** (`status != "ok"`). Maximum 8 per row; spill to second row if more.
- Button label: `❌ <test short label>`. Long Russian labels are truncated to ~14 chars + `…`.
- callback_data: `diag_test:<cache_key>:<test_id>` where `<test_id>` is a stable short slug (`mtu`, `dns_leak`, `host_route`, `iptables`, `endpoint_ping`, `handshake`, `tunnel_conn`, `awg_proxy`, `pingcheck_status`, `route_resolve`, `validate_config`, `state_consistency`). Slug map built once during parse. Stays under 64-byte TG limit because `cache_key` is 8 hex.

**Tap flow (`DiagTestExpandAction`):**

```
1. Parse callback args → cache_key, test_id
2. Lookup raw JSON from diagCache by cache_key
   - miss → render "⏱ Сводка устарела" + [🔄 Diag] button → return
3. Re-parse JSON (parser is pure, ~ms cost)
4. Find TestDetail by test_id
   - not found → render "❓ Не нашёл этот тест" + [« К сводке] [🔄 Diag] → return
5. Render detail block:
     📊 Диагностика / <test full label>

     <icon> <tunnel-or-global label>
        <key>: <value>
        ...
        reason: <reason text>

     [« К сводке]  [📊 raw]
6. EditMessageText
```

**`« К сводке`** re-renders the original summary by re-parsing the same cached raw JSON. No state stored.

**Parser extension (`alerts/diag_parse.go`):**
- Existing parser returns bullets `[]Bullet{Status, Label}`.
- New parser returns `[]TestDetail{ID, Label, Status, PerTunnel: []PerTunnelDetail{TunnelLabel, KeyValues, Reason}}` plus the legacy bullet view (for the summary screen). Backwards-compat: missing detail fields → empty struct, summary still renders.
- Slug-from-Russian-label map lives in the parser.

---

## Section 5 — Error handling matrix (canonical)

All error renderings use `alerts.Card{Badge, Label, Summary, Hint}` rendered via `card.Render(alerts.CardOpts{MaxBytes: 3500})`. Hint dictionary extended in `alerts/hints.go` with new prefixes for `pingcheck_status`, `pingcheck_toggle`.

| Scenario | Render | Recovery button |
|---|---|---|
| A `pingcheck_status` agent err | "⚠ агент не ответил" + err + hint | `[🔄 Повторить]` |
| A awg-mgr 5xx / refused / timeout | Same shape, hint via prefix `HTTP_5xx`/`HTTP_REFUSED` | `[🔄 Повторить]` |
| A globally disabled (`enabled:false`) | Grey panel + banner "Глобально: ⏸ disabled" | `[🔄 Обновить]` |
| A empty tunnels list | Empty-state body | `[🔄 Обновить]` |
| B-toggle ndmc/POST err | Sticky banner over old panel | `[🔄 Обновить]` |
| B-toggle success but follow-up status err | Banner "✅ Переключено, но не удалось обновить" | `[🔄 Обновить]` |
| B-toggle dupe-tap inside 5s | Toast "⏳ команда уже выполняется" | (no edit) |
| C drill cache miss | "⏱ Сводка устарела" | `[🔄 Diag]` |
| C drill test_id not in JSON | "❓ Не нашёл этот тест" | `[« К сводке] [🔄 Diag]` |
| C drill JSON parse error | Existing fallback path (raw JSON dump) | `[📊 raw]` |

**Rollback semantics:** none required — A and C are read-only; B-toggle is a single atomic operation. Last-write-wins on race with another operator or with the awg-mgr web UI; no "your change was overwritten" notice.

**Logging:** `slog.Warn` with `action`, `tunnel_id`, `err`, `duration_ms` on every err path. Mirrors current awgmgr.Client logging.

---

## Section 6 — Implementation order

The plan must execute in this order to fail fast on unknowns:

1. **Verify mutation endpoints** *(no code yet)*
   - In awg-mgr web UI, open DevTools → Network, toggle the per-tunnel PingCheck slider on `192.168.31.1:2222/pingcheck`, observe the actual POST URL/body.
   - Confirm exact ndmc syntax for ping-check enable/disable: `ndmc -c "interface Wireguard0 ping-check"` vs `ndmc -c "no interface Wireguard0 ping-check"` — try once on a non-critical tunnel and verify with `awgmgr.PingCheckStatus()` before/after.
   - **Decision point:** if no awg-mgr POST endpoint exists, primary path in `PingCheckToggle` falls away and only the ndmc path remains. Update spec inline if so.

2. **Wire types + agent action** (Section 3): `actions/pingcheck.go` with `PingCheckStatus` (passthrough JSON) and `PingCheckToggle` (primary + fallback). Tests with fake awgmgr-client + fake `ExecFunc`.

3. **Wire dispatch** in `actions/runner.go` for both actions.

4. **TG renderers** (Section 2): `tg/pingcheck_panel.go` (text + keyboard). Pure functions, table-driven tests.

5. **Backend handlers + notifier** (Section 2 + 3): `callbacks/pingcheck_panel.go` with `pingcheck_open`, `pingcheck_toggle`, `PingCheckPanelNotifier`, in-flight store. Tests with fake `CommandEnqueuer` + fake TG.

6. **Hub registration**: extend `panel_hub.go` (kind=pingcheck), `router.go` (action allow-list, callback routes), `parse.go` (callback prefixes), `tg/help_panels.go` (help text).

7. **Diag parser extension** (Section 4): extend `alerts/diag_parse.go` to emit `[]TestDetail` alongside bullets. Tests cover legacy + new JSON shapes.

8. **Diag drill-down** (Section 4): `callbacks/diag_drilldown.go` with `DiagTestExpandAction`. Extend `tg/diag_keyboard.go` with failing-test buttons row.

9. **Integration test**: `cmd/backend/backend_pingcheck_integration_test.go` — full happy-path through fake awgmgr httptest server.

10. **Smoke on testkeen**: open all three flows manually, verify against web UI.

---

## Section 7 — File structure (delta)

**NEW:**
```
internal/agent/actions/pingcheck.go
internal/agent/actions/pingcheck_test.go
internal/backend/callbacks/pingcheck_panel.go
internal/backend/callbacks/pingcheck_panel_test.go
internal/backend/callbacks/diag_drilldown.go
internal/backend/callbacks/diag_drilldown_test.go
internal/backend/tg/pingcheck_panel.go
internal/backend/tg/pingcheck_panel_test.go
cmd/backend/backend_pingcheck_integration_test.go
```

**EXTEND:**
```
internal/agent/actions/runner.go            (+2 dispatch cases)
internal/backend/callbacks/panel_hub.go     (+ kind=pingcheck)
internal/backend/callbacks/router.go        (+ action allow-list, callback routes)
internal/backend/callbacks/parse.go         (+ pingcheck_open / pingcheck_toggle / diag_test prefixes)
internal/backend/tg/diag_keyboard.go        (+ failing-test buttons row)
internal/backend/tg/help_panels.go          (+ pingcheck panel help)
internal/backend/alerts/diag_parse.go       (+ TestDetail extraction)
internal/backend/alerts/hints.go            (+ pingcheck_* hints)
pkg/wire/commands.go                        (if a const list exists — add new actions)
```

---

## Out of scope (explicit)

- Per-tunnel "restart on dead" toggle.
- PingCheck host / method / interval / threshold editor.
- Single-test diag retry endpoint (would require awg-mgr API change).
- Diag run history / diff between runs.
- Auto-refresh / periodic polling of any panel.
- Multi-language UI.
- Persistence of UI state across backend restart (in-flight store, panel cache — all in-memory, lost on restart, acceptable).
