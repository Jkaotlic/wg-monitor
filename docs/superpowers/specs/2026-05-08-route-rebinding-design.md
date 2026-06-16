# Route Rebinding via Telegram — Design

**Date:** 2026-05-08
**Status:** Approved
**Scope:** Allow admin to rebind all "smart routing" rules from one managed AmneziaWG tunnel to another via a Telegram panel, while strictly preserving rules pointing to WAN / system / other tunnels.

---

## 1. Use Case

Admin imports a new tunnel (e.g. `awg13`) and wants to migrate all routing rules currently attached to an old tunnel (`awg11`) onto the new one. The migration must NOT touch routes that go through WAN, system tunnels, or other managed tunnels — those carry RU-services that must stay on the ISP path due to RKN regional blocks.

The feature also exposes a **routing-mechanism inspector**: the same panel reports what smart-routing engines are configured on each router (HydraRoute Neo present? NDMS DNS routes? static IP routes?) and how many rules sit on each tunnel.

---

## 2. Non-Goals

- Auto-rebind on tunnel import (manual via panel only)
- Per-rule selection (rebind always covers ALL categories the tunnel has — DNS + Static IP + HR-Neo)
- Cross-category transactional rollback (idempotent re-run replaces it)
- Editing routes that target WAN / system / external tunnels (excluded by design)
- Managing HR-Neo when it is not installed (the section is hidden)

---

## 3. Data Flow

```
Admin → /routes (or "🛣 Маршруты" button)
  backend: callbacks.RoutesOpen
  backend: route status cache lookup (TTL 30s, per-router)
    └─ miss → wire.Command{Action:"route_status", Args:{}} per active router
  agent: GET /api/system/hydraroute-status
       + GET /api/tunnels/all
       + GET /api/dns-routes/list
       + GET /api/static-routes/list
       + GET /api/hydraroute/config            (only if HR-Neo installed)
  agent: aggregate into RouteSnapshot, return as wire.Response
  backend: render Screen 2 (status + per-tunnel counts), edit message in place

Admin: tap [🔄 Перенести] for src tunnel
  backend: callbacks.RoutesRebindStart → render Screen 3 (pick destination)

Admin: tap a destination tunnel
  backend: callbacks.RoutesRebindPick
    1. compute preview (from cached snapshot — counts per category for src target)
    2. mint 8-hex token, store in pendingRebinds (TTL 5 min)
    3. render Screen 4 (preview + "Подтвердить"/"Отмена")

Admin: tap [✅ Подтвердить]
  backend: callbacks.RoutesRebindConfirm
    1. validate token, consume it
    2. wire.Command{Action:"route_rebind",
                    Args:{src_tunnel_id, dst_tunnel_id}}
  agent: runner → route_rebind.go
    1. GET /api/tunnels/get?id=src   → srcRefs = {ID, NDMSName, InterfaceName, Name}
    2. GET /api/tunnels/get?id=dst   → dst.NDMSName (the canonical write target)
    3. DNS:    list → filter by srcRefs → POST /api/dns-routes/bulk-backend
    4. Static: list → filter by srcRefs → POST /api/static-routes/update?id= per rule
    5. HR-Neo: GET /api/hydraroute/config → replaceAll(srcRefs → dst.NDMSName)
               PUT /api/hydraroute/config/update
               POST /api/system/hydraroute-control {action:"restart"}
    6. POST /api/routing/refresh
    7. return RouteRebindResult with per-category {ok, failed, errors}
  backend:
    - invalidate route status cache for this router
    - render Screen 5 (success or partial fail with [🔁 Повторить])
```

---

## 4. UX (Telegram screens)

All screens are edits of the same panel message. Inline keyboard size stays under TG's 100-button limit.

### 4.1 Screen 1 — router picker (only if 2+ routers)

```
🛣 Маршруты — выбери роутер

[testkeen ▶]
[home-2 ▶]

[Закрыть]
```

If only one router is configured, this screen is skipped.

### 4.2 Screen 2 — router status

```
🛣 Маршруты — testkeen
   обновлено 14:02:33

HydraRoute Neo: ✅ установлен, работает
NDMS DNS routes:    12 правил
Static IP routes:    4 правила
HR-Neo policies:     8 политик

По туннелям (направленные в туннели):
  awg10 (sg)        → 0
  awg11 (amnezia)   → 8   [🔄 Перенести]
  awg12 (amnezia2)  → 4   [🔄 Перенести]

Не входят в перенос (показано для контроля):
  WAN:        12 правил
  System:      0 правил

[🔁 Обновить]  [Закрыть]
```

