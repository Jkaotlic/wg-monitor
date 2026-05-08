# awg-manager Routing API Probes — testkeen
**Date:** 2026-05-08
**Source:** Live curl + awg-manager web UI bundle inspection on 192.168.31.1 (KeeneticOS 5.00.C.11.0-0, awg-manager 2.8.2, KN-1811).

---

## Tunnels on testkeen

`GET /api/tunnels/all`:
```json
{
  "tunnels": [
    {"id":"awg11", "name":"amnezia_for_awg",  "interfaceName":"nwg1", "ndmsName":"Wireguard1", "type":"awg", "enabled":true, "backend":"nativewg"},
    {"id":"awg12", "name":"amnezia_for_awg2", "interfaceName":"nwg0", "ndmsName":"Wireguard0", "type":"awg", "enabled":true, "backend":"nativewg"}
  ]
}
```

`GET /api/routing/tunnels` returns a different shape — used by routing UI dropdowns:
```json
[
  {"id":"awg11", "name":"amnezia_for_awg",  "iface":"nwg1", "type":"managed",  "available":true},
  {"id":"awg12", "name":"amnezia_for_awg2", "iface":"nwg0", "type":"managed",  "available":true},
  {"id":"system:Wireguard2", "name":"Wireguard VPN Server", "iface":"Wireguard2", "type":"system",  "available":true},
  {"id":"wan:eth3",   "name":"Подключение Ethernet",      "iface":"eth3",      "type":"wan",     "available":true},
  {"id":"wan:apcli0", "name":"Wi-Fi клиент 2.4 ГГц",       "iface":"apcli0",    "type":"wan",     "available":false}
]
```

Note: `iface` here is the canonical value used in route bindings. Managed tunnels have `iface` matching the WireGuard kernel interface (`nwg0`/`nwg1`), NOT the NDMS name (`Wireguard0`/`Wireguard1`).

---

## DNS Routes (`GET /api/dns-routes/list`)

### Shape

48 rules total, all with `id` prefixed `hr:` (HydraRoute Neo rules). Schema:

```json
{
  "id":            "hr:Ali",                    // engine prefix + rule name
  "name":          "Ali",
  "domains":       ["alipay.com", ...],         // resolved (manual+subscriptions)
  "manualDomains": ["alipay.com", ...],         // user-editable
  "routes":        [{"interface":"eth3", "tunnelId":"eth3"}] | null,
  "enabled":       true,
  "createdAt":     "",
  "updatedAt":     "",
  "backend":       "hydraroute",                // engine: hydraroute | ndms
  "hrRouteMode":   "policy",                    // policy | direct
  "hrPolicyName":  "HydraRoute"                 // global policy identifier
}
```

### KEY: `routes` field semantics

- `routes: [{interface, tunnelId, fallback?}]` — explicit per-rule binding. **`interface` and `tunnelId` carry the SAME value**, the `iface` from `/api/routing/tunnels` (e.g. `"nwg1"`, `"eth3"`).
- `routes: null` — fall-through to the global HR-Neo policy named `hrPolicyName`. The global policy's default tunnel is configured outside the API (in `ip route table 4096` and HR-Neo daemon config files like `/opt/etc/HydraRoute/domain.conf`). On testkeen currently `default dev nwg1`.

### KEY: bulk "Сменить туннель" UI handler

The web UI does NOT use `/api/dns-routes/bulk-backend` for the "Change tunnel" mass operation. It iterates `/api/dns-routes/update` per rule. Source from `_app/immutable/nodes/7.BdrxntWo.js`:

```js
const x = d.routes.length > 0
    ? [{...d.routes[0], tunnelId: e(H), interface: e(H)}, ...d.routes.slice(1)]
    : [{tunnelId: e(H), interface: e(H), fallback: "auto"}];
await Pe.updateDnsRoute(c, {...d, routes: x});
```

Where `e(H)` = the selected destination tunnel's `iface` value.

So:
- Rule HAD explicit routes → first entry's `interface`/`tunnelId` is replaced with the new value, other entries (priority-2 fallbacks) preserved.
- Rule HAD `routes: null` → routes is **created** as a single-entry array `[{tunnelId, interface, fallback:"auto"}]`. **This converts the rule from "fall-through to global policy" to "direct route".**

### What `bulk-backend` actually does

`POST /api/dns-routes/bulk-backend` body shape: `{"listIDs": [...], "backend": "<engine>"}`.

