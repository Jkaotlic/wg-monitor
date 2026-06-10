# VPS Dashboard Design

**Date:** 2026-06-10
**Status:** Approved direction: Option A, built-in admin dashboard MVP
**Owner:** Anex + Codex

## Goal

Build a protected web dashboard served by `wg-monitor-backend` on the VPS so the operator can monitor routers, deploy backend or agent updates, and run the safe operational actions that are currently available through the Telegram bot or deploy wizard.

The first slice is an admin-only dashboard for the current workspace. It must reuse existing backend, wizard, and command-queue contracts instead of creating a second operational brain next to Telegram.

## Non-goals

- No multi-workspace or multi-customer UI in the first slice. The design leaves room for workspace selection later, but this MVP manages the current backend instance only.
- No separate `wg-monitor-dashboard` service or binary. The dashboard is served by the existing backend process.
- No one-click destructive actions in the first slice. Tunnel import/delete, route add/delete, firmware install, token rotation, and router reboot require a later confirm-screen and audit-log design.
- No secret editing in the browser. Bot tokens, wizard tokens, AWG Manager credentials, agent tokens, and backup passphrases stay in files, environment variables, or the existing deploy secret store.
- No replacement for Telegram topics. Telegram remains the alerting and human-notification surface; the dashboard is the VPS-side operator console.

## Existing Project Context

Current code already has the right primitives:

- `internal/backend/handler.go` wires `/healthz`, `/readyz`, agent `/v1/report`, command long-poll `/v1/cmd`, command result `/v1/cmd/result`, and protected `/v1/wizard/*` endpoints.
- `internal/backend/wizard_handler.go` exposes authenticated fleet sync, agent enrollment, agent self-update dispatch, backend deploy request enqueueing, allowlisted operational commands, and command-result polling.
- `internal/backend/cmd/queue.go` is the shared in-memory command channel used by Telegram callbacks and wizard endpoints.
- `internal/backend/callbacks` already encodes important safety behavior: action allowlists, Telegram ACLs, confirm tokens for dangerous operations, and result rendering.
- `internal/backend/db` stores users, deploy metadata, last-seen timestamps, and active incident state.
- The release/deploy lane already depends on the public backend host headers for backend and agent deploy requests, so dashboard endpoints must preserve the same public-host behavior instead of inventing a new download URL path.

## UI Kit Decision

Use Tabler as the visual baseline for the MVP dashboard.

Why Tabler:

- It is a popular open-source admin dashboard UI kit built on Bootstrap 5.
- It provides responsive layouts, tables, badges, cards, button groups, dark mode, and a large icon set that fit an operations console.
- It can be used as static HTML/CSS/JS, which matches the current Go backend and avoids introducing a required Node/Vite/React runtime for the first slice.
- It looks more modern than classic Bootstrap admin templates while still being practical for dense operational screens.

Alternatives considered:

- AdminLTE: very popular and battle-tested, but its default visual language is more traditional and heavier than the desired modern operator console.
- CoreUI: strong component library and open-source admin template, but it is more framework-oriented and heavier than needed for a static embedded MVP.

Implementation constraint:

- Do not load Tabler, Bootstrap, icons, or fonts from a CDN at runtime. Vendor the required compiled assets into the backend static bundle, keep license notices, and serve them from `/dashboard/assets/*`.
- The first implementation may use vanilla JavaScript with Tabler/Bootstrap classes. A frontend framework can be added later only if the UI state becomes too complex for small focused modules.

## Architecture

The dashboard adds a small backend module under `internal/backend/dashboard` plus a Tabler-based static web app embedded into the backend binary.

Backend responsibilities:

- Serve `GET /dashboard/` and static dashboard assets only when dashboard auth is configured.
- Expose JSON API under `/v1/dashboard/*`.
- Authenticate dashboard requests with a dedicated dashboard token, separate from agent tokens and the wizard token.
- Build dashboard summary data from the existing database repositories.
- Dispatch operational actions through the same command queue used by wizard endpoints.
- Reuse the same backend deploy request file path already used by `POST /v1/wizard/backend/deploy`.

Frontend responsibilities:

- Render a polished Tabler-based operator dashboard as the first screen.
- Poll summary data on a short interval.
- Let the operator select a router and run safe actions.
- Show command lifecycle: queued, waiting, ok, err, locked, timeout, result output.
- Keep offline routers, active incidents, pending deploys, and backend health visible at all times.
- Use attractive, consistent icon buttons, status badges, segmented filters, drawers, toasts, and compact tables instead of bare HTML controls.

The backend remains the source of truth. The browser does not compute router health from raw events; it renders the summary the backend returns.

## Configuration

Add an optional dashboard config block to backend config:

```yaml
dashboard:
  enabled: true
  token_file: /etc/wg-monitor/dashboard-token.txt
```

Behavior:

