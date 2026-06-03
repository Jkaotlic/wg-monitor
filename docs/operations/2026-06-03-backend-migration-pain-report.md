# 2026-06-03 wg-monitor backend migration pain report

## Scope

Emergency migration of `wg-monitor` backend/frontend from the failed public VPS path to the home Raspberry Pi 4 behind KeenDNS/Keenetic, then fleet-wide agent retargeting from the old backend URL to:

`https://wgmonitor.anexaev.crazedns.ru`

No secrets are recorded in this report.

## Final State Observed

- New backend is running on the home Raspberry Pi 4 in Docker.
- Public health endpoint works: `/healthz` returned version `v0.13.0-rc71`.
- Keenetic HTTP proxy terminates public HTTPS on 443 and forwards `wgmonitor` to the Pi backend on port 8080.
- Backup archive from 2026-06-02 was restored and backend DB became usable.
- `testkeen` was retargeted through local Entware SSH and restarted.
- `del`, `alyaba`, `de4ddy`, and `puzirek` were retargeted through AWG Manager terminal.
- `router4car4`, `bronya`, and `caredns-oldcar` were queued as pending/offline mobile jobs.

## Main Incidents And Pain Points

### 1. Backend hosting boundary was unclear under pressure

The practical target was not a normal replacement VPS but a home RPi4 behind Keenetic/KeenDNS. That created a different deploy shape:

- reverse proxy is on Keenetic, not Caddy on the host;
- the public entrypoint depends on KeenDNS/proxy state;
- Pi backend should expose only local/LAN port 8080;
- public correctness is `https://wgmonitor.anexaev.crazedns.ru/healthz`, not just a local container health check.

Improvement:

- Add a first-class `home-keenetic-backend` deploy profile.
- Preflight should verify DNS, public 443, Keenetic proxy target, Docker container, `/healthz`, and restored DB in one command.

### 2. Backup restore was too manual

The restore worked, but required manual discovery and validation of the archive, container paths, compose state, and DB health.

Improvement:

- Add `deploy backend restore-backup --archive <tgz> --target docker-compose`.
- Command should unpack safely, verify SQLite integrity, back up current DB, restore ownership/modes, restart backend, then check `/healthz` and a sample agent row.

### 3. Fleet backend URL migration has no clean one-command path

Retargeting agents required different access paths:

- direct Entware SSH for reachable local router;
- AWG Manager terminal for public routers;
- deferred pending jobs for sleeping mobile routers.

Temporary one-off helpers were needed during the migration. These should become supported deploy tooling, not emergency code.

Improvement:

- Add `deploy agents retarget-backend --old-url ... --new-url ... --fleet`.
- Per-agent strategy should be explicit: `local-ssh`, `awgm-terminal`, `deferred-awgm`, `skip-offline`.
- Output should be a table: nickname, access path, before URL, after URL, restart result, heartbeat freshness.

### 4. AWG Manager public TLS is messy for nested subdomains

Several AWG Manager URLs use names like `awg.router4car4.crazedns.ru` or `awg.caredns.netcraze.link`. Their certificates were wildcard certs for one label only, so Go TLS verification failed for two-label subdomains.

Observed pending-job failures:

- `router4car4`: certificate valid for `*.crazedns.ru`, not `awg.router4car4.crazedns.ru`.
- `caredns-oldcar`: certificate valid for `*.netcraze.link`, not `awg.caredns.netcraze.link`.
- Windows `curl -k` hid this during manual checks; Go client correctly rejected it.

Improvement:

- Add an explicit deploy flag/env for emergency insecure AWG Manager TLS, with loud warning.
- Prefer fixing DNS/cert naming so the normal client stays strict.
- Deferred jobs should record `tls_cert_mismatch` separately from auth/network/offline errors.

### 5. Deferred mobile jobs need better offline/error classification

Pending jobs were installed on the Pi and cron-driven, but logs showed three distinct failure classes:

- offline/service unavailable;
- DNS NXDOMAIN;
- TLS hostname mismatch.

Current pending runner treats these mostly as retryable failures.

Improvement:

- Add structured deferred job state: `pending`, `offline`, `dns_error`, `tls_error`, `auth_error`, `patched`, `failed_permanent`.
- Add `deploy deferred status` to summarize jobs and latest log reason.
- Support per-job scheme override (`http` vs `https`) and TLS policy.

### 6. `bronya` hostname drift

The deferred job for `bronya` resolved `awg.router4car2.crazedns.ru` as NXDOMAIN from the Pi. This may be a wrong hostname, disabled mobile WAN, stale DNS, or HTTP-only route.