This changes the **engine** field (e.g. `hydraroute` ↔ `ndms`), NOT the target tunnel. Not relevant to our rebind feature — discard.

### CRUD methods on DNS routes (UI-confirmed)

```
GET    /api/dns-routes/get?id=<id>
POST   /api/dns-routes/list
POST   /api/dns-routes/create               body: full route object
POST   /api/dns-routes/update?id=<id>       body: full route object (with new routes[])
POST   /api/dns-routes/delete?id=<id>
POST   /api/dns-routes/set-enabled?id=<id>  body: {"enabled": bool}
POST   /api/dns-routes/create-batch         body: array of route objects
POST   /api/dns-routes/delete-batch         body: {"ids": [...]}
POST   /api/dns-routes/refresh              (subscriptions reload)
POST   /api/dns-routes/bulk-backend         body: {"listIDs":[...], "backend":"<engine>"}  ← NOT FOR US
```

---

## Static Routes (`GET /api/static-routes/list`)

### Shape

Currently empty (`[]`) on testkeen — no fixtures. From the UI bundle:

```json
{
  "id":       "<rid>",
  "name":     "<human name>",
  "tunnelID": "<iface>",              // bind field — note CAPITAL D
  "subnets":  ["10.0.0.0/24", ...],   // CIDRs
  "fallback": "auto",
  "enabled":  true
}
```

### KEY: bind field `tunnelID` (DIFFERENT capitalization from DNS)

- DNS uses `routes[].tunnelId` (lower-case d)
- Static uses `tunnelID` (capital D)
- Both hold the same kind of value — the `iface` from `/api/routing/tunnels`

### "Сменить туннель" handler for static

```js
for (const d of e(N)) {
    const x = t.ipRoutes.find(U => U.id === d);
    if (x) await Pe.updateStaticRoute({...x, tunnelID: e(G)});
}
```

Iterates per-rule `updateStaticRoute` with the full object + new `tunnelID`.

### CRUD methods

```
GET    /api/static-routes/list
POST   /api/static-routes/create               body: full route object
POST   /api/static-routes/update               body: full route object (with id)
POST   /api/static-routes/delete?id=<id>
POST   /api/static-routes/set-enabled?id=<id>  body: {"enabled": bool}
POST   /api/static-routes/import               body: {"tunnelID":<iface>, "name":..., "content":<txt>}
```

Note: `update` does NOT take `id` in the URL — the `id` is in the body.

---

## HR-Neo Config (`GET /api/hydraroute/config`)

### Shape

```json
{
  "autoStart":          false,
  "clearIPSet":         false,
  "cidr":               true,
  "ipsetEnableTimeout": true,
  "ipsetTimeout":       21600,
  "ipsetMaxElem":       0,
  "directRouteEnabled": true,
  "globalRouting":      false,
  "conntrackFlush":     true,
  "log":                "",
  "logFile":            "",
  "geoIPFiles":         ["/opt/etc/HydraRoute/geofile/geoip_GA.dat"],
  "geoSiteFiles":       ["/opt/etc/HydraRoute/geofile/geosite_GA.dat"],
  "policyOrder":        ["HydraRoute"]
}
```

### CRITICAL FINDING: HR-Neo config does NOT contain rules

The endpoint returns ONLY global daemon settings — `policyOrder` lists policy names, not routing rules. Per-rule tunnel binding lives entirely in `/api/dns-routes/list` (rules with `backend:"hydraroute"`).

The global policy "HydraRoute" itself binds to a tunnel via `ip route show table 4096` (default rule, currently `default dev nwg1`) which awg-manager does NOT manage via API. Changing the global policy's target requires SSH + Keenetic CLI — out of scope for v1.

**Implication for design:** "HR-Neo rules" = DNS rules with `backend:"hydraroute"`. They are operated through the same `/api/dns-routes/*` API as NDMS rules. There is no separate HR-Neo rule API. Our rebind logic only deals with DNS routes (mixed engine) + static routes.

### HR-Neo daemon control

```
POST /api/system/hydraroute-control   body: {"action":"start"|"stop"|"restart"}
GET  /api/system/hydraroute-status    → {"installed":bool, "running":bool}
PUT  /api/hydraroute/config/update    body: full config JSON  (only for global settings — irrelevant to rebind)
```

After mass-updating DNS rules with `backend:"hydraroute"`, restart HR-Neo so the daemon reloads its rules from disk (awg-manager writes to `/opt/etc/HydraRoute/domain.conf` on each rule update).

---

