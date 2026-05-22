# Deploy wg-monitor

`wg-monitor-deploy` is the canonical installer and operator wizard. The normal router path is AWG Manager/KeenDNS + Entware bootstrap; direct router SSH is only a break-glass recovery path.

## Prerequisites

| Item | Needed for |
| --- | --- |
| Linux amd64 VPS | Backend, SQLite state, Caddy/TLS reverse proxy |
| Domain for VPS | Public HTTPS backend URL |
| Telegram bot | Alerts and control UI |
| Telegram forum group | One topic per router |
| Keenetic router | KeeneticOS 4/5 with Entware installed |
| AWG Manager | Publicly reachable through KeenDNS or another HTTPS domain |

## First VPS Install

1. Download the deploy wizard from <https://github.com/Jkaotlic/wg-monitor/releases>.
2. Run `wg-monitor-deploy`.
3. Choose `[1] VPS / backend`.
4. Enter VPS host, SSH auth, domain, Telegram bot token, chat ID, and admin user ID.
5. The wizard installs backend service files, config, Caddy route, and backend enrollment API.

After install, the wizard records backend version and deploy time in `wizard.toml`.

The backend install also enables `wg-monitor-backup.timer`. Every day at
05:00 Europe/Moscow it sends the admin user a private Telegram document with a
small recovery bundle: SQLite `state.db`, rendered `backend.yaml`, agent
inventory CSV, and a manifest. Bot and wizard token files are not copied into
the archive. If the bundle grows past the Telegram upload safety limit, the bot
sends a warning and leaves the archive on the VPS under
`/var/lib/wg-monitor/backups/`.

## Add A Router

Use `[3] Routers`, then the add/re-enroll action.

The wizard asks for:

- Router nickname.
- Telegram topic ID.
- Public AWG Manager URL.
- AWG Manager API key, or web login/password fallback.
- Entware terminal login/password when the terminal bridge needs credentials.

Flow:

1. Wizard creates or refreshes backend enrollment on VPS.
2. Wizard authenticates to AWG Manager.
3. Wizard opens the AWG Manager terminal websocket.
4. Bootstrap script downloads the matching agent binary from GitHub release.
5. Agent config and Entware init service are installed.
6. Backend receives heartbeat and confirms the version.

No SSTP/WireGuard connection from the operator machine to the router LAN is required for this path.

## Move Old Routers To A New VPS

Use `[4] Move to new VPS`.

This is the recovery path when the old VPS is dead and existing routers must be attached to the replacement backend:

1. Install backend on the new VPS with `[1]`.
2. Make sure every old router has AWG Manager reachable through its public domain.
3. Run `[4]`.
4. For each router, provide/confirm AWG Manager credentials.
5. The wizard creates a fresh enrollment and re-runs Entware bootstrap with the new backend URL/token.

If a raw `WG_AGENT_TOKEN_<NICK>` still exists locally, the wizard can preserve it. If not, it safely re-enrolls the agent with a new token and updates the backend hash.

## Restore From Telegram Backup

Use `[7] Restore / Disaster Recovery` or:

```bash
wg-monitor-deploy restore-backup <archive.tgz> --dry-run
wg-monitor-deploy restore-backup <archive.tgz> --to-current-vps
wg-monitor-deploy restore-backup <archive.tgz> --to-new-vps
```

Dry-run extracts the archive locally and shows the manifest, backend version,
SQLite size, and agent count. Restore mode uploads `state.db` and
`backend.yaml`, makes timestamped backups of any existing VPS files, checks
SQLite integrity, restores ownership/modes, starts `wg-monitor-backend`, and
refreshes the daily Telegram backup timer.

`--to-new-vps` bootstraps the new host first: `wgmonitor` user, systemd units,
backend binary from the current release, Caddy route, bot token from the local
secret store, wizard token, and the backup timer. If the backend domain changes,
use `[4] Move to new VPS` afterwards to rewrite agents through AWG Manager.

## Update Components

Use `[2] Update components`.

The wizard compares `wizard.toml`, backend `/healthz`, and the latest GitHub release. Static agents use the backend-mediated pull-flow where possible, so the operator does not need direct router SSH for normal updates.

## Doctor And Sync

- `[5] Doctor` checks local state, VPS reachability, backend health, and known agents.
- `[6] Sync from VPS` refreshes local `wizard.toml` from backend state.
- Startup sync is best-effort and quiet for normal offline/timeouts; only auth problems are shown loudly.

## Local Files

- `wizard.toml` - non-secret local state: backend, routers, versions, deploy timestamps.
- Local secret store / env vars - passwords, API keys, wizard token, raw agent tokens.
- `WG_LEGACY_ROUTER_SSH=1` - exposes legacy SSH recovery helpers in the service menu.

Do not commit real `wizard.toml`, tokens, router passwords, or local probe captures.