The "Перенести" button is shown only when the tunnel has >0 rules. Counts include only rules whose target is this managed tunnel. WAN/system/external sums are shown separately so the admin can verify they exist and will not be touched.

If HR-Neo is not installed, its line is hidden and the HR-Neo column drops from per-tunnel counts.

### 4.3 Screen 3 — pick destination

```
🛣 Перенос с awg11 (amnezia) → куда?

Доступные:
[awg10 (sg)]
[awg12 (amnezia2)]
[awg13 (newtun)]

[← Отмена]
```

Source tunnel is excluded. Disabled tunnels appear with a `(off)` suffix and are still selectable (admin may enable them after).

### 4.4 Screen 4 — preview (safety gate)

```
🛣 Превью: awg11 → awg13

Будет перенесено (8):
  • DNS routes:  5 (Vkontakte, Rutube, +3)
  • Static IP:   2 (work, gh-mirrors)
  • HR-Neo:      1 политика

НЕ ТРОГАЕМ:
  • WAN:          12 правил   ← RU-сервисы
  • awg10:         0
  • awg12:         4

token:8a3f  истекает через 5 мин

[✅ Подтвердить]  [← Отмена]
```

The "НЕ ТРОГАЕМ" block is required — it makes the safety guarantee visible before commit.

### 4.5 Screen 5 — result

Success:
```
🛣 ✅ awg11 → awg13 готово

  • DNS routes:  5 ok
  • Static IP:   2 ok
  • HR-Neo:      1 ok

[🛣 К маршрутам]  [Закрыть]
```

Partial failure:
```
🛣 ⚠ awg11 → awg13 — частично

  • DNS routes:  5 ok
  • Static IP:   1 ok, 1 FAIL (timeout)
  • HR-Neo:      1 ok

Операция идемпотентна — можно повторить.

[🔁 Повторить]  [🛣 К маршрутам]
```

### 4.6 Callback action grammar

| Callback | Effect |
|----------|--------|
| `routes_open:<uid>`                                       | open panel (skip Screen 1 if one router) |
| `routes_router:<uid>:<rid>`                               | switch active router |
| `routes_rebind:<uid>:<src_id>`                            | render Screen 3 |
| `routes_pick:<uid>:<src_id>:<dst_id>`                     | mint token, render Screen 4 |
| `routes_confirm:<uid>:<src_id>:<dst_id>:<token8>`         | execute rebind |
| `routes_refresh:<uid>:<rid>`                              | bypass cache, re-fetch status |
| `routes_back:<uid>`                                       | return to Screen 2 |
| `routes_close`                                            | dismiss panel |

The 8-hex token follows the existing `tunnel_import_*` pattern — single-use, 5-minute TTL, stored in a backend-local `pendingRebinds` map keyed by token.

---

## 5. Components Changed

| File | Change |
|------|--------|
| `internal/agent/awgmgr/types_routing.go` | NEW — `DNSRoute`, `DNSRouteEntry`, `StaticRoute`, `RoutingTunnel`, `RouteSnapshot`, `RouteRebindResult` |
| `internal/agent/awgmgr/routing.go` | NEW — `ListDNSRoutes`, `UpdateDNSRoute`, `ListStaticRoutes`, `UpdateStaticRoute`, `RoutingTunnels`, `RoutingRefresh`, `HydraRouteControl` |
| `internal/agent/actions/route_status.go` | NEW — collects snapshot via parallel calls (errgroup) |
| `internal/agent/actions/route_rebind.go` | NEW — three-phase execution, builds `srcRefs`, calls per-category methods, returns counts + failures |
| `internal/agent/actions/runner.go` | EDIT — add `route_status` and `route_rebind` cases |
| `pkg/wire/types.go` | EDIT — add `"route_status"` and `"route_rebind"` to `validCommandActions`; add `RouteSnapshot` and `RouteRebindResult` to `CommandResult` payload |
| `internal/backend/tg/routes_panel.go` | NEW — render functions for Screens 1–5; `RoutePanelEntry`, `RouteCounts` types |
| `internal/backend/callbacks/parse.go` | EDIT — register `routes_open`, `routes_router`, `routes_rebind`, `routes_pick`, `routes_confirm`, `routes_refresh`, `routes_back`, `routes_close` |
| `internal/backend/callbacks/actions.go` | EDIT — implement above callbacks; `pendingRebinds` map with token+TTL |
| `internal/backend/callbacks/routes_cache.go` | NEW — per-router TTL cache for `RouteSnapshot` (sync.Map + 30 s TTL) |
| `internal/backend/cmd_routes.go` (or wherever `/tunnels` lives) | EDIT — register `/routes` command and the menu button |

