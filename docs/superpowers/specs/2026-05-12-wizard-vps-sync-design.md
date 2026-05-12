# Wizard ⇄ VPS Sync — Design

**Date:** 2026-05-12
**Status:** Draft
**Targets:** v0.12.0 (next minor after v0.11.0-rc OPKG/operators line)

## Summary

Сегодня `wg-monitor-deploy` хранит весь свой state в локальном `wizard.toml`
(`%APPDATA%\wg-monitor-deploy\wizard.toml` на Windows). Если роутер
задеплоен с одного ПК, любой другой ПК про него ничего не знает и не может
ни увидеть его в меню, ни редеплоить агента без ручного ввода SSH-координат.

Сделать бэкенд (VPS) единственным источником правды о списке роутеров и их
deploy-метаданных. Локальный `wizard.toml` становится кэшем: при старте
визард тянет картину с VPS, после успешного деплоя пушит обратно. С любого
ПК после `wg-monitor-deploy` сразу видишь весь флот и можешь деплоить.

## Why now

- Реальный кейс: `alyaba` задеплоена с дом-ПК, на раб-ПК визард её не видит —
  пришлось бы перевбивать host/port/user/arch и рисковать дублем в DB.
- Все остальные «единые истины» уже на VPS (DB пользователей, версии агента
  через `version_audit` cache, ACL/operators) — wizard-state остаётся
  единственным split-truth артефактом.
- Под рукой `/healthz` JSON-эндпоинт (`internal/backend/handler.go:204`) и
  устоявшийся middleware-стек `reqID → auth → rate → handler` — есть куда
  пристроить.

## Non-Goals

- **Не сохраняем SSH-ключи на VPS.** Только координаты: host/port/user/arch.
  Приватные ключи остаются в `~/.ssh/` каждого ПК.
- **Никакой multi-tenancy.** Один глобальный `WIZARD_TOKEN` на инсталляцию —
  тот же, что подойдёт для любой админ-машины. Кто получил токен — тот
  владеет флотом, симметрично текущим админ-правам.
- **Без блокировок / lease / распределённого locking'а.** Если два ПК пишут
  одновременно — последний выигрывает. UX-сценарий «два админа одновременно
  деплоят одного `alyaba`» нереалистичен.
- **Без миграционной утилиты для существующих ПК.** Первый pull заполнит
  локальный `state.Agents` с пустыми SSH-полями для тех роутеров, что в DB
  есть, а у нас нет. Локально уже существующие записи остаются, обогащаются
  серверными полями при merge.
- **Минимум тестов.** Покрыть только: handler happy/auth, merge-функцию,
  push после `actionAddRouter`. Без обширного table-driven по edge cases.

## Architecture

```
┌─ wizard (любой ПК) ────────────────────────┐    ┌─ VPS backend ──────────────┐
│                                            │    │                            │
│ on start ──pull── GET /v1/wizard/agents ───┼───>│ users table                │
│                                            │    │ + ssh_host/port/user/arch  │
│ on deploy ──push── PUT /v1/wizard/agents/{nick}│ + last_deployed_version    │
│                                            │    │                            │
│ wizard.toml [cache]                        │    │ Auth: WIZARD_TOKEN env     │
└────────────────────────────────────────────┘    └────────────────────────────┘
```

### Data model — DB

В существующей таблице `users` добавляем 4 nullable колонки (SQLite ALTER
TABLE ADD COLUMN — non-blocking, NULL для существующих строк):

| Column | Type | Origin |
|---|---|---|
| `ssh_host`             | TEXT NULL | wizard на первом deploy |
| `ssh_port`             | INTEGER NULL | wizard |
| `ssh_user`             | TEXT NULL | wizard |
| `arch`                 | TEXT NULL | wizard |
| `last_deployed_version` | TEXT NULL | wizard после успешного деплоя |

Миграция: одна функция `ensureWizardSyncColumns(db)` в `internal/backend/db/`
с idempotent `ALTER TABLE … ADD COLUMN IF NOT EXISTS` (SQLite `PRAGMA
table_info` чек + ALTER). Вызывается из существующей `Migrate()` цепочки.

