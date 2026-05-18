# Route Add/Delete and Overlap Design

Date: 2026-05-18

## Goal

Add safe Telegram workflows for adding and deleting router routes, backed by live
awg-manager/HR-Neo data and explicit overlap detection before any write.

## Source of Truth

Route lists are pulled from the most capable source available on the router:

1. If HR-Neo/HydraRoute is installed and available, DNS route inventory prefers
   HydraRoute-backed rules from the awg-manager DNS route list, including
   `backend=hydraroute`, `hrRouteMode`, `hrPolicyName`, `routes`,
   `domains`, and `manualDomains`.
2. If HR-Neo is not installed or not available, the workflow falls back to the
   awg-manager DNS/static route APIs.
3. Static CIDR routes always come from the awg-manager static route API.

The implementation must not depend on backend cache for safety decisions. The
agent fetches live router state for previews and fetches it again immediately
before writes.

## Route Overlap Detection

The agent owns overlap detection because it can read the live router state.

Targets are normalized before comparison:

- Domains are lowercased, trailing dots are removed, and IDNA normalization is
  applied. `example.com` overlaps `api.example.com`.
- CIDRs are parsed with `net/netip`. A single IPv4 or IPv6 address becomes
  `/32` or `/128`.
- Static routes use `subnets`.
- DNS/HR-Neo entries that parse as IP/CIDR targets are compared as CIDR
  targets.

Severity policy:

- `block`: exact or overlapping target is already bound to a different tunnel,
  interface, WAN, or system bind.
- `warn`: duplicate or broader/narrower target is bound to the same tunnel, or
  the existing rule is disabled.
- `info`: target cannot be safely compared.

Blocking overlaps prevent add/delete confirmation from executing a write.

## Add Route Workflow

Telegram route panel adds an `Add route` entry.

The flow:

1. User chooses route type: DNS/HR-Neo or Static CIDR.
2. User chooses destination tunnel/interface.
3. User sends route name and targets as a text reply.
4. Backend stores a short-lived draft bound to user, topic, and router.
5. Backend enqueues read-only `route_add_plan`.
6. Agent fetches live routes, normalizes targets, computes overlaps, and
   returns a preview.
7. Telegram renders the preview. `Confirm` is available only when there are no
   blocking overlaps.
8. Backend enqueues `route_add`.
9. Agent fetches live routes again, recomputes overlaps, refuses if a new
   blocking overlap appears, creates the route, refreshes routing, and restarts
   HR-Neo only when a HydraRoute-backed DNS route was created.

Version 1 intentionally has no force override for blocking overlaps.

## Delete Route Workflow

Telegram route panel adds delete controls for individual visible DNS/HR-Neo and
static routes.

The flow:

1. User selects one existing route to delete.
2. Backend stores a short-lived delete draft bound to user, topic, router, route
   kind, route id, and a preview hash.
3. Backend enqueues read-only `route_delete_plan`.
4. Agent fetches live routes and returns the exact route preview: name, kind,
   backend, bind/tunnel, enabled state, domains/CIDRs, and risk warnings.
5. Telegram renders the preview and confirmation button.
6. Backend enqueues `route_delete`.
7. Agent fetches live routes again, verifies the route still exists and matches
   the preview hash, deletes that single route, refreshes routing, and restarts
   HR-Neo only when a HydraRoute-backed DNS route was deleted.

Version 1 deletes one route per confirmation. Bulk delete is out of scope.

Risk warnings for delete:

- Default-route-like targets such as `0.0.0.0/0` or `::/0`.
- WAN/system binds.
- Routes with a large target count.
- HydraRoute-backed rules that affect HR-Neo policies.

Warnings do not block deletion, but the preview must be explicit.

## Wire Commands

Add read-only preview commands:

- `route_add_plan`
- `route_delete_plan`

Add mutating commands:

- `route_add`
- `route_delete`

The mutating commands must reuse the existing route mutex so add, delete, and
rebind cannot race each other.

## Telegram Callback Shape

Callbacks use short tokens to stay inside Telegram callback length limits.

Add route callbacks:

- `routes_add:<uid>:_panel_`
- `routes_add_type:<uid>:dns|static`
- `routes_add_tunnel:<uid>:<draft_token>:<tunnel_id>`
- `routes_add_confirm:<uid>:<draft_token>:<confirm_token>`
- `routes_add_cancel:<uid>:<draft_token>`

Delete route callbacks:

- `routes_del:<uid>:<route_token>`
- `routes_del_confirm:<uid>:<draft_token>:<confirm_token>`
- `routes_del_cancel:<uid>:<draft_token>`

The backend owns draft tokens and maps route tokens to full route identifiers.

## User Messages

Messages must be compact and stable in Telegram:

- No alignment that depends on monospace width.
- Each route preview uses labels on separate lines.
- Blocking overlaps appear before warnings.
- Every failure tells the user the next safe action: choose another tunnel,
  edit targets, cancel, refresh routes, or run diagnostics.

## Tests

Agent tests:

- Domain exact/suffix overlap.
- CIDR containment and IP-to-prefix normalization.
- Disabled duplicate warning.
- Add plan is read-only.
- Add apply refuses after fresh blocking overlap.
- Delete plan is read-only.
- Delete apply refuses when preview hash no longer matches.
- HR-Neo restart happens only after HydraRoute-backed DNS add/delete.

Backend tests:

- Callback parsing for add/delete tokens.
- Draft TTL and wrong-user/topic rejection.
- Preview rendering for allowed, warning, and blocked states.
- Confirm buttons are hidden when add has blocking overlaps.
- Delete preview shows route kind, backend, bind, and targets without spacing
  drift.

## Implementation Boundaries

Do not add force delete/add in version 1.
Do not perform bulk deletion in version 1.
Do not update existing routes as part of add/delete.
Do not trust backend cached route data for safety decisions.

## Self Review

No placeholders remain. The design covers add, delete, overlap detection,
HR-Neo preference, awg-manager fallback, Telegram callback constraints, safety
rules, and tests. The scope is large enough for parallel implementation but
still a single routing feature because all commands share one route safety
model and one Telegram panel.