---

## 6. awg-manager API Contract

> **Verified 2026-05-08 against awg-manager 2.8.2 on testkeen.** Findings recorded in [docs/superpowers/notes/2026-05-08-routing-api-probes.md](../notes/2026-05-08-routing-api-probes.md).

### 6.1 Endpoints used

| Method | Path | Use |
|--------|------|-----|
| GET | `/api/system/hydraroute-status` | detect HR-Neo daemon |
| GET | `/api/tunnels/all` | list managed tunnels (for display) |
| GET | `/api/tunnels/get?id=` | resolve src/dst → `interfaceName` |
| GET | `/api/routing/tunnels` | catalogue of all routable interfaces (managed/system/wan) for "untouched" classification |
| GET | `/api/dns-routes/list` | enumerate DNS rules (mixed engines: HR-Neo + NDMS) |
| POST | `/api/dns-routes/update?id=` | per-rule rebind (full body resent) |
| GET | `/api/static-routes/list` | enumerate static IP rules |
| POST | `/api/static-routes/update` | per-rule rebind (full body, id in body, NO query param) |
| POST | `/api/system/hydraroute-control` | restart HR-Neo daemon (body `{"action":"restart"}`) — only invoked if any rule with `backend:"hydraroute"` was touched |
| POST | `/api/routing/refresh` | reset NDMS routing cache after a batch |

All requests carry `X-Requested-With: XMLHttpRequest`. **NOT used in v1** (originally planned but not the right primitive): `bulk-backend` (changes the engine field, not the tunnel target), `hydraroute/config` GET/PUT (daemon settings only — no per-rule data).

### 6.2 The bind identifier — `iface`

DNS and static routes both bind to an interface by its **kernel device name** (`iface` field from `/api/routing/tunnels`):
- managed tunnels: `nwg0`, `nwg1`, …
- system tunnels: `Wireguard0`, `Wireguard2`, …
- WAN: `eth3`, `apcli0`, …

For a managed tunnel, the route interface value is the fresh `iface` from
`/api/routing/tunnels`. DNS entries also need the stable AWG Manager
routing/NDMS bind id in `tunnelId` when available. So the rebind logic uses:
```go
srcIface := src.Iface               // e.g. "nwg1" for awg11
dstIface := dst.Iface               // e.g. "nwg0" for awg12
dstDNSBindID := routeDNSBindID(dst) // e.g. "Wireguard3" or "awg12"
```

A rule "belongs to src" iff its bind field equals `srcIface`. WAN-targeted rules (e.g. `iface == "eth3"`) NEVER match — guaranteed by interface uniqueness on the router.

#### DNS rule binding

```json
{
  "id": "hr:Vk",
  "routes": [{"interface": "nwg1", "tunnelId": "Wireguard1", "fallback": "auto"}],
  "backend": "hydraroute" | "ndms",
  "hrPolicyName": "HydraRoute"
}
```

`routes[i].interface` carries the kernel iface. For managed tunnels,
`routes[i].tunnelId` must carry the stable AWG Manager routing/NDMS id when
available; iface-only values such as `nwg0` can be rejected by AWG Manager. A
rule with `routes: null` falls through to its global engine policy (HR-Neo's
`policyOrder[0]` = `"HydraRoute"` policy whose default tunnel is set in
`ip route table 4096`, NOT manageable via API).

#### Static rule binding

```json
{
  "id":       "<rid>",
  "name":     "<name>",
  "tunnelID": "nwg1",
  "subnets":  ["10.0.0.0/24"],
  "fallback": "auto",
  "enabled":  true
}
```

Note: `tunnelID` (capital D) — different casing from DNS's `tunnelId`.

### 6.3 Per-category rebind logic

#### DNS routes

For each rule from `/api/dns-routes/list`:

