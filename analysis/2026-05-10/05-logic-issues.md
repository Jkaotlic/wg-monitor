# wg-monitor — logic & architecture audit

Scope: `internal/backend/{alerts,callbacks,cmd,db,handler,heartbeat,realert,retention,state,tg,upstream}` plus `internal/agent/{actions,awgmgr,checks,cmdloop,reporter,config}`. cmd/deploy excluded by request (recently audited rc12-rc18).

Severity: **HIGH** = silently breaks user-facing behaviour or leaks resources in prod; **MED** = wrong in edge case, misleading log, or magic constant drift; **LOW** = pure DRY/abstraction.

---

## Behaviour-changing (fix this cycle)

### LOGIC-01 — Maint confirm path drops MessageRef → panel never refreshes after restart/install [HIGH]
**File:** `internal/backend/callbacks/maint.go:151`

`MaintConfirmAction.Apply` enqueues with `a.sink.Enqueue(args.UserID, cmd)` instead of `EnqueueWithRef`. So when the agent posts the `service_restart` / `firmware_install` `CommandResult`, the handler's `ConsumeOriginRef` returns `(_, false)` and **`MaintNotifier.NotifyCommandResult` is never invoked**. The user's Maintenance panel stays in the confirmation screen forever; the "✅ запрос отправлен: %s" message-edit is the last UI signal.

The other confirm action — `RebindConfirmAction.Apply` (`actions.go:335`) — correctly uses `EnqueueWithRef`. Asymmetric.

**Fix:** capture `q.Message.{Chat.ID,MessageID,MessageThreadID}` into a `cmdpkg.MessageRef` and call `EnqueueWithRef`. Drop the unused `_ cmdpkg.MessageRef` lint-suppress at `maint.go:164` afterwards.

### LOGIC-02 — `cmd.Queue.results` map grows without bound [HIGH]
**File:** `internal/backend/cmd/queue.go:158-175` (and `q.origins` partially same)

`RecordResult` writes into `q.results[userID][result.ID]` and `q.signal.Broadcast()`s. Nothing ever deletes from `q.results`. The only consumer is `AwaitResult` — but production code currently uses `ConsumeOriginRef` + a TG-relay goroutine, not `AwaitResult`. So every command leaves a permanent entry in `q.results` for the lifetime of the backend process.