Эти поля **никогда не читает агент**. Они чисто wizard-side. ACL/auth-логика
бэка их игнорирует.

### Endpoints

Два новых, под общим префиксом `/v1/wizard/`:

#### `GET /v1/wizard/agents`

```json
{
  "agents": [
    {
      "nickname": "alyaba",
      "kind": "static",
      "thread_id": 4217,
      "ssh_host": "192.168.1.1",
      "ssh_port": 222,
      "ssh_user": "root",
      "arch": "mips",
      "last_deployed_version": "v0.10.3",
      "last_known_agent_version": "v0.10.3",
      "has_topic": true
    },
    ...
  ]
}
```

`last_known_agent_version` берётся из `simpleAuditCache` (то, что прислал
агент в последнем `version_audit`) — нужно показать визарду «реальную»
версию для сравнения с тем, что он сам когда-то деплоил.

#### `PUT /v1/wizard/agents/{nickname}`

```json
{
  "ssh_host": "192.168.1.1",
  "ssh_port": 222,
  "ssh_user": "root",
  "arch": "mips",
  "last_deployed_version": "v0.10.3"
}
```

Поведение: upsert deploy-полей в существующую строку `users` по nickname.
Если пользователя с таким nickname нет — 404. Создавать через этот PUT
нельзя (создание = существующий поток `actionAddRouter` → CLI `add-user`
на VPS, мы не дублируем).

Status: 204 No Content на успех. 4xx/5xx с JSON-envelope как остальные `/v1/*`.

### Auth

Новая env-переменная для бэкенда: `WIZARD_TOKEN` (обязательная для запуска,
если эндпоинты включены — feature-флаг по факту наличия токена).

Заголовок: `Authorization: Bearer <token>`. Middleware — лёгкий
`WizardAuthMiddleware`, проверяет `Bearer == os.Getenv("WIZARD_TOKEN")`
constant-time через `subtle.ConstantTimeCompare`. На пустой токен в env →
эндпоинты не регистрируются вообще (вижу как fail-closed).

Wizard читает токен из `WIZARD_TOKEN` env или из `secrets.env` кэша (по
существующему `SecretStore` паттерном, ключ `WIZARD_TOKEN`).

**Bootstrap токена** — токен генерируется на стороне wizard'а (64-hex,
`crypto/rand`) и прокидывается на VPS через ту же ssh-write, которой уже
пишется `wg-monitor.env`:

- При первом `actionInstallBackend` — generate + push в env-файл + cache в
  `secrets.env`.
- При `actionUpdateComponents` — если в кэше токена нет (апгрейд с v0.11),
  generate + push + cache (без вопросов; reset токена — отдельная ручка,
  здесь не делаем).
- При sync с другого ПК на тот же VPS — wizard вытащит токен из VPS env
  тоже через ssh при `actionUpdateComponents` (или, как fallback, юзер
  скопирует токен руками в `secrets.env`). Авто-импорт токена с VPS по ssh
  при первом запуске — лёгкий бонус-хелпер, не блокер для PR-1.

Rate-limit не вешаем — wizard-эндпоинты редкие (раз в сессию пользователя),
не write-path на DB-mutex.

### Wizard changes

**`cmd/deploy/state.go`** — `AgentState` остаётся как есть (host/port/user/
arch уже там). Локальный `state.Agents` теперь кэш — не правда.

**Новый файл `cmd/deploy/vps_sync.go`** (один файл, ~150 строк):
- `type VPSClient struct { domain, token string; httpClient *http.Client }`
- `func (c *VPSClient) ListAgents(ctx) ([]RemoteAgent, error)`
- `func (c *VPSClient) PushAgent(ctx, RemoteAgent) error`
- `func MergeAgents(local []AgentState, remote []RemoteAgent) (merged []AgentState, added []string, divergent []string)` — pure-functional, чистый merge.

**Merge logic:**
1. Для каждого remote — найти local по nickname (case-sensitive).
2. Если local нет → добавить новый `AgentState` с remote-полями (SSH-поля
   могут быть NULL → wizard покажет ⚠ для деплоя). `added`.