## Resolved: spec §10 Open Questions

| # | Question | Answer |
|---|----------|--------|
| 1 | Field name in DNS route holding tunnel reference | `routes[i].interface` AND `routes[i].tunnelId` (same value, both fields). For static routes it's `tunnelID` (capital D). |
| 2 | Value form of the bind field | The `iface` from `/api/routing/tunnels` — the kernel interface name (`nwg0`, `nwg1`, `eth3`, `Wireguard2`), NOT the NDMS name and NOT the awg-manager tunnel ID. |
| 3 | bulk-backend body | `{listIDs:[...], backend:<engine>}` — but irrelevant; we use per-rule update. |
| 4 | HR-Neo etag/version | None visible on the config GET. Single-admin race window accepted. |
| 5 | HR-Neo policy targets shape | N/A — per-rule binding lives in `/api/dns-routes/*`, not in HR-Neo config. |
| 6 | bulk-backend missing-id behaviour | N/A — we use per-rule update; per-id failures are caught individually. |

---

## Routing Refresh

```
POST /api/routing/refresh   → forces NDMS cache reset; should be called after a batch of route updates.
```

---

## Concrete rebind algorithm (revised from spec)

For `rebind src=<src_tunnel_id> dst=<dst_tunnel_id>`:

1. `GET /api/tunnels/get?id=<src>` → `srcIface = src.interfaceName` (e.g. `nwg1`)
2. `GET /api/tunnels/get?id=<dst>` → `dstIface = dst.interfaceName` (e.g. `nwg0`)
3. `GET /api/dns-routes/list` → for each rule:
   - If any entry in `rule.routes` has `interface == srcIface` (or `tunnelId == srcIface`):
     replace that entry's `interface` and `tunnelId` with `dstIface`; `POST /api/dns-routes/update?id=<rule.id>` with full body.
   - **Optionally (config flag):** if `rule.routes == null` AND `rule.backend == "hydraroute"` AND `rule.hrPolicyName == "HydraRoute"` AND the global HydraRoute policy's current default == srcIface (detected separately): create explicit `routes:[{interface:dstIface, tunnelId:dstIface, fallback:"auto"}]` and update.
   - Else skip.
4. `GET /api/static-routes/list` → for each rule with `tunnelID == srcIface`:
   `POST /api/static-routes/update` body `{...rule, tunnelID: dstIface}`.
5. `POST /api/routing/refresh`
6. If any HR-Neo (`backend:"hydraroute"`) rule was touched: `POST /api/system/hydraroute-control` with `{action:"restart"}`.

The "fall-through with global policy default" optional handling is the only design knob — for v1 default to ON (most useful for the user's case where 47/48 rules are fall-through). UI Screen 4 should make this visible: "+47 fall-through rules will get explicit routes to dst (was: следуй глобальной политике HydraRoute, default nwg1)".

---

## Field-name mapping table for the Go client

| Domain field | DNS routes | Static routes | Source |
|--------------|-----------|---------------|--------|
| Rule ID      | `id`      | `id`          | both `id` |
| Tunnel bind (per-rule) | `routes[i].interface` + `routes[i].tunnelId` | `tunnelID` | UI bundle |
| Engine       | `backend` (`hydraroute`/`ndms`) | n/a | DNS only |
| HR policy name | `hrPolicyName` | n/a | DNS only when `backend=hydraroute` |
| Bind value   | `iface` from `/api/routing/tunnels` | same | tested with `eth3` example |

---

## Endpoints to implement in Go client

```go
// internal/agent/awgmgr/routing.go
ListDNSRoutes(ctx) ([]DNSRoute, error)
GetDNSRoute(ctx, id) (*DNSRoute, error)
UpdateDNSRoute(ctx, rule DNSRoute) error          // POST /api/dns-routes/update?id=<id> with full body

ListStaticRoutes(ctx) ([]StaticRoute, error)
UpdateStaticRoute(ctx, rule StaticRoute) error    // POST /api/static-routes/update with full body in JSON

RoutingTunnels(ctx) ([]RoutingTunnel, error)      // /api/routing/tunnels — needed for iface mapping
RoutingRefresh(ctx) error
HydraRouteStatus(ctx) (already exists)
HydraRouteControl(ctx, action string) error
```

Drop these from the original plan: `BulkBackendDNS`, `GetHRConfig`, `PutHRConfig`, `ReplaceHRTargets`. They were based on incorrect assumptions about how HR-Neo rules are stored.
