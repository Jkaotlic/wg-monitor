# 2026-06-09 Bot Offline And Route Template Fix Session

## Scope

Fixed two live bot regressions reported on June 9:

- heartbeat offline notices had no way to silence or mute follow-up notifications;
- adding routes from an AWG Manager template on `testkeen` failed with
  `DNS_ROUTE_CREATE_ERROR` because AWG Manager rejected stale/iface-only
  `tunnelId` values such as `nwg3`.

## Root Causes

### Offline heartbeat notices

`Dispatcher.SendOffline` sent a plain Telegram message, not a HARD-alert style
message with inline controls. The heartbeat watcher also had its own renotify
map and did not consult `incident_state`, so even if controls were added, a
silenced `agent_heartbeat` row would not suppress future offline sends.

### Template route add

The route-add path had already been hardened to refresh the routing interface
from `/api/routing/tunnels`, but DNS route creation still wrote the same value
to both `routes[0].interface` and `routes[0].tunnelId`. On the live AWG Manager
path, `interface=nwg*` is valid while `tunnelId=nwg*` can be rejected as an
unknown tunnel. Managed tunnel DNS creates now keep the fresh `interface` and
use a stable managed/NDMS bind ID for `tunnelId`.

## Fixes

- Offline heartbeat notices are sent with the standard inline alert controls for
  `agent_heartbeat`, including silence and mute.
- `SendOffline` persists an `agent_heartbeat` HARD row so the existing callback
  actions have state to update.
- The heartbeat watcher skips offline sends while `agent_heartbeat` is silenced
  or acked, and clears that HARD state when a fresh heartbeat arrives.
- DNS route create uses `routeDNSBindID` for managed tunnels, preserving the
  fresh `interface` while avoiding stale or iface-only `tunnelId` values.

## Local Verification

Used local Go cache:

`$env:GOCACHE='C:\Users\User\Documents\wg-monitor\.gocache'`

Commands passed:

- `go test ./internal/backend/alerts -run TestSendOffline_HappyPath -count=1`
- `go test ./internal/backend/heartbeat -run TestWatcherSkipsOfflineWhenAgentHeartbeatSilenced -count=1`
- `go test ./internal/backend/heartbeat -run TestWatcherClearsOfflineHardWhenHeartbeatFresh -count=1`
- `go test ./internal/agent/actions -run 'TestRouteAddJSON_(ManagedCreateUsesFreshIfaceAndStableTunnelID|RefreshesRoutingIfaceWhenRoutingIDDiffersFromTunnelID|RefreshesAndUsesRoutingIfaceForCreate|AWGTemplateNDMSBindsSelectedRoutingInterface|AWGTemplateHRNeoBindsSelectedRoutingInterface)' -count=1`
- `go test ./internal/agent/actions -count=1`
- `go test ./internal/backend/alerts -count=1`
- `go test ./internal/backend/heartbeat -count=1`
- `go test ./internal/backend/callbacks -count=1`
- `go test ./... -count=1`
- `git diff --check`

## Release State At Handoff

Before release continuation, public backend health was:

`{"status":"ok","version":"v0.13.0-rc117"}`

Next expected RC: `v0.13.0-rc118`.

## Release And Live Rollout

Published release:

- Tag: `v0.13.0-rc118`
- Commit: `80da3ab5` - `fix(bot): silence offline alerts and route templates`
- GitHub Actions release run: `27206714519` - success
- GitHub release: `draft=false`, `prerelease=true`
- Verified assets include `checksums.txt`, `wg-monitor-backend-linux-arm64`,
  and `wg-monitor-agent-linux-mipsle`.
- Public release mirror verified:
  - `/v1/releases/download/v0.13.0-rc118/checksums.txt` -> HTTP 200
  - `/v1/releases/download/v0.13.0-rc118/wg-monitor-agent-linux-mipsle` -> HTTP 200

Backend rollout:

- `POST /v1/wizard/backend/deploy` with target `v0.13.0-rc118` returned
  HTTP 202 and `{"accepted":true}`.
- Pi backend update runner completed with:
  `{"target_version":"v0.13.0-rc118","status":"ok"}`.
- Public `/healthz` after update:
  `{"status":"ok","version":"v0.13.0-rc118"}`.

Agent rollout:

- `testkeen` self-update to `v0.13.0-rc118` was verified by command ACK and
  follow-up heartbeat.
- The first direct self-update attempt omitted the public-host headers, so the
  agent tried `https://192.168.0.87/v1/releases/download/...` and failed with
  connection refused. Re-enqueueing with:
  - `X-WG-Public-Proto: https`
  - `X-WG-Public-Host: wgmonitor.example.com`
  produced the correct public release mirror and succeeded.
- Live agent table after rollout:
  - `testkeen`: `v0.13.0-rc118`, current.
  - `client-i`, `client-a`, `client-b`, `del`, `gachimikhail`, `client-c`:
    still `v0.13.0-rc117`.
  - `client-e`, `client-f-old`, `client-g`: still pending older rc117
    mobile/offline updates.

For the reported symptoms, the live boundary is now:

- Offline heartbeat alert controls are fixed by the backend update to rc118.
- The reported template-route failure on `testkeen` is fixed by the verified
  `testkeen` agent update to rc118.