- If `dashboard.enabled` is false or omitted, dashboard routes are not registered.
- If `dashboard.enabled` is true but `token_file` is empty or unreadable, backend startup must fail closed with a clear error.
- The token is read from the file at startup.
- The token is accepted as `Authorization: Bearer <token>` for JSON API calls.
- `GET /dashboard/` may serve the login shell without auth, but protected data and actions must require the token.
- The token must never be rendered into HTML, logged, or returned by an endpoint.

The first implementation can use browser local storage to remember the token client-side. A later hardening pass may add an HTTP-only session cookie, CSRF protection, and token rotation.

## Routes

Dashboard routes are registered only when dashboard config is valid.

```text
GET  /dashboard/
GET  /dashboard/assets/*
GET  /v1/dashboard/summary
POST /v1/dashboard/agents/{nickname}/commands
POST /v1/dashboard/agents/{nickname}/deploy
POST /v1/dashboard/backend/deploy
GET  /v1/dashboard/cmd/{cmd_id}?nickname=<nickname>&wait_sec=<0..60>
```

`GET /v1/dashboard/summary` returns:

```json
{
  "backend": {
    "status": "ok",
    "version": "v0.13.0",
    "dashboard_enabled": true
  },
  "agents": [
    {
      "nickname": "testkeen",
      "kind": "static",
      "online": true,
      "last_seen_at": "2026-06-10T12:30:00Z",
      "agent_version": "v0.13.0",
      "last_deployed_version": "v0.13.0",
      "pending_version": "",
      "pending_since": "",
      "deploy_mode": "awgm",
      "awgm_url_configured": true,
      "has_topic": true,
      "active_hard_count": 0
    }
  ],
  "incidents": [
    {
      "nickname": "alyaba",
      "check_name": "dns",
      "hard_since": "2026-06-10T12:00:00Z",
      "fail_count": 4
    }
  ]
}
```

The exact JSON shape may include additional fields if they come from existing database metadata, but the fields above are required for the MVP UI.

`POST /v1/dashboard/agents/{nickname}/commands` accepts:

```json
{
  "action": "diag_now",
  "args": {
    "check_name": "_dashboard"
  }
}
```

Allowed actions in the MVP:

- `diag_now`
- `force_recheck`
- `check_via_tunnel`
- `check_direct`
- `pingcheck_now`
- `pingcheck_status`
- `router_doctor`
- `route_status`
- `tunnels_status`
- `service_restart` with `{"name":"awgmgr"}` only

Disallowed in the MVP:

- `tunnel_delete`
- `tunnel_import`
- `route_add`
- `route_delete`
- `route_rebind`
- `firmware_install`
- `self_update` through the generic command endpoint
- token or enrollment changes

Agent deploy uses the dedicated endpoint:

```json
{
  "target_version": "v0.13.0"
}
```

Backend deploy uses the dedicated endpoint:

```json
{
  "target_version": "v0.13.0"
}
```

Both deploy paths must reuse the same public backend URL and release mirror behavior as the wizard deploy endpoints.

## Safety Model

The dashboard has stronger blast radius than Telegram because it centralizes many controls in one browser. The MVP keeps safety narrow:

- Dedicated dashboard token, not the wizard token.
- Read-only summary is protected.
- All actions are allowlisted.
- Dangerous actions are omitted in the first slice.
- Backend deploy and agent deploy use dedicated endpoints with explicit `target_version`.
- Generic command endpoint rejects `self_update`.
- `service_restart` is limited to AWG Manager restart.
- Request bodies are capped to the same small JSON size class as wizard requests.
- Results are correlated by command id and nickname.
- API responses must use structured JSON errors.
- Static assets must not expose local filesystem paths, config contents, or secrets.

## User Interface

The first screen is the working dashboard, not a landing page.

Visual baseline:

- Base the UI on Tabler's dashboard layout: `.page`, top navbar, `.page-wrapper`, responsive grid, tables, cards where they frame actual widgets, badges, offcanvas/drawer patterns, and Tabler Icons.
- Default to a refined dark operator theme with a light-mode toggle if cheap to support from Tabler's built-in theming.
- Use a restrained but distinctive operations palette: deep graphite background, clean white/gray table surfaces, green for healthy, amber for pending/warning, red for hard failures, and cyan/blue only for informational actions.
- Keep the memorable visual cue as the fleet table: each router row gets a strong status rail or badge stack so an operator can scan the fleet in seconds.
- Use polished buttons with icons and short labels for actions. Examples: diagnostics, refresh, route status, tunnel status, pingcheck, direct/via checks, AWG Manager restart, deploy.
- Use icon-only buttons only for universally recognizable controls and add tooltips. Dangerous or deploy actions keep text labels.
- Use Tabler toasts or alerts for command dispatch feedback and a dedicated command-result panel for command output.
- Use compact, consistent spacing. The dashboard must feel like a high-quality control room, not a plain generated CRUD table.

Layout:

- Header: backend version, backend health, dashboard connection state, refresh timestamp.
- Left/main: fleet table with compact rows.
- Top filters: all, offline, hard incidents, pending deploy, stale version.
- Right drawer: selected router details and action buttons.
- Bottom or side panel: recent command results for this browser session.

Fleet row signals:

- nickname
- kind
- online/stale/offline derived from last seen
- active hard count
- agent version
- last deployed version
- pending version
- last seen time
- deploy mode

Router drawer actions:

- Run diagnostics
- Force recheck
- Routes status
- Tunnels status
- PingCheck status
- Check via tunnel
- Check direct
- Restart AWG Manager
- Deploy agent version

Backend action:

- Deploy backend version

Visual direction:

- Beautiful, dense, utilitarian, operations-focused.
- No marketing hero, no decorative sections, no nested cards.
- Broken state stays visible.
- Buttons use Tabler Icons where practical; labels stay short and explicit.
- Text must fit on mobile and desktop.
- The implementation must be visually checked in a browser before completion. A passing API test is not enough for the UI slice.

## Data Flow

Summary:

1. Browser calls `GET /v1/dashboard/summary` with dashboard auth.
2. Backend reads users, active hard incidents, current backend version, and deploy metadata.
3. Backend returns a stable JSON summary.
4. Browser renders fleet rows and incident badges.

Command:

1. Operator clicks a safe action.
2. Browser posts to `/v1/dashboard/agents/{nickname}/commands`.
3. Backend validates auth, nickname, action allowlist, and action-specific args.
4. Backend enqueues `wire.Command` through the existing `CommandSink`.
5. Browser receives `202 {"cmd_id":"..."}`.
6. Browser polls `/v1/dashboard/cmd/{cmd_id}?nickname=...&wait_sec=...`.
7. Agent posts result through existing `/v1/cmd/result`.
8. Browser displays the result output.

Agent deploy:

1. Operator enters or selects target release tag.
2. Browser posts to `/v1/dashboard/agents/{nickname}/deploy`.
3. Backend enqueues `self_update` through the same path as wizard agent deploy.
4. Backend marks pending deploy metadata.
5. Browser follows command result and subsequent summary refresh to see the version flip.

Backend deploy:

1. Operator enters target release tag.
2. Browser posts to `/v1/dashboard/backend/deploy`.
3. Backend writes the existing backend-update JSON request file.
4. The existing backend update runner performs the swap.
5. Browser sees backend version change after refresh.

## Error Handling

- Missing or invalid dashboard token returns `401`.
- Dashboard disabled returns `404` for dashboard API routes because routes are not registered.
- Unsupported command returns `400 unsupported_command`.
- Unknown router nickname returns `404 user_not_found`.
- Command sink unavailable returns `503`.
- Command result not ready returns `404 result_not_ready`, matching wizard behavior.
- Backend update queue unavailable returns `503`.
- Public host missing for deploy returns `400` with guidance to call the public dashboard URL or preserve forwarded host headers.

## Testing Strategy

Backend tests:

- Dashboard routes are absent when config is disabled.
- Dashboard startup fails closed when enabled with unreadable token file.
- Bearer auth accepts the correct token and rejects missing or wrong tokens.
- Summary returns users plus active incidents without exposing secrets.
- Command endpoint accepts each MVP-safe action.
- Command endpoint rejects disallowed actions, including generic `self_update`.
- `service_restart` accepts only `awgmgr`.
- Agent deploy delegates to the same self-update behavior as wizard deploy.
- Backend deploy writes the same backend update request shape as wizard backend deploy.
- Command result endpoint mirrors wizard polling semantics.

Frontend/static tests:

- Embedded dashboard index is served.
- Static assets are served with stable paths.
- The UI can render an empty fleet, offline routers, active incidents, pending deploy, command success, and command failure.

Verification commands:

```text
go test ./internal/backend -count=1
go test ./cmd/backend ./internal/backend ./internal/backend/db -count=1
go test ./... -count=1
git diff --check
```

## Rollout Plan

1. Land the dashboard behind disabled-by-default config.
2. Add backend template support for `dashboard.enabled` and `dashboard.token_file`.
3. Add deploy wizard or documentation steps to create `/etc/wg-monitor/dashboard-token.txt` with restrictive permissions.
4. Deploy to the active backend.
5. Verify `/healthz`, `/readyz`, `GET /dashboard/`, unauthorized `GET /v1/dashboard/summary`, authorized summary, one safe command, and one command result.
6. Keep Telegram as the fallback operational surface.

## Future Slices

Later work can add:

- Multi-workspace selector for one VPS hosting several isolated backend instances.
- Confirm-screen and audit log for destructive actions.
- Token rotation from deploy wizard.
- Live event stream or server-sent events for command progress.
- Release tag discovery from GitHub or the backend release mirror.
- Read-only public status page with no operational controls.