3. Если local есть → обновить thread_id/last_deployed_version из remote
   (бэк — правда), host/port/user/arch не трогаем если remote NULL, иначе
   replace. `divergent` если значения SSH-полей разошлись (на будущее —
   пока just lognote).
4. Local agents, которых нет на remote → оставляем как есть, но печатаем
   warning «есть локально, нет на VPS — возможно удалён через CLI».

**Меню `cmd/deploy/menu.go`** — новый пункт `[10] Синхронизация с VPS`,
runs sync action. Auto-sync на старте wizard (если `state.Backend.Host !=
""` и токен есть в кэше) — best-effort, на network-fail → молча
продолжаем с локальным state и печатаем `⚠ VPS unreachable, using cached state`.

**Push-points** — после успешного завершения этих экшенов добавить
`vpsClient.PushAgent(ctx, a)`:
- `actionAddRouter` → push после первого install (creates SSH info)
- `actionInstallAgent` → push (обновляет `last_deployed_version`)
- `actionUpdateComponents` → push для каждого обновлённого агента

Push best-effort: лог-предупреждение на ошибку, не fail весь action.

### Error / failure modes

| Сценарий | Поведение |
|---|---|
| VPS unreachable на startup | Warn в баннере, используем local cache, кнопка `[10]` тоже отвалится с понятной ошибкой |
| `WIZARD_TOKEN` не настроен на бэке | Эндпоинты не зарегистрированы → wizard видит 404, отключает sync с предложением «выполни `actionInstallBackend` чтобы сгенерить токен» |
| Wizard token mismatch | 401 от бэка, wizard печатает «токен невалиден, проверь `secrets.env`» |
| `PUT` на несуществующий nickname | 404, wizard советует «сначала `actionAddRouter`» |
| Merge: remote вернул роутер которого нет локально | Добавляется с warning «новый из VPS — нужно ввести SSH вручную чтобы редеплоить» |
| Один агент задеплоен с двух ПК одновременно | Last-write-wins, без warning (cf. Non-Goals) |
| Откат с v0.12.x на v0.11.x | DB columns остаются (SQLite не дропает), v0.11 их игнорирует — безопасно |

## Testing strategy

Минимум, по запросу «без лишних не нужных тестов»:

1. **`MergeAgents` unit tests** — 3-4 кейса: empty local + remote, local
   superset, remote-only, divergent SSH. Pure-функция, тривиально.
2. **Handler happy + auth** — 1 тест на GET (200 с JSON), 1 на 401 (нет
   токена), 1 на PUT 204 + 404. С httptest.NewServer.
3. **Push-after-deploy интеграционный** — НЕ пишем. Visual smoke на двух ПК
   решает быстрее.

Никаких end-to-end / table-driven / property-based.

## Migration & rollout

1. PR-1: DB migration (4 ALTER COLUMN) + GET handler + auth middleware
   + minimal merge logic в wizard + `[10] Sync` menu item.
   Subagent-friendly: 3-4 относительно независимых файла.
2. PR-2: PUT handler + push-after-deploy points в wizard.
3. PR-3 (опц): auto-pull на старте + UX-полировка.

Релиз — v0.12.0-rc1 после PR-1 + PR-2 в одном цикле. PR-3 можно в rc2.

## Open Questions

- Куда положить `WIZARD_TOKEN` в backend env? Существующий
  `/etc/wg-monitor/wg-monitor.env` или новый файл? **Решение:** существующий,
  systemd service уже его читает.
- Логировать `divergent` SSH-расхождения? **Да**, лог-line на каждый случай,
  но без интерактивного prompt'а — KISS.

## Спецификация: ссылки на код

- Backend mux: [internal/backend/handler.go:199-246](internal/backend/handler.go#L199-L246)
- Existing auth middleware (model): [internal/backend/auth.go](internal/backend/auth.go)
- Wizard state: [cmd/deploy/state.go](cmd/deploy/state.go)
- Wizard menu: [cmd/deploy/menu.go](cmd/deploy/menu.go)
- Wizard secrets: [cmd/deploy/secrets.go](cmd/deploy/secrets.go)
- Existing version audit cache: [internal/backend/callbacks/maint_audit_cache.go](internal/backend/callbacks/maint_audit_cache.go)