```
if rule.routes != null:
    for entry in rule.routes:
        if entry.interface == srcIface:
            entry.interface = dstIface
            entry.tunnelId  = dstDNSBindID
            mark rule as touched
elif convertFallthroughEnabled and rule.routes == null
                                and rule.backend == "hydraroute"
                                and globalHRPolicyDefault == srcIface:
    rule.routes = [{interface: dstIface, tunnelId: dstDNSBindID, fallback: "auto"}]
    mark rule as touched

if rule was touched:
    POST /api/dns-routes/update?id=<rule.id> body=full_rule_object
```

`convertFallthroughEnabled` is a v1 default-on flag. The fall-through-conversion case is the user's primary use case (47/48 rules on testkeen are fall-through). Without it, a rebind would touch nearly nothing for HR-Neo users.

`globalHRPolicyDefault` detection: heuristic — assume it equals the `defaultRoute=true` managed tunnel's iface from `/api/tunnels/all`. For testkeen that's `awg11/nwg1`. Edge case: if no managed tunnel has `defaultRoute=true`, fall-through conversion is skipped and a UI warning is shown.

#### Static routes

For each rule from `/api/static-routes/list` where `rule.tunnelID == srcIface`:
```
rule.tunnelID = dstIface
POST /api/static-routes/update body=full_rule_object   (id is in the body, NOT in URL)
```

#### Finalisation

```
POST /api/routing/refresh
if any DNS rule with backend == "hydraroute" was touched:
    POST /api/system/hydraroute-control body={"action":"restart"}
```

### 6.4 Order and concurrency

- Sequential: DNS → Static → finalisation. No parallelism (NDMS recomputes on each write).
- Per-rule failures within a category are recorded but do not stop subsequent rules.
- Idempotent: re-running rebind src→dst on already-migrated rules finds no rules matching srcIface and is a no-op.
- The agent serialises rebinds per router via a `sync.Mutex`. A second concurrent rebind queues behind the first.
- No rollback within a rebind. If Static fails after DNS succeeded, partial success is reported and the user can re-run.

---

## 7. Caching

Backend keeps an in-memory cache of `RouteSnapshot` per router with a 30-second TTL. Cache invalidation:

- TTL expiry → next read refetches
- `[🔁 Обновить]` button → bypass and refresh
- After a successful `route_rebind` for a router → invalidate that router's entry so Screen 5 → "К маршрутам" shows fresh data

The cache lives in process memory only — a backend restart drops it, which is fine.

---

## 8. Error Handling

### 8.1 Network / HTTP

| Case | Behaviour |
|------|-----------|
| Timeout (10 s) on a single call | category failure recorded, other categories proceed |
| 401 Unauthorized | agent re-auths (existing flow), retries once; on second 401 → `Err = "awg-manager auth failed"` |
| 4xx on a per-rule update | `Failed++` for that category, loop continues |
| 5xx on the listing endpoint | entire category marked failed (no rules to iterate); other categories still run |
| Connection refused / EOF | `Err` returned, no further calls in this rebind |
| Cancelled context | propagates, returns clean "cancelled" error |

### 8.2 Validation / state

