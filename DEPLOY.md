# Deploy wg-monitor

`wg-monitor-deploy` is the canonical installer and operator wizard. The normal router path is AWG Manager/KeenDNS + Entware bootstrap; direct router SSH is only a break-glass recovery path.

## Prerequisites

| Item | Needed for |
| --- | --- |
| Linux amd64 VPS | Backend, SQLite state, Caddy/TLS reverse proxy |
| Domain for VPS | Public HTTPS backend URL |
| Telegram bot | Alerts and control UI |
| Keenetic router | KeeneticOS 4/5 with Entware installed |
| AWG Manager | Publicly reachable through KeenDNS or another HTTPS domain |

## First VPS Install

1. Download the deploy wizard from <https://github.com/Jkaotlic/wg-monitor/releases>.
2. Run `wg-monitor-deploy`.
3. Choose `[1] VPS / backend`.
4. Enter VPS host, SSH auth, domain, Telegram bot token, and admin user ID.
5. The wizard installs backend service files, config, Caddy route, and backend enrollment API.

After install, the wizard records backend version and deploy time in `wizard.toml`.

The backend install also enables `wg-monitor-backup.timer` and
`wg-monitor-backup-verify.timer`. Every night the backend makes two encrypted
archives (`.tgz.enc`): a **small** one (everything needed to restore the system
except the event history) that is sent to the admin user as a private Telegram
document, and a **full** one (with the history) that stays on disk and is copied
to an off-site server when one is configured. Both contain SQLite `state.db`,
`backend.yaml`, bot, wizard and dashboard token files, the JSON stores (VPN
panels, own servers, cabinet keys), agent inventory CSV, a
manifest, and an encrypted operator vault when the wizard has pushed one. The agent revive key (`revive.key`) is deliberately **not** in the backups. Raw
deploy secrets are not stored on the backend in plaintext; the vault is
encrypted with the same backup password. Details: [Encrypted Nightly
Backups](#encrypted-nightly-backups).

The wizard generates `WG_BACKUP_PASSPHRASE`, saves it in the local secret store,
uploads it to the backend as `backup-passphrase.txt` with strict permissions, and
shows it to the operator for password-manager storage.

## VPS Dashboard

`wg-monitor-backend` can serve optional browser control at `/dashboard/`:

- `/dashboard/` — the mini app itself in a regular browser (no Telegram needed);
  sign in with the dashboard token or a personal link issued from the mini app
  «Настройки». The browser session acts as the configured Telegram admin.
- `/dashboard/rescue/` — emergency page for when the app bundle does not load:
  one self-contained response (no external resources, strict CSP), token sign-in,
  backend version and fleet counts, and backend rollout to another version
  (with explicit downgrade opt-in). Old `/dashboard/classic/…` bookmarks redirect here.

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
(or `/dashboard/rescue/` if the app does not load) and paste the token there. The backend validates it and
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

Ключ **не входит** в ночные архивы (малый и полный) — ни отдельным файлом, ни
внутри другого (это проверяют тесты `TestRunBackupCommandDoesNotCarryReviveKey` и
`TestBackupReviveKeyFollowsSwitch`). Иначе утёкший бэкап расшифровывал бы
сохранённые пароли роутеров. Цена: после восстановления из бэкапа
сохранённых паролей роутеров не расшифровать — их нужно ввести заново
(ожидающие оживления закроются, как при потере ключа). Манифест архива и вывод
`backup verify` говорят об этом прямо. Ключ надо хранить отдельно и самому;
положить его рядом с копией базы — значит свести шифрование на нет. Решение
обратимо одним выключателем `includeReviveKey` в `cmd/backend/backup_archive.go`
(тесты параметризованы по нему), но по умолчанию он выключен.

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
install the agent straight from web control: open `/dashboard/` → «Парк» →
«Добавить роутер» for a new router, or «Переустановить агент» on an existing
router row. Enter the AWG Manager auth and the router root password when asked.
The backend re-mints the enrollment token, resolves the latest stable version
(or a version you choose), and drives the AWG Manager terminal to download the
agent, write `config.yaml`, install the init service, and start it. Credentials
are used once and never stored.

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
wg-monitor-deploy restore-backup <archive.tgz.enc> --dry-run
wg-monitor-deploy restore-backup <archive.tgz.enc> --to-current-vps
wg-monitor-deploy restore-backup <archive.tgz.enc> --to-new-vps
```

The archive may be a small or a full one, in the old (v1) or the streaming (v2)
encryption format, or a legacy unencrypted `.tgz`; for encrypted archives the
wizard takes `WG_BACKUP_PASSPHRASE` from the local secret store or asks for it.

Dry-run extracts the archive locally and shows the manifest, backend version,
SQLite size, agent count, which stores are inside, and that `revive.key` is not in the backup.
Restore mode uploads `state.db` and `backend.yaml`, plus (v0.53+ archives) the
JSON stores (and `revive.key` if an archive happens to carry one); makes timestamped backups of any existing VPS
files, checks SQLite integrity, restores ownership/modes, starts
`wg-monitor-backend`, and refreshes the backup timers. Stores land where the
backend with the restored `backend.yaml` looks for them (next to `state.db`
unless a path is set explicitly), a `revive.key` found in an archive at
`revive.key_file`; all with
mode `0600`. After a restore the saved router passwords must be entered again
(the revive key is not part of the backup). Destinations outside `/var/lib/wg-monitor` and `/etc/wg-monitor`
are refused. The uploaded copies in `/tmp/wg-monitor-restore` are removed
whether the restore succeeds or not.

This flow is for the VPS (systemd) layout. For the Docker layout see
[Восстановление из малого архива](#восстановление-из-малого-архива).

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

`backup install` installs or repairs the backend timers/services (nightly backup
and weekly restore check) and makes sure a password exists locally and on the
backend. `backup run` starts the backup job immediately. `backup push-secrets`
encrypts local `secrets.env` plus `wizard.toml` into `operator-secrets.tgz.enc`
and uploads only that encrypted vault to the backend. `backup status` also
prints `backup-status.json`.

Legacy unencrypted `.tgz` archives are still handled by `restore-backup`.

### Что делает ночной бэкап (v0.53)

Служба `wg-monitor-backup.service` (таймер — 05:00 МСК) одним запуском делает два
архива в каталоге `--out-dir`:

| Архив | Имя | Что внутри | Куда уходит |
|---|---|---|---|
| малый | `wg-monitor-small-backup-<время UTC>.tgz.enc` | всё для восстановления системы, **кроме истории**: строки таблиц `events`, `daily_soft_flaps`, `awgm_ping_runs`, `alert_messages` не переносятся (схема остаётся) | админу в личку Telegram |
| полный | `wg-monitor-full-backup-<время UTC>.tgz.enc` | вся база с историей | остаётся на диске; копируется на внешний сервер, если он настроен. В Telegram не отправляется никогда |

В обоих: `state.db`, `backend.yaml`, токены бота, мастера и дашборда, четыре
JSON-хранилища (`amnezia-premium.json`, `amnezia-selfhosted.json`,
`awg3-panels.json`, `hidemyname.json` — те, что есть), `agents.csv`,
`manifest.txt` (с числом роутеров, владельцев и операторов на момент бэкапа),
хранилище секретов оператора. **`revive.key` в архивах нет** — по правилу,
ключ живёт отдельно.

Провал одного вида не отменяет другой; служба завершается с ошибкой, если не
удался хотя бы один. Малый архив больше лимита Telegram (47 МБ) — это ошибка
прогона, а не тихий пропуск. Полный архив пишется потоком: память процесса не
зависит от размера базы (формат шифрования v2, блоками по 1 МиБ).

Запуск руками:

```bash
wg-monitor-backend backup --config … --passphrase-file … --out-dir … --kind small   # или full, both (по умолчанию)
```

**Хранение.** После удачной записи нового архива лишние старые удаляются —
только файлы с именами архивов этого вида, чужие файлы в каталоге не трогаются.
Малый: по одному архиву за 7 последних дней и за 4 предыдущие недели
(`--small-keep-daily 7 --small-keep-weekly 4`). Полный: за 3 последних дня
(`--full-keep-daily 3 --full-keep-weekly 0`) — он большой и лежит на том же
диске, что и база. `--keep-daily N --keep-weekly M` задают правило сразу для
всех видов в этом запуске; свой флаг вида сильнее общего. Архивы на внешнем
сервере не чистятся — пока руками.

### Внешняя цель для полного архива

Полный архив на том же диске, что и база, — не копия на случай смерти диска.
Чтобы он уходил на другой сервер по `scp`:

1. На бэкенде под пользователем, от которого работает служба бэкапа, создать
   ключ без пароля: `ssh-keygen -t ed25519 -N '' -f ~/.ssh/wg-monitor-offsite`.
2. Положить открытый ключ (`~/.ssh/wg-monitor-offsite.pub`) в
   `~/.ssh/authorized_keys` пользователя на сервере-приёмнике и создать там
   каталог для архивов.
3. Один раз проверить руками с бэкенда — заодно сервер попадёт в `known_hosts`:
   `scp -B -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 -i ~/.ssh/wg-monitor-offsite /etc/hostname user@host:/path/`
4. В `wizard.toml`, секция `[backend]`:

   ```toml
   backup_offsite_scp = "user@host:/path/"
   backup_offsite_key = "/home/<пользователь>/.ssh/wg-monitor-offsite"
   ```

   и `wg-monitor-deploy backup install` — служба получит флаги
   `--offsite-scp user@host:/path/ --offsite-key <файл ключа>`. В значениях не
   должно быть пробелов. Без мастера — дописать эти два флага в `ExecStart`
   юнита руками и сделать `systemctl daemon-reload`.

Провал копирования — ошибка прогона (`"offsite":"error"` в файле состояния);
сам архив при этом на диске остаётся. Ключ передаётся `scp` путём к файлу, его
содержимое служба не читает и не печатает. При первом соединении ключ сервера
запоминается (`accept-new`); если он потом сменится, копирование начнёт падать —
это защита, а не поломка: проверить сервер и поправить `known_hosts`.

В VPS-раскладке служба работает под `wgmonitor` с `ProtectHome=true`: ключ и
`known_hosts` в домашнем каталоге ей не видны. Ключ класть в
`/etc/wg-monitor/` (владелец `wgmonitor`, права `0600`), а шаг 3 выполнять от
`wgmonitor` — и убедиться по `journalctl -u wg-monitor-backup`, что копирование
прошло.

### Файл состояния `backup-status.json`

Лежит рядом с базой (`state.db`), переписывается целиком после каждого прогона
бэкапа и проверки; секретов в нём нет, права `0644` (его читает бэкенд из
контейнера). Его читает бэкенд, чтобы показать состояние бэкапа в мини-аппе.

```json
{
  "version": 1,
  "small":  {"last_ok_at": "2026-10-02T02:00:41Z", "last_run_at": "2026-10-02T02:00:41Z", "ok": true,
             "size_bytes": 4193280, "file": "wg-monitor-small-backup-20261002T020003Z.tgz.enc",
             "telegram": "ok", "error": ""},
  "full":   {"last_ok_at": "2026-10-01T02:07:10Z", "last_run_at": "2026-10-02T02:06:55Z", "ok": false,
             "size_bytes": 198246400, "file": "wg-monitor-full-backup-20261002T020003Z.tgz.enc",
             "telegram": "off", "offsite": "error", "error": "архив не скопирован на внешний сервер: …"},
  "verify": {"last_run_at": "2026-09-27T03:31:02Z", "ok": true, "error": "", "routers": 14}
}
```

- `last_run_at` — когда вид запускался в последний раз (UTC); пусто — ещё ни разу.
- `ok` — последний прогон прошёл целиком: архив записан и ушёл всюду, куда настроен.
- `last_ok_at` — последний такой прогон; неудачный его не трогает. По нему видно,
  сколько времени нет годного бэкапа.
- `file`, `size_bytes` — архив последнего прогона; пусто и 0 — архив не записан.
  `ok: false` с непустым `file` значит «архив на диске есть, но не доставлен».
- `telegram`, `offsite` — `ok`, `error` или `off` (не настроено или не положено
  этому виду: полный в Telegram не ходит, у малого нет внешней цели).
- `error` — короткая причина без секретов.
- `verify` — итог последней проверки восстановления; `routers` — сколько роутеров
  в проверенном архиве.

### Проверка восстановления

`wg-monitor-backup-verify.timer` раз в неделю (воскресенье, 06:30 МСК) запускает

```bash
wg-monitor-backend backup verify --config … --passphrase-file … --out-dir …
```

Команда берёт самый свежий малый архив, расшифровывает его во временный каталог
внутри `--out-dir` (каталог убирается при любом исходе) и проверяет: база
проходит `PRAGMA integrity_check`; число роутеров, владельцев и операторов
равно записанному в манифест при бэкапе; каждое хранилище из строки `stores=`
манифеста есть в архиве и разбирается как JSON (сверка с манифестом, а не с
живыми файлами). Ключа оживления в архиве нет, и
проверка его не требует; вывод напоминает, что после восстановления пароли
роутеров вводятся заново. Итог пишется в секцию `verify`
файла состояния; код выхода не ноль при провале.

Счётчики сверяются с записанными в манифест при сборке архива, а не с живой
базой: роутер, добавленный между ночным бэкапом и недельной проверкой, проверку
не валит. Провал «в архиве операторов 1, в манифесте архива 2» значит, что база
в архиве не та, что была снята.

### Восстановление из малого архива

Малый архив восстанавливается так же, как полный; разница одна — в нём нет
истории событий, поэтому после восстановления экраны пусты до первого отчёта
агентов (до минуты), а графики и журнал начинаются заново.

**VPS-раскладка (systemd)** — мастером, архив взять из лички Telegram:

```bash
wg-monitor-deploy restore-backup wg-monitor-small-backup-<время>.tgz.enc --dry-run
wg-monitor-deploy restore-backup wg-monitor-small-backup-<время>.tgz.enc --to-current-vps   # или --to-new-vps
```

**Докер-раскладка (Raspberry Pi)** — руками. Пути ниже — от корня раскладки
(`~/wg-monitor`); парольная фраза лежит в `secrets/backup-passphrase.txt` (на
пустой машине — создать этот файл с фразой из менеджера паролей, права `0600`).
Контейнер бэкенда на время замены файлов должен быть остановлен — тем же
способом, каким он запущен на площадке (`docker compose stop` или
`docker stop <имя>`).

```bash
cd ~/wg-monitor
bin/wg-monitor-backend backup extract --archive <архив.tgz.enc> \
  --passphrase-file secrets/backup-passphrase.txt --to data/restore-tmp
sqlite3 data/restore-tmp/state.db 'PRAGMA integrity_check;'      # должно ответить ok

# остановить контейнер бэкенда
[ -f data/state.db ] && cp -p data/state.db data/state.db.bak.$(date +%Y%m%d)
rm -f data/state.db-wal data/state.db-shm
install -m 600 data/restore-tmp/state.db data/state.db
for f in amnezia-premium.json amnezia-selfhosted.json awg3-panels.json hidemyname.json; do
  [ -f data/restore-tmp/$f ] && install -m 600 data/restore-tmp/$f data/$f
done
# на пустой машине -- ещё конфиг и токены из того же каталога:
#   backend.yaml -> config/; bot-token.txt, wizard-token.txt, dashboard-token.txt -> secrets/
# запустить контейнер бэкенда
rm -rf data/restore-tmp
```

Хранилища кладутся туда, где их ищет бэкенд: рядом с базой, если в
`backend.yaml` путь не задан явно. Ключа оживления в архиве нет: свой `revive.key`
верните вручную из места, где вы его храните (иначе включите оживление заново, а
сохранённые пароли роутеров введите снова).
Владелец файлов — тот же, что у остальных файлов в `data/` и `secrets/`.
`backup extract` читает оба формата шифрования и оба вида архивов; в непустой
каталог не разворачивает, а при битом архиве не оставляет половины файлов.

## Update Components

Use `[2] Update components`.

The wizard compares `wizard.toml`, backend `/healthz`, and the latest GitHub release. Static agents use the backend-mediated pull-flow where possible, so the operator does not need direct router SSH for normal updates.

## Telegram Menu Operations

English:

- The backend clears the bot's command menu at every start in all three scopes it ever used: default, the admin's private chat and (when `telegram.chat_id` is still present in the config) that group's admin member scope. There are no slash commands left.
- The chat menu button is re-applied at every start: with `public_base_url` on HTTPS it is a `web_app` button opening `<public_base_url>/miniapp/`. Without HTTPS the button is left untouched — there is no command list behind it any more.
- `telegram.chat_id` and `telegram.extra_chat_ids` are optional. They are used once at startup to clear the group's command menu and are shown in the dashboard summary; nothing else reads them. A group can be deleted by hand after the rollout.

Русский:

- Бэкенд на каждом старте стирает меню команд во всех трёх областях, в которые когда-либо его ставил: по умолчанию, личка админа и (если `telegram.chat_id` ещё есть в конфиге) админ-участник этой группы. Слеш-команд не осталось.
- Кнопка меню ставится заново на каждом старте: при `public_base_url` по HTTPS это `web_app`-кнопка на `<public_base_url>/miniapp/`. Без HTTPS кнопку не трогаем — списка команд за ней больше нет.
- `telegram.chat_id` и `telegram.extra_chat_ids` необязательны: они нужны один раз на старте, чтобы стереть меню команд в группе, и показываются в сводке дашборда. Группу можно удалить руками после раскатки.

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
