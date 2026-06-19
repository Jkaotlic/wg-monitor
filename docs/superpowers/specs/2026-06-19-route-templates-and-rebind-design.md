# Route templates + rebind-to-new-tunnel — design

Date: 2026-06-19
Status: implemented (pivoted — see "Implementation note")

## Implementation note (what shipped vs proposed)

While implementing, discovered the repo **already has** the route subsystem this
spec proposed to build from scratch: `route_templates` (lists awg-manager
`/api/presets` as templates), `route_add` (`materializeRouteTemplate` →
create a DNS/static route on a tunnel from a template/preset, with overlap
planning + HR-Neo support), and `route_rebind` (move routes between any two
tunnels, incl. a freshly-imported one). So instead of a parallel
`route_template_apply` action + a separate embedded template, the delta is:

1. **Fix `awgmgr.DNSRoute`**: add `Subnets` + `ManualText` so list→modify→update
   round-trips don't silently drop them (latent bug in `route_rebind`'s
   full-replace for subnet-bearing routes). Done + round-trip test.
2. **Make `route_add` mechanism-aware**: `buildRouteAddPlan` now refuses on a
   sing-box router (`settings.singboxRouter.enabled`) with a clear reason —
   NDMS/HR-Neo routes are ignored there, so creating one is dead config. Done +
   test. (Detection/surfacing of all three methods already shipped in v0.13.5.)
3. "перекинуть на новый туннель" = existing `route_rebind(src→new tunnel)`, now
   subnet-safe via the struct fix. "дополнить из шаблона" = existing
   `route_add`/`route_templates`, now mechanism-aware.

sing-box-side route writes remain a deliberate follow-up (the operator: "не
надо ничего менять на самом агенте, бот должен просто понимать все способы").

---

(original proposal below, for reference)

Scope: Routes management extension. All route ops go through the **awg-manager
API**, executed by the agent (like the existing `route_rebind`). The wg-monitor
agent's own config/monitoring behavior is NOT changed. **NDMS/HR-Neo now;
sing-box routers are detected and explicitly deferred.**

## Goal (operator asks, 2026-06-19)

1. "перекидывать маршрут из новой выпущенной конфигурации" — rebind existing
   route lists onto a freshly-imported AWG tunnel.
2. "дополнять маршруты из шаблона" — bulk-add a curated set of domain-list DNS
   routes (Telegram / YouTube / Торренты / Google / …) to a chosen tunnel from a
   built-in template.
3. "бот должен понимать все способы маршрутизации" — mechanism-aware: act per the
   active routing method (default-route / HR-Neo / sing-box).

## Verified awg-manager API (live, snekhaev, 2026-06-19)

- DNS route shape (`/api/dns-routes/list .data[]`): `{id, name, domains[],
  subnets[], manualDomains[], manualText, routes:[{interface, tunnelId,
  fallback?}], enabled, backend ("ndms"|"hydraroute")}`.
- `CreateDNSRoute` → `POST /api/dns-routes/create` (body = rule). Exists today.
- `UpdateDNSRoute` → `POST /api/dns-routes/update?id=` (full-replace).
- Mechanism signals: `settings.singboxRouter.enabled` (sing-box, deviceMode),
  `settings.download.routeTag` (NDMS default tunnel), `HydraRouteStatus`
  (HR-Neo). All already parsed (`route_status` reports all three as of v0.13.5).

### Gotcha to fix first

`awgmgr.DNSRoute` (`types_routing.go`) is missing `Subnets` and `ManualText`.
On `json.Marshal` for create/update those fields are dropped — a latent bug:
`route_rebind`'s full-replace update of a route that has subnets (e.g.
snekhaev's Telegram list) silently erases them. Add:

```go
Subnets    []string `json:"subnets"`
ManualText string   `json:"manualText,omitempty"`
```

and preserve them through list→modify→update round-trips.

## Design

### Part 1 — rebind onto a newly-imported tunnel

The existing `route_rebind(srcTunnelID, dstTunnelID)` already moves DNS+static
bindings between any two tunnels, and the Routes panel populates the destination
picker from `RoutingTunnels()`/`TunnelsAll()` — which includes a freshly-imported
tunnel. So Part 1 is **largely already supported**: pick the new tunnel as the
rebind destination. Work: (a) the `Subnets`/`ManualText` preservation fix above
so static/subnet-bearing routes survive the rebind; (b) confirm the panel lists
newly-imported tunnels as destinations.

### Part 2 — supplement routes from a template (new)

**Template** — a small, embedded, curated set of categories in
`internal/agent/actions/route_template.go`:

```go
type RouteTemplateList struct {
    Name    string
    Domains []string
    Subnets []string // optional
}
var defaultRouteTemplate = []RouteTemplateList{ /* Telegram, YouTube, Discord,
    Meta/Instagram, Google, Торренты — curated domains/subnets */ }
```

Kept deliberately small and maintainable; not an attempt to mirror a full
geosite database.

**New agent action `route_template_apply`** (args: `{tunnel_id string,
lists []string (optional; empty = all)}`):

1. Read the active routing method. If `settings.SingboxRouterActive()` → return a
   no-op result `{skipped: true, reason: "singbox_router_active", note: "router
   routes via sing-box; NDMS DNS-route template does not apply — sing-box route
   config is a follow-up"}`. (Mechanism-aware.)
2. Otherwise list existing DNS routes; for each template list **not already
   present by name** (idempotent), `CreateDNSRoute` bound to `tunnel_id`
   (`backend: "ndms"`, explicit `routes: [{interface, tunnelId}]` resolved from
   the chosen tunnel, `enabled: true`, `domains`, `subnets`, `manualText` built
   from the list).
3. Return `{created: []string, skipped: []string, mechanism: "ndms"|"hrneo"}`.

**Wire**: register `route_template_apply` in `validCommandActions` + parse args.
**Backend**: dispatch + dashboard/wizard allowlist.
**Dashboard/TG**: an "Add routes from template" control (pick tunnel + which
lists) in the Routes panel; shows the created/skipped summary. For a sing-box
router it shows the mechanism note instead of applying.

## Mechanism-aware behavior (summary)

| Active method | rebind | template supplement |
|---|---|---|
| NDMS default-route | move bindings (route_rebind) | create NDMS DNS routes |
| HR-Neo | move bindings / fall-through | create routes (backend per rule) |
| sing-box | report method; defer | **skip + note** (NDMS disabled) — follow-up |

## Testing

- Unit: template-apply payload construction (domains/subnets/routes/backend),
  idempotency-by-name (skip existing), sing-box skip path — all against a fake
  awgmgr client (no live router).
- Unit: `DNSRoute` round-trips `subnets`/`manualText` (marshal→unmarshal).
- Real create validated by the operator on a non-critical router before fleet
  use (prod write — not auto-tested against a live router).

## Risks

- **Prod write.** Creating DNS routes mutates a live router. Idempotent-by-name
  + operator-triggered + validate-on-one-router-first mitigate.
- Template content drift (domain lists change) — keep the set small; treat as a
  starter the operator can extend.
- Agent-side action → requires a fleet agent redeploy to land.
