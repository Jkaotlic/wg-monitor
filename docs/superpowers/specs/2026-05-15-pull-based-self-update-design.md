# Pull-based self-update — design

**Дата:** 2026-05-15
**Статус:** approved by user, fast-track implementation (без TDD по запросу)

## Проблема

`actionUpdateAgent` использует SSH wizard'а → роутера на `192.168.31.1:222`. У всех Keenetic LAN-адрес одинаков, у оператора SSTP-туннели до нескольких роутеров плюс собственная локалка тоже в 192.168.31.0/24. Из-за коллизии маршрутов TCP-handshake падает по таймауту "через раз". Симптом: `✗ 192.168.31.1:222 не отвечает за 3с`.

## Решение

Перенести update-flow с SSH wizard → роутер на backend → agent через уже существующий long-poll-канал команд (`/v1/cmd`, `/v1/cmd/result`). Wizard теперь дёргает только HTTPS на VPS — никаких прямых SSH-сессий к роутерам для обновления версии.

Cold install (новый роутер) остаётся через SSH — там агента ещё нет, pull-канал недоступен.

## Поток

```
wizard          backend (VPS)             agent (router)
  │                  │                          │
  │ POST /v1/wizard/agents/<nick>/deploy
  │  {target_version:"v0.13.0-rc1"}             │
  ├─────────────────▶                           │
  │              Enqueue(uid, Command{          │
  │                ID:uuid, Action:"self_update",│
  │                Args:{version}})              │
  │ 202 {cmd_id}    │                          │
  ◀──────────────────                          │
  │                  │ ◀── PollCommand (long)
  │                  │ ─── Command{self_update}─▶
  │                  │                          │
  │                  │                  ┌──────────────────┐
  │                  │                  │ 1. download .new │
  │                  │                  │ 2. sha256 verify │
  │                  │                  │ 3. spawn detached│
  │                  │                  │    swap script    │
  │                  │                  │ 4. return ok      │
  │                  │                  └──────────────────┘
  │                  │ ◀── PostResult(ok, "swap scheduled")
  │                  │                          │
  │ GET /v1/wizard/cmd/<cmd_id>                 │
  │  (long-poll AwaitResult)                    │
  ├─────────────────▶                           │
  │ 200 {status:ok,output:"..."}                │
  ◀──────────────────                          │
  │                  │                  ┌──────────────────┐
  │                  │                  │ swap script:     │
  │                  │                  │  kill self →     │
  │                  │                  │  mv .new bin →   │
  │                  │                  │  S99 start       │
  │                  │                  └──────────────────┘
  │                  │ ◀── Heartbeat(agent_version=v0.13.0-rc1)
  │                  │     UPDATE users.last_deployed_version
  │                  │                          │
  │ GET /v1/wizard/agents (poll)                │
  ├─────────────────▶                           │
  │ {agents:[{last_deployed_version:"v0.13.0-rc1",...}]}
  ◀──────────────────                          │
  │                  │                          │
  │ "✅ <nick> подтвердил v0.13.0-rc1"          │
```

## Компоненты

### Wire (`pkg/wire/types.go`)
- Добавить `"self_update": true` в `validCommandActions`. Wire-формат `Command.Args = {"version": "v0.13.0-rc1"}`.

### Agent (`internal/agent/actions/self_update.go` — новый файл)
- Function `SelfUpdate(ctx, args) (status, output string)`:
  1. Достать `version` из args. Если пусто → err.
  2. Определить arch через `uname -m` → "arm64"|"mipsle"|err.
  3. Сформировать GitHub URL: `https://github.com/Jkaotlic/wg-monitor/releases/download/<version>/wg-monitor-agent-linux-<arch>` + `checksums.txt`.
  4. Скачать оба файла (HTTP, агент уже ходит в инет для opkg).
  5. Распарсить checksums.txt, найти sha256 для своего asset name.
  6. Скачанный бинарь хешировать SHA256, сверить.
  7. Записать в `/opt/bin/wg-monitor.new`, chmod 755.
  8. Сгенерить `/tmp/wg-monitor-swap.sh`:
     ```sh
     #!/bin/sh
     sleep 3
     /opt/etc/init.d/S99wg-monitor stop 2>/dev/null
     killall -9 wg-monitor 2>/dev/null
     sleep 1
     cp -p /opt/bin/wg-monitor /opt/bin/wg-monitor.bak 2>/dev/null
     mv /opt/bin/wg-monitor.new /opt/bin/wg-monitor
     chmod 755 /opt/bin/wg-monitor
     /opt/etc/init.d/S99wg-monitor start
     ```
  9. Запустить `nohup sh /tmp/wg-monitor-swap.sh >/dev/null 2>&1 &`.
  10. Return `("ok", "v0.13.0-rc1 verified, swap scheduled in ~3s")`.

  Любая ошибка до шага 9 → return `("err", "<reason>")`, агент остаётся работать на старом бинаре.

