# AWG Manager Bootstrap Deploy Design

Date: 2026-05-21

## Goal

Replace the normal router deployment mechanism with an AWG Manager + KeenDNS bootstrap flow.

The operator should no longer connect their own machine to a customer router over SSTP, WireGuard, or a local overlapping LAN. The deploy wizard stays the operational entry point, but its normal add/install/migrate/update path becomes VPS/backend mediated and router-public-web mediated.

## Non-Goals

- Keep the old local SSH router deploy as a normal menu option.
- Require a public router SSH port.
- Require the operator workstation to be routed into the customer LAN.
- Use AWG Manager as a long-lived remote shell.

## Current Problem

The existing cold install and migration flows assume direct SSH from the operator machine to the router. This fails operationally when many routers share `192.168.31.1`, when the operator forgets which VPN is active, or when the old VPS dies and existing agent tokens are missing.

Pull-based `self_update` already solves steady-state updates after an agent is alive. The missing piece is first install and re-enrollment without local router connectivity.

## Recommended Architecture

Use AWG Manager as the temporary HTTPS control surface into Entware:

1. The wizard receives a router nickname and the AWG Manager public URL, usually a KeenDNS web-app URL.
2. The wizard talks to the wg-monitor backend/VPS using the existing wizard token.
3. The backend creates a short-lived single-use enrollment token for the requested nickname.
4. The wizard authenticates to AWG Manager through `/api/auth/login`.
5. The wizard validates the target through AWG Manager API:
   - `/api/health`
   - `/api/system/info`
   - optional `/api/settings/get`
6. The wizard starts the AWG Manager terminal bridge only for the deploy window:
   - `/api/terminal/install`
   - `/api/terminal/start`
   - `/api/terminal/ws`
7. Over the terminal WebSocket, the wizard runs a small bootstrap script inside Entware:
   - detect architecture and `/opt` availability
   - download the release asset and `checksums.txt`
   - verify SHA-256
   - write `/opt/etc/wg-monitor/config.yaml`
   - install/update `/opt/etc/init.d/S99wg-monitor`
   - start or restart the agent
8. The wizard stops the terminal bridge with `/api/terminal/stop`.
9. The agent enrolls/reports to the backend. From this point all normal updates use existing backend-mediated `self_update`.

## UX Shape

There should not be a separate "deploy through AWG Manager" button.

The normal wizard actions change meaning:

- Add/install router: asks for AWG Manager URL and router credentials, not local router SSH coordinates.
- Migrate clients to new VPS: re-enrolls each selected router through AWG Manager when old tokens are absent.
- Update agents: uses the existing pull-flow whenever the agent is alive.
- Local SSH deploy: hidden break-glass path only, reachable by CLI flag or advanced recovery prompt, not by the normal menu.

The operator-facing story should be simple:

> Give me the router's AWG Manager URL and credentials. I will enter through KeenDNS, install the agent into Entware, close the terminal, and the router will take future updates from the VPS itself.

## State Model

Extend `AgentState` with public bootstrap metadata while preserving existing fleet fields:

- `deploy_mode`: `awgm` for the new default, `legacy_ssh` only for break-glass imported records.
- `awgm_url`: public AWG Manager base URL, for example `https://awg.myrouter.keenetic.pro`.
- `awgm_auth`: credential source name only, not the password value.
- keep `host`, `port`, and `user` for legacy/recovery compatibility until migration cleanup.

Secrets stay out of `wizard.toml` and remain in the deploy secret store:

- `WG_AWGM_LOGIN_<NICK>`
- `WG_AWGM_PASS_<NICK>`
- existing `WIZARD_TOKEN`
- generated `WG_AGENT_TOKEN_<NICK>` only after enrollment succeeds, if the wizard needs local recovery records.

## Backend Changes

Add wizard-auth endpoints for enrollment:

- `POST /v1/wizard/enrollments`
  - input: nickname, expected kind, optional thread id, optional target version
  - output: one-time enrollment token, backend URL, release version, checksum metadata
- `POST /v1/wizard/enrollments/{id}/complete`
  - called after the agent reports or after the wizard observes successful install
  - persists deployment metadata and clears pending state

The backend remains the canonical owner of user/token records. Re-enrollment creates a new raw token and token hash for the nickname when old token material is unavailable.

## Bootstrap Script Contract

The script must be idempotent:

- existing matching `agent.nickname` may be updated in place
- different existing nickname refuses overwrite unless a force/re-enroll confirmation exists
- failed download or checksum leaves the old binary/config untouched
- service restart happens only after config and binary are valid

The script must not print raw tokens after writing config.

## Error Handling

Failures should tell the operator which boundary failed:

- KeenDNS/AWG Manager not reachable
- AWG Manager auth failed
- Entware or `/opt` missing
- unsupported architecture
- terminal already active
- download/checksum failed
- agent installed but did not report to backend

When terminal is active from another session, the wizard should offer a retry/stop-terminal action only after confirming the current target nickname and URL.

## Security

- AWG Manager terminal is started only for bootstrap and stopped immediately afterward.
- Credentials are read from the secret store and never written to `wizard.toml`.
- Enrollment tokens are short-lived and single-use.
- Bootstrap verifies release checksums before swapping binaries.
- The wizard records the public AWG Manager URL and expected nickname to avoid cross-router mistakes.
- Legacy local SSH is hidden from normal operation to avoid returning to the overlapping-LAN failure mode.

## Migration Plan

For the current VPS migration:

1. Forget the bad `puzirek` record.
2. For `testkeen`, `de4ddy`, and `alyaba`, collect/confirm AWG Manager public URLs and credentials.
3. Re-enroll each router through AWG Manager, creating fresh backend token records on the new VPS.
4. Verify `last_seen_at`, `agent_version`, and command long-poll health through `/v1/wizard/agents`.
5. After all live routers are re-enrolled, remove or quarantine stale local SSH-only state.

## Testing

Unit tests:

- AWG Manager client login/session handling.
- terminal lifecycle request handling.
- bootstrap script rendering without token leakage.
- `AgentState` TOML round trip for new fields.
- migration planner prefers AWG Manager re-enroll over legacy SSH when tokens are missing.

Integration-style tests with local HTTP/WebSocket fakes:

- successful AWG Manager bootstrap from login to terminal stop.
- terminal busy response.
- auth failure.
- backend enrollment failure.
- install succeeds but heartbeat never arrives.

Manual/live verification:

- one controlled router with real AWG Manager.
- one re-enrollment against the new VPS.
- verify no operator-side VPN is needed during the whole flow.

## Rollout

Implement behind the normal wizard paths but default new installs/migrations to `deploy_mode = "awgm"`.

Keep legacy SSH code available during the first release as an advanced recovery path, then remove normal menu exposure and update help/doctor text so operators understand AWG Manager/KeenDNS is now the supported path.