`q.origins` is partially mitigated by `ConsumeOriginRef`, but if the agent never POSTs a result (crash mid-action, network partition that outlives the agent's retry window), the entry persists.

**Fix:** TTL-evict both maps (e.g., piggyback on the retention loop, or run a small janitor goroutine). Or have `ConsumeOriginRef` also drop the matching `q.results` entry once the relay goroutine fires.

### LOGIC-03 — Heartbeat watcher re-notify hardcoded to 6h, ignores RealertEvery [MED]
**File:** `internal/backend/heartbeat/watcher.go:149`

```go
notify := !sent || now.Sub(last) > 6*time.Hour
```

Magic `6*time.Hour` duplicates what `Config.State.RealertEverySec` controls for the realert poller. If an operator sets `realert_every_sec: 3600` (1h) to be louder, ROUTER-OFFLINE re-notifications still wait 6h. Either drop the config knob into `heartbeat.Config.RenotifyEvery` or document the divergence.

### LOGIC-04 — FormatRealert tail string lies about cadence [MED]
**File:** `internal/backend/alerts/format.go:155`

```go
fmt.Fprintf(&b, "\nс %s (%s назад) · Re-alert #%d / 6h", ...)
```

Hardcodes `/ 6h` regardless of `RealertEvery`. If config diverges, the operator sees `#3 / 6h` while the actual cadence is e.g. 1h or 12h. Pass `RealertEvery` into `RealertArgs` and humanise.

### LOGIC-05 — `upstream.Cache.Latest` thundering-herd on miss [MED]
**File:** `internal/backend/upstream/versions.go:71-88`

`Latest` checks the cache under RLock, then drops the lock and fires `c.fetch(...)`, then re-acquires Lock to write. Two concurrent callers for the same name (e.g. simultaneous smart-replies + maint-panel render) both miss → both fire a GitHub API request → eat the 60 req/h anonymous limit twice as fast as needed. Add a per-name singleflight or a "fetching" sentinel.

### LOGIC-06 — `Reporter.ForceResumed` can race with the scheduled tick [MED]
**File:** `internal/agent/reporter.go:85-90`

`ForceResumed` flips `forceResumed` and calls `sendOnce(ctx)` from the cmd-loop goroutine. The 60s ticker can fire concurrently and call `sendOnce(ctx)` itself. Two concurrent `sendOnce` runs each marshal a full `wire.Report` and POST. Backend's reportHandler is idempotent FSM-wise, but you get duplicate per-tunnel events in the DB (skews retention). Serialise with a `sync.Mutex` around `sendOnce`, or use a single-slot trigger channel.

### LOGIC-07 — SQL abstraction leak in `dispatchSmartReply` [MED]
**File:** `internal/backend/callbacks/router.go:715-739` (`collectActiveIncidents`)

Goes around the StateRepo and uses `r.d.SQL().Query(...)` with a hand-written SQL string, including the `silenced_until / acked` filter logic. `db.StateRepo.AllActiveHard` already exists and almost matches, but applies no filters. The duplicated WHERE-clause means changes to one (e.g. tightening "active" semantics) won't propagate to the other. Add `db.StateRepo.ActiveHardForUser(uid)` mirroring `AllActiveHard`'s filters and call it.

### LOGIC-08 — `collectNeighbors` duplicated across dispatcher and realert.poller with skew [MED]
**Files:** `internal/backend/alerts/dispatcher.go:128-155` (`collectNeighbors`) and `internal/backend/realert/poller.go:75-108` (`neighborSummaries`)

Same data shape, same prefix, same details parsing. The dispatcher version overrides `ns.Status` with `ping_check_status` when present (line 144); the poller version does NOT. Result: HARD alerts and STILL-DOWN reminders show different status strings for the same neighbour. Extract one helper into the `alerts` package and call from both sites.

### LOGIC-09 — `Update`/`UpdateLine` formatter logic duplicated in 2 places [MED]
**Files:** `internal/backend/callbacks/router.go:1173-1194` (`computeUpdates → []alerts.UpdateAvailable`) and `internal/backend/callbacks/maint_notifier.go:124-150` (`buildMaintPanelArgs → tg.UpdateLine`)

Both compare `va.FirmwareCurrent` vs `va.FirmwareAvail` via `upstream.FirmwareNewerThan`, then iterate `awgmgr` / `hrneo` against `upstream.SoftwareNewerThan`. The output struct differs in name only. If the rules drift (e.g. ignoring suffixes), only one site picks it up. Extract a shared `computeUpdates(va, up) []UpdateInfo` and let each site project to its UI struct.

### LOGIC-10 — `CommandAction.Apply` falls back to bare Enqueue when q==nil, dropping MessageRef [MED]
**File:** `internal/backend/callbacks/actions.go:230-243`

```go
if q != nil { EnqueueWithRef(...) } else { Enqueue(...) }
```

Only test code sets `q==nil` today, so behaviour-equivalent in prod. But this is a foot-gun: any new caller that takes the `q==nil` branch (e.g. a future scheduled-trigger feature) silently loses the result-relay. Either make this an explicit error, or always `EnqueueWithRef` with a zero-valued ref when q is nil.

### LOGIC-11 — `IsMobile` flag relies on `Users.GetByID` lookup that may fail silently [LOW-MED]
**File:** `internal/backend/alerts/dispatcher.go:65-67`

```go
if u, err := di.d.Users().GetByID(userID); err == nil {
    args.IsMobile = u.IsMobile()
}
```

On a transient DB error a mobile user is rendered without the 📱 badge. Already-logged elsewhere as "warn", but here the err is silently dropped. Either log warn or tolerate by reusing the nickname-keyed lookup we just did via `ensureTopic`.

---

## Refactoring (deferred — separate PR)

### REFACT-01 — `callbacks/router.go` is 1207 lines and mixes 6 concerns
Routing, panel-build, smart-reply, document-upload, maint, routes, pendingRebind. Split:
- `router_core.go` — `Run`/`HandleCallback`/`HandleMessage`/dispatch table
- `panels_tunnels.go` — buildTunnelsPanel, dispatch* helpers
- `panels_maint.go` — handleMaint*
- `panels_routes.go` — handleRoutes*
- `smart_reply.go` — collectTunnelViews / collectActiveIncidents / computeUpdates / dispatchSmartReply
- `import.go` — handleDocumentUpload / handlePendingNameReply / sendImportConfirmation

The huge `switch` in `HandleCallback` (lines 251-326) can become a registry: `actionHandlers map[string]func(...)`.

### REFACT-02 — `actions/runner.go` 13 nil-checks for AwgClient/Exec
Each case re-validates `r.AwgClient == nil` / `r.Exec == nil`. Build a tiny `requires(awg, exec bool)` helper or split into per-action methods that the dispatch loop introspects.

### REFACT-03 — Three near-identical TG send variants
`SendMessage`, `SendMessageWithKeyboard`, `SendMessageWithReplyKeyboard` (`tg/client.go`). Collapse into one varargs/options call with functional options (`WithInlineKB`, `WithRawMarkup`, `WithReplyTo`).

### REFACT-04 — Three near-identical awgmgr POST helpers
`Client.post`, `Client.confPost`, `Client.postJSON` (`agent/awgmgr/client.go` + `routing.go`). Collapse to one `postJSON(path, body, out, opts...)`.

### REFACT-05 — `moscowLoc()` duplicated
`callbacks/actions.go:52` and `alerts/format.go:546` define identical `Europe/Moscow` loaders. Move to a shared `pkg/timezone` (or `internal/backend/locale`).

### REFACT-06 — `cmdSink == nil` checks scattered through router.go (5+ sites)
Either reject `nil` sink at construction time (`NewRouterWithSink` returns error if sink is nil but command actions are wired) or have `cmdSink` be a no-op sink that returns a typed error.

### REFACT-07 — `simpleAuditCache` and `routesCache` have identical shape
Both are per-user TTL'd in-memory `map[int64]T` + RWMutex. Promote to a generic `cache[K, V]`.

### REFACT-08 — Repeated `loadingText + EditMessageText + EnqueueWithRef + AnswerCallbackQuery` template
Fires in `openRoutesPanelMessage`, `openMaintPanelMessage`, `handleRoutesOpen`, `handleMaintOpen`, `handleMaintFwCheck`. Extract `enqueueWithLoading(ctx, q, action, text)`.

### REFACT-09 — `db.DB.SQL()` exposed as escape hatch and used in business logic
`router.go:716` uses it to query incident_state. Adding `StateRepo.ActiveHardForUser` would let us seal the escape hatch and untangle that import.

---

## Out of scope / verified OK

- `state/fsm.go` — pure, no obvious transition gap; test coverage looks dense.
- `retention/policy.go` — clean separation, defaults reasonable.
- `cmd/queue.go::Dequeue` — cond-variable wakeup pattern is correct.
- `auth.go` — token comparison uses `subtle.ConstantTimeCompare`, lookup goes via `GetByToken`.
- `heartbeat::staleFor` — kind-aware threshold logic with sane fallbacks.
