# 2026-06-08 Router Update And Routes Fix Session

## Scope

Fixed local code regressions for:

- bot fleet router self-update
- route add target interface resolution
- HydraRoute route rebind/status attribution

Initial local verification was completed before release/deploy continuation.

## Root Causes

### Bot self-update

The deploy wizard uses the backend release mirror:

`<public backend>/v1/releases/download`

The Telegram fleet update path only queued `self_update` with `version`, so agents
fell back to their default GitHub release base. On routers with broken GitHub DNS
or reachability, bot-triggered updates could fail while wizard-triggered updates
worked.

### Routes

Route target resolution preferred `/api/routing/tunnels`, but managed tunnel
metadata and routing metadata can use different IDs:

- managed tunnel ID: `awg12`
- routing/NDMS ID: `Wireguard3`
- fresh route bind iface: `nwg5`

When the managed `/api/tunnels/get` response still carried a stale
`interfaceName`, route add/rebind/status could bind or count rules against the
wrong iface.

## Fixes

- Added backend `public_base_url` config and deploy template rendering.
- Passed `public_base_url` into Telegram callbacks.
- Bot fleet self-update now queues `repo_base=<public_base_url>/v1/releases/download`
  when configured.
- Route target resolution now merges managed tunnel identity/default-route
  metadata with the fresh iface from `/api/routing/tunnels`.
- Route status now maps fresh routing aliases back onto the managed tunnel ID
  instead of classifying them as Other/WAN/system.

## Verification

Used local Go cache:

`$env:GOCACHE='C:\Users\User\Documents\wg-monitor\.gocache'`

Commands passed:

- `go test ./internal/agent/actions -run "TestRouteStatus_CreditsFreshRoutingIfaceToManagedTunnelWhenRoutingIDDiffers|TestRouteAddJSON_RefreshesRoutingIfaceWhenRoutingIDDiffersFromTunnelID|TestRouteRebind_UsesRoutingIfaceWhenRoutingIDDiffersFromTunnelID" -count=1`
- `go test ./internal/backend/callbacks -run TestPanelUpdateAll_UsesBackendReleaseMirror -count=1`
- `go test ./internal/backend -run TestLoadConfigTrimsPublicBaseURL -count=1`
- `go test ./cmd/deploy -run TestRenderBackendYAML -count=1`
- `go test ./internal/agent/actions ./internal/backend/callbacks ./internal/backend ./cmd/backend ./cmd/deploy -count=1`
- `go test ./... -count=1`
- `go vet ./internal/agent/actions ./internal/backend/callbacks ./internal/backend ./cmd/backend ./cmd/deploy`
- `git diff --check`

## Deployment Note

For the live bot update fix to take effect, the running backend config must include:

`public_base_url: "https://wgmonitor.anexaev.crazedns.ru"`

The deploy template now writes this for future backend installs/rewrites, but a
binary-only backend self-update will not rewrite an existing `backend.yaml`.

## Release Continuation

The intended release target for this continuation is `v0.13.0-rc117`.