Improvement:

- Before scheduling deferred jobs, preflight should run DNS resolution from the backend host and from the operator PC, then record both results.
- For mobile/offline routers, distinguish "device sleeping" from "hostname does not exist".

### 7. Wizard API cannot dispatch arbitrary operational commands

The backend wizard routes can list/update agents and enqueue limited deploy/maintenance actions. There was no obvious supported HTTP endpoint for "run this safe agent command now" such as `tunnels_status`, `tunnel_enable`, `tunnel_restart`, or `diag_now`.

This forced direct AWG Manager operations for `puzirek`.

Improvement:

- Add authenticated wizard/admin endpoint:
  - `POST /v1/wizard/agents/{nickname}/commands`
  - body: action plus validated args
  - returns command id
  - `GET /v1/wizard/cmd/{cmd_id}` polls result
- Persist command queue/results across backend restarts. Current in-memory command queue is weak during migrations.

### 8. `puzirek` was not offline, but had real tunnel failure

`puzirek` agent was alive and reporting to the new backend. The failing checks were:

- `tunnel_awg11`
- `dns`

AWG Manager showed:

- `awg10`: disabled
- `awg11`: disabled initially
- HydraRoute: installed and running

Actions taken:

- Started `Wireguard1` via NDMS equivalent.
- Then started `awg11` through AWG Manager `POST /api/control/start?id=awg11`.
- `awg11` moved to `enabled=true` and `status=starting`, but did not reach handshake/running.
- One restart through `POST /api/control/restart?id=awg11` still left it in `starting`.

Router-side diagnostics:

- WAN default route exists through `eth3`.
- Ping to `89.105.210.226` from the router had 100% packet loss.
- NDMS showed `Wireguard1` state up but link down, online no, RX zero, TX increasing.
- NDMS showed remote endpoint address as `127.0.0.1`, which is suspicious for this tunnel unless AWG Manager/nativewg intentionally proxies it.

Conclusion:

`puzirek` did not fall off because of the backend migration. The agent path is alive. The remaining fault is the AWG tunnel/server/config path for `awg11`, which also explains DNS failure when DNS policy depends on that tunnel.

Improvement:

- Smart reply should clearly separate "agent alive, backend OK" from "tunnel disabled" and "tunnel enabled but no handshake".
- Add AWG Manager control actions to deploy tooling for emergency `start/restart/status` without Telegram UI.
- Add tunnel diagnostics that capture endpoint, NDMS link state, handshake age, RX/TX, WAN route, and endpoint reachability.

### 9. Telegram/bot/front auth risk during emergency

During the incident, frontend authorization was disabled temporarily. This is acceptable only if the public surface exposes no sensitive control actions and the reverse proxy is tightly scoped.

Risk:

- Public dashboard without auth can leak router names, statuses, topology, and operational timing.
- If any control endpoints are reachable without auth, impact is much higher.

Improvement:

- Keep public frontend read-only if auth is off.
- Add emergency auth modes:
  - `readonly-public`
  - `basic-auth`
  - `telegram-admin-only`
- Deploy preflight should print whether public control endpoints are protected.

## Code/Tooling Changes From This Session

### `cmd/deploy/awgm_client.go`

Useful fixes made during the migration:

- `AWGM_INSECURE_TLS=1` allows emergency AWG Manager access where public TLS hostname is known broken.
- Terminal marker parsing now uses the last marker occurrence, because AWG Manager terminal echoes submitted script text before command output. Using the first marker can parse the echoed script instead of the actual result.

These changes should get tests before release.

## Follow-Up Backlog

1. Implement first-class backend restore/migration command for Docker-on-RPi/Keenetic.
2. Implement fleet backend URL retarget command with structured per-agent strategies.
3. Add deferred job status command and structured failure classes.
4. Add wizard/admin command dispatch endpoint for safe agent commands.
5. Persist command queue/results across backend restarts.
6. Add AWG Manager control/status commands to deploy tooling.
7. Add tunnel diagnostic bundle for disabled/starting/no-handshake states.
8. Improve Smart Reply classification for agent-alive/tunnel-dead cases.
9. Add TLS/DNS preflight for AWG Manager public URLs.
10. Add explicit frontend auth mode checks to deploy doctor.

## Current Open Items

- `router4car4`, `bronya`, and `caredns-oldcar` remain pending/offline or blocked by DNS/TLS issues.
- `puzirek` agent is alive, but `awg11` remains enabled/starting without handshake.
- Pi SSH access from this operator session returned permission errors during later log checks; backend public health was still verified separately earlier.
