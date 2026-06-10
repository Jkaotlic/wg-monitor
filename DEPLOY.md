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

The backend install also enables `wg-monitor-backup.timer`. Every night it sends
the admin user a private Telegram document with an encrypted full backup
(`.tgz.enc`). The encrypted archive contains SQLite `state.db`, rendered
`backend.yaml`, bot and wizard token files, agent inventory CSV, a manifest, and
an encrypted operator vault when the wizard has pushed one. Raw deploy secrets
are not stored on the backend in plaintext; the vault is encrypted with the same
backup password.

The wizard generates `WG_BACKUP_PASSPHRASE`, saves it in the local secret store,
uploads it to the backend as `backup-passphrase.txt` with strict permissions, and
shows it to the operator for password-manager storage.

## VPS Dashboard

`wg-monitor-backend` can serve an optional local-assets dashboard at `/dashboard/`.
It uses the same backend command queue and deploy endpoints as the wizard:
fleet summary, safe agent commands, AWG Manager service restart, agent
self-update, backend-update queueing, and command-result polling.

The dashboard is disabled by default. To enable it on the VPS:

```bash
sudo install -o wgmonitor -g wgmonitor -m 600 /dev/null /etc/wg-monitor/dashboard-token.txt
sudo sh -c 'openssl rand -base64 32 > /etc/wg-monitor/dashboard-token.txt'
sudo chown wgmonitor:wgmonitor /etc/wg-monitor/dashboard-token.txt
sudo chmod 600 /etc/wg-monitor/dashboard-token.txt
```

Then set:

```yaml
dashboard:
  enabled: true
  token_file: /etc/wg-monitor/dashboard-token.txt
```

Restart `wg-monitor-backend` and open `https://<backend-domain>/dashboard/`.
Paste the token once; the browser stores it locally and sends it as a Bearer
token to `/v1/dashboard/*`. If `enabled: true` is set but the token file is
missing or empty, the backend refuses to start.

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

## Encrypted Nightly Backups

Use `[7] Backups` or:

```bash
wg-monitor-deploy backup status
wg-monitor-deploy backup install
wg-monitor-deploy backup run
wg-monitor-deploy backup push-secrets
wg-monitor-deploy backup password
wg-monitor-deploy backup restore <archive.tgz.enc>
```

`backup install` installs or repairs the backend timer/service and makes sure a
password exists locally and on the backend. `backup run` starts the backup job
immediately. `backup push-secrets` encrypts local `secrets.env` plus
`wizard.toml` into `operator-secrets.tgz.enc` and uploads only that encrypted
vault to the backend.

Legacy unencrypted `.tgz` archives are still handled by `restore-backup`.

## Update Components

Use `[2] Update components`.

The wizard compares `wizard.toml`, backend `/healthz`, and the latest GitHub release. Static agents use the backend-mediated pull-flow where possible, so the operator does not need direct router SSH for normal updates.

## Telegram Menu Operations

English:

- The backend registers two command surfaces at startup: the default operator command set and a scoped admin command set.
- The backend also sets the bot chat menu button to Telegram's `commands` menu, so clients show the blue `Menu` button next to the input bar when supported.
- The default operator scope intentionally excludes admin-only commands, so operators on desktop clients see only topic-safe actions.
- Router-topic menus are generated from the same menu registry as reply keyboards, compat inline keyboards, slash commands, and operator help.
- `/menu` and `/keyboard` re-send both menu surfaces in the active router topic: first the bottom reply keyboard, then the visible inline fallback.
- Admins can open `/panel` and use "Revive topics" to re-send both menu surfaces to every router topic that has a Telegram thread ID. The result screen reports sent, failed, and skipped-no-topic counts.

Русский:

- Backend при старте регистрирует две поверхности команд: обычную операторскую и scoped admin-команды.
- В default scope нет админских команд, поэтому операторы в desktop-клиентах видят только безопасные действия текущего топика.
- Видимое меню топика строится из общего registry: из него же собираются reply keyboard, compat inline keyboard, slash-команды и операторская справка.
- `/menu` и `/keyboard` заново присылают актуальное меню в текущий топик роутера.
- Админ может открыть `/panel` и нажать "Оживить топики": бот переотправит актуальное меню во все топики роутеров с Telegram thread ID и покажет счётчики отправлено / ошибок / без топика.

## Doctor And Sync

- `[5] Doctor` checks local state, VPS reachability, backend health, and known agents.
- `[6] Sync from VPS` refreshes local `wizard.toml` from backend state, including portable non-secret agent metadata: SSH deploy coordinates, arch, versions, rollout/pending state, last deploy time, deploy mode, AWG Manager URL/auth mode, and `expected_mac`.
- Startup sync is best-effort and quiet for normal offline/timeouts; only auth problems are shown loudly.
- Sync deliberately does not copy passwords, AWG Manager API keys, raw agent tokens, SSH private-key paths, or `preferred_iface`; those remain local or backup/recovery-only.

## Local Files

- `wizard.toml` - non-secret local state: backend, routers, versions, deploy timestamps.
- Local secret store / env vars - passwords, API keys, wizard token, raw agent tokens.
- `WG_LEGACY_ROUTER_SSH=1` - exposes legacy SSH recovery helpers in the service menu.

Do not commit real `wizard.toml`, tokens, router passwords, or local probe captures.