| Case | Behaviour |
|------|-----------|
| `src == dst` | UI prevents (Screen 3 filter); agent guards with early `ok, counts={0,0,0}` |
| Token expired or wrong | callback rejected with "сессия истекла, открой панель заново" |
| Token replay (double-tap) | second use returns "already used" |
| dst tunnel deleted between Screens 3 and 4 | `route_rebind` validates existence; on miss → `Err = "target tunnel not found"` |
| Source tunnel deleted between snapshot and rebind | rules with stale target still match srcRefs; they migrate (acceptable, mirrors awg-manager's own behaviour for orphaned rules) |
| 0 rules on src | UI hides button; agent guards with early ok |
| dst disabled | preview shows `⚠ awg13 выключен`; allowed |
| HR-Neo installed but stopped | rebind config OK; restart still issued |
| Concurrent rebinds on same router | mutex serialises; second waits |

### 8.3 Degraded UI

If the agent returns `Err` for `route_status`:

```
🛣 Маршруты — testkeen
⚠ awg-manager не отвечает (timeout 10s)
   проверь /opt/etc/init.d/S99awg-manager

[🔁 Обновить]  [Закрыть]
```

---

## 9. Testing

### 9.1 Unit

```
internal/agent/awgmgr/routing_test.go
  ParseDNSRoute / ParseStaticRoute / ParseHRConfig (golden JSON fixtures)
  BulkBackendRequestShape (golden HTTP body)
  HRConfigReplaceTargets (input cfg + srcRefs → expected modified cfg)

internal/agent/actions/route_status_test.go
  SnapshotAggregation (mock awgmgr.Client, verify counts)
  SnapshotHRNeoAbsent (Installed=false → HR-Neo path skipped)

internal/agent/actions/route_rebind_test.go
  HappyPath (3 categories, all succeed)
  PartialFail (Static fails on rule 2 of 3 → counts reflect)
  ZeroRules (early return)
  SrcEqDst (early return)
  SrcRefsExpansion (rule matched by ID, NDMSName, InterfaceName, Name independently)

internal/backend/callbacks/routes_panel_test.go
  RenderEmpty / RenderTunnelsWithCounts / RenderHRNeoMissing
  CallbackParseAllActions
  TokenLifetime / TokenReplay
```

### 9.2 Integration with mock awg-manager

`httptest.Server` impersonates awg-manager:

- list endpoints return fixed fixtures
- bulk-backend / update / put-config record requests for assertion
- full status → rebind → status flow verifies counts moved from src to dst
- WAN-targeted rule fixture verifies it remains untouched

### 9.3 Manual smoke (acceptance)

1. Fresh wg-monitor; `/routes` → testkeen visible with status and counts
2. In awg-manager web-UI: create 2 DNS rules on `awg11`, 1 static on `awg11`, 1 HR-Neo policy targeting `awg11`, **and 1 DNS rule on WAN**
3. Via TG: import a new tunnel `awg13`
4. `/routes` → tap "Перенести" on `awg11`
5. Pick `awg13`; preview shows 4 rules to migrate, "WAN: 1" in untouched block
6. Confirm; result shows 4 ok
7. In awg-manager web-UI: confirm all 4 rules now point to `awg13`
8. **Safety check:** the WAN rule MUST still be on WAN. Failing this fails the feature.

Acceptance is conditional on step 8 passing.

### 9.4 Coverage target

~80% line coverage on new files. No coverage targets imposed on existing files unless touched.

---

## 10. Resolved Questions (post-probes 2026-05-08)

1. **Bind field for DNS rules** — `routes[i].interface` is the kernel iface; `routes[i].tunnelId` is the stable AWG Manager routing/NDMS id for managed tunnels. Fall-through if `routes==null`.
2. **Bind field for static rules** — `tunnelID` (capital D — note the casing difference from DNS).
3. **Bind value form** — the `iface` from `/api/routing/tunnels`, equal to `Tunnel.interfaceName` for managed tunnels (`nwg0`, `nwg1`, …); WAN/system have their own `iface` (`eth3`, `Wireguard2`, …).
4. **Update endpoint shape** — DNS uses `POST /api/dns-routes/update?id=<id>` body=full rule. Static uses `POST /api/static-routes/update` (no id in URL) body=full rule (id inside body).
5. **HR-Neo rule storage** — HR-Neo rules are NOT in `/api/hydraroute/config`. They are DNS rules with `backend:"hydraroute"` and live in `/api/dns-routes/list`. The hydraroute config endpoint contains daemon settings only.
6. **Fall-through rules** — DNS rules with `routes:null` and `backend:"hydraroute"` follow the global HR-Neo policy (named in `policyOrder[0]`). The policy's default tunnel is set via `ip route table 4096` outside awg-manager API. v1 converts these to explicit routes when the user-detectable global policy default matches `srcIface` (heuristic: managed tunnel with `defaultRoute=true`).
7. **bulk-backend** — `POST /api/dns-routes/bulk-backend` body `{listIDs, backend}` changes the engine field (hydraroute↔ndms), not the tunnel target. NOT used in this design.

Full transcripts and shape excerpts: [docs/superpowers/notes/2026-05-08-routing-api-probes.md](../notes/2026-05-08-routing-api-probes.md).

---

## 11. Future Work (out of v1)

- Auto-rebind option in tunnel-import flow (when `replace=true` and the new interface has a different identifier)
- Per-rule selection UI for cases where bulk migration is too coarse
- Routing-rules backup/snapshot before rebind (download JSON, attach to TG message) for forensic recovery
- Support for non-managed (system / external) tunnels as rebind targets
- `--pin <version>` style awg-manager API version negotiation when shapes change between awg-manager releases