- В `runner.go::dispatchWithPayload` добавить `case "self_update"`.

### Backend (`internal/backend/wizard_handler.go` + `handler.go`)

**Новые endpoint'ы** (защищены `WizardAuthMiddleware`):

`POST /v1/wizard/agents/{nickname}/deploy`
- Body: `{"target_version": "v0.13.0-rc1"}`
- Lookup user by nickname; 404 если нет.
- Сгенерить UUID для cmd.ID.
- `d.CommandSink.Enqueue(uid, wire.Command{ID, Action:"self_update", Args:{"version":...}, IssuedAt:now})`.
- Return 202 `{"cmd_id": "<uuid>"}`.

`GET /v1/wizard/cmd/{cmd_id}?nickname=<nick>&wait_sec=<n>`
- Lookup user; resolve uid.
- `d.CommandSink.AwaitResult(ctx, uid, cmd_id, wait_sec)` (default 30s, max 60s).
- Если timeout → 408. Иначе 200 с CommandResult JSON.

**Регистрация маршрутов** в `NewMux` под `d.WizardToken != ""` блоком.

**CommandSink расширение:** добавить `Enqueue` и `AwaitResult` в интерфейс (уже есть в `*cmd.Queue`).

**Heartbeat → last_deployed_version sync** (`handler.go::reportHandler`):
- После успешного ingest'а: если `rep.AgentVersion != ""`, вызвать `d.DB.Users().UpdateLastSeenAgentVersion(uid, rep.AgentVersion)` (новый метод). Метод делает UPDATE только если значение поменялось — избегаем лишних writer-mutex hit'ов.

### DB (`internal/backend/db/users.go`)
- Новый метод `UpdateLastSeenAgentVersion(uid int64, version string) error`:
  ```sql
  UPDATE users SET last_deployed_version = ?
  WHERE id = ? AND COALESCE(last_deployed_version, '') != ?
  ```

### Wizard (`cmd/deploy/vps_sync.go` + `update_components.go`)

**VPSClient методы:**
- `Deploy(ctx, nickname, version) (cmd_id string, err error)`
- `AwaitCommandResult(ctx, nickname, cmd_id, timeout) (*wire.CommandResult, error)`

**`runOneUpdate` маршрутизация:**
- Для агента, если: (a) есть `state.Wizard.Token`, (b) есть `state.Backend.Domain`, (c) `t.InstalledVersion != ""` (значит агент когда-то деплоился и pull-канал жив) → новый pull-flow:
  1. POST deploy → получили cmd_id.
  2. AwaitCommandResult (45s timeout).
  3. Если status=err → print, fallback (offer SSH path).
  4. Если status=ok → polling `ListAgents` каждые 5s до 120s, проверяя что `LastDeployedVersion == target_version`.
  5. Если flipped → success. Если timeout → warn + offer SSH fallback.
- Иначе (cold install / no wizard token) → существующий `actionUpdateAgent` (SSH).

## Что НЕ делаем в этой итерации

- Canary rollout (пин один за раз, проверять heartbeat, потом остальные) — пока ручной. Оператор выбирает 1 роутер в меню, ждёт ack, потом следующий.
- DB-persisted `target_version` pin (агент офлайн при попытке деплоя → команда умрёт из памяти при рестарте backend'а). Acceptable trade-off: оператор просто повторит [2] через wizard.
- Rollback автоматический (если новая версия задеплоилась но heartbeat не пришёл) — оставляем SSH-fallback для recovery.
- Юнит-тесты — пропущены по запросу пользователя ради скорости. Smoke-test на testkeen после деплоя.

## Безопасность

- Self-update качает с GitHub Release официальной репы (`Jkaotlic/wg-monitor`). Sha256 verify против `checksums.txt` обязателен — без него ничего не пишем в `/opt/bin/wg-monitor.new`. Github не подписывает релизы криптографически, но HTTPS + checksums.txt из той же ассет-группы покрывает MITM на pull-канале.
- `self_update` команду может выслать только тот, у кого есть `WIZARD_TOKEN`. Токен лежит в `/etc/wg-monitor/wizard-token.txt` mode 640 root:wgmonitor на VPS. Не агент, не TG-бот.

## Smoke на testkeen после имплементации

1. Wizard: меню [2] → `agent testkeen` → подтвердить. Должен сыпануть POST /deploy, дождаться ok, дождаться flip версии.
2. На роутере проверить `/opt/bin/wg-monitor.bak` существует.
3. TG топик testkeen → heartbeat watchdog не должен сработать (gap < `mobile_sleep_after_sec`).
4. Включить `WG_NO_PULL=1` env или удалить wizard token → должен fallback'нуть на SSH-путь.
