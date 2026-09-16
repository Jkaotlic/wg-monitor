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

`wg-monitor-backend` can serve optional browser control at `/dashboard/`:

- `/dashboard/` — the mini app itself in a regular browser (no Telegram needed);
  sign in with the dashboard token or a personal link issued from the mini app
  «Настройки». The browser session acts as the configured Telegram admin.
- `/dashboard/classic/` — the legacy dashboard described below, kept until its
  functions move into the app.

The legacy dashboard uses the same backend command queue and deploy endpoints as the wizard:
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

Restart `wg-monitor-backend` and open `https://<backend-domain>/dashboard/`
(or `/dashboard/classic/login` for the legacy dashboard) and paste the token there. The backend validates it and
sets an `HttpOnly`, `SameSite=Strict` `wg_dashboard_session` cookie; the browser
does not store the dashboard token in local storage. The JSON dashboard API also
continues to accept `Authorization: Bearer <token>` for scripted operator calls.
If `enabled: true` is set but the token file is missing or empty, the backend
refuses to start.

## Agent Revive Key (оживление агента)

Оживление переустанавливает агента на роутере, который был выключен: админ один раз
вводит пароль root или вход в панель роутера в мини-аппе, бэкенд хранит его
зашифрованным до успеха, отмены или срока и затем стирает. Шифрование — AES-256-GCM,
ключ лежит **отдельным файлом**, а не в базе: база или ночной бэкап без этого файла
паролей не раскрывают. Без ключа бэкенд работает как раньше, а экран отвечает
«оживление не настроено на сервере».

> Создание ключа и перезапуск — изменение продакшена. Выполнять только с явного «да»
> оператора.

Docker-раскладка (Pi): каталог бэкенда содержит `config/backend.yaml`, `secrets/`,
`data/`, `docker-compose.yml`; внутри контейнера `secrets/` смонтирован как `/secrets`.
**Перед записью ключа проверить монтирование `secrets/` в `docker-compose.yml`** — если
том не смонтирован или смонтирован не туда, ключ уйдёт мимо контейнера, и после
перезапуска бэкенд его не найдёт:

```bash
cd <каталог бэкенда>
grep -n secrets docker-compose.yml    # ждём строку вида "./secrets:/secrets"
```

```bash
cd <каталог бэкенда>
umask 077
head -c 32 /dev/urandom | base64 > secrets/revive.key
chmod 600 secrets/revive.key
ls -l secrets/            # владелец revive.key должен совпадать с соседними файлами токенов
```

Если владелец отличается от остальных файлов в `secrets/`, выровнять его по соседу
(`sudo chown --reference=secrets/<файл токена бота> secrets/revive.key`).

В `config/backend.yaml`:

```yaml
revive:
  key_file: /secrets/revive.key
```

Перезапустить контейнер бэкенда (`docker compose restart` в каталоге бэкенда) и
проверить журнал:

```bash
docker logs wg-monitor-backend 2>&1 | grep -i оживлен
```

Строка `оживление агента включено` — ключ принят. Строка `оживление агента выключено`
с причиной — файл не найден или длина ключа не 32 байта.

VPS-раскладка (systemd): тот же файл в `/etc/wg-monitor/revive.key`

```bash
sudo install -o wgmonitor -g wgmonitor -m 600 /dev/null /etc/wg-monitor/revive.key
head -c 32 /dev/urandom | base64 | sudo tee /etc/wg-monitor/revive.key >/dev/null
```

`key_file: /etc/wg-monitor/revive.key`, `sudo systemctl restart wg-monitor-backend`,
проверка — `sudo journalctl -u wg-monitor-backend -n 200 | grep -i оживлен`.

Ключ **не входит** в ночной зашифрованный бэкап (это проверяет тест
`TestRunBackupCommandDoesNotCarryReviveKey`). Копировать ключ рядом с бэкапом базы
нельзя — это сводит шифрование на нет.

**Ключ потерян или испорчен** (файла нет, права не те, длина не 32 байта):
`newReviveService` возвращает `nil`, функция выключена целиком (экран отвечает
«оживление не настроено на сервере»), `Service.Run` и `Recover` не выполняются вовсе.
Несмотря на это, решение оператора «затем стирается» продолжает действовать: бэкенд
без ключа всё равно раз в час (и один раз сразу на старте) проходит по базе
беспарольным сторожем (`revive.RunJanitor`, ключ ему не нужен — он ничего не
расшифровывает) и переводит намерения, чей срок истёк, в `expired`, стирая
зашифрованный секрет. Непросроченные намерения сторож не трогает — они дожидаются
возврата ключа (заново пройденных шагов выше на том же файле) или своего срока.
Отменить их из мини-аппа без ключа нельзя: отмена отвечает «оживление не настроено
на сервере», а строка оживления в «Парке» не показывается. После возврата ключа
`Recover` при следующем старте подхватит их как обычно, и отмена снова работает. Замена ключа на новый (файл существовал, но его перезаписали другим) ведёт
себя иначе: с НОВЫМ ключом функция включена сразу, но старые (зашифрованные под
прежний ключ) секреты не расшифровываются — такие намерения закрываются на первой же
попытке запуска как «пароль на сервере не расшифровывается — поставьте оживление
заново».

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

### From the dashboard (no wizard machine needed)

For a router that already has AWG Manager reachable on its public domain, you can
install the agent straight from the dashboard: open the agent's drawer →
**Recovery** → **Deploy to router**. Enter the AWG Manager auth (api-key or
login/password) and the router root password. The backend re-mints the enrollment
token, resolves the latest stable version (or a version you type), and drives the
AWG Manager terminal to download the agent, write `config.yaml`, install the init
service, and start it. Credentials are used once and never stored. The router must
already be enrolled (Add agent) with its `awgm_url` set.

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
- The backend also sets the bot chat menu button on every start. With `public_base_url` on HTTPS it becomes a `web_app` button opening the mini app at `<public_base_url>/miniapp/`; without it (or on plain HTTP, which Telegram refuses to open as a web app) it falls back to Telegram's `commands` menu. Either way the button is re-applied at startup, so a button set by hand in BotFather does not survive a restart — change `public_base_url` instead.
- The default operator scope intentionally excludes admin-only commands, so operators on desktop clients see only topic-safe actions.
- Router-topic menus are generated from the same menu registry as reply keyboards, compat inline keyboards, slash commands, and operator help.
- `/menu` and `/keyboard` re-send both menu surfaces in the active router topic: first the bottom reply keyboard, then the visible inline fallback.

Русский:

- Backend при старте регистрирует две поверхности команд: обычную операторскую и scoped admin-команды.
- Кнопка меню приватного чата на каждом старте ставится заново: при `public_base_url` по HTTPS это `web_app`-кнопка «Открыть приложение» на `<public_base_url>/miniapp/`, иначе -- список команд. Кнопка, выставленная руками в BotFather, до следующего рестарта не доживёт. Топиков в группе это не касается: TG показывает кнопку меню только в приватном чате.
- В default scope нет админских команд, поэтому операторы в desktop-клиентах видят только безопасные действия текущего топика.
- Видимое меню топика строится из общего registry: из него же собираются reply keyboard, compat inline keyboard, slash-команды и операторская справка.
- `/menu` и `/keyboard` заново присылают актуальное меню в текущий топик роутера.

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
