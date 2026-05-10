# Observability аудит — wg-monitor

Дата: 2026-05-10
Скоуп: `cmd/{backend,agent}`, `internal/{backend,agent}` (рабочее дерево, без worktrees `.claude/`).
Версия: v0.11.0-rc9 (memory).

## TL;DR

`log/slog` (JSON-handler в backend, Text-handler в agent) — выбран и используется правильно там, где есть. Но **observability покрытие крайне неравномерное**: critical paths типа TG-клиента (HTTP к Bot API), agent-side checks, agent-side actions, awgmgr-клиента, in-memory cmd Queue, FSM transitions — **ЛОГИРУЮТ 0 строк**. `/healthz` — пустышка. **Нет ни одной метрики**, нет request_id для трассировки. Auth-failures на `/v1/report` уходят в `http.Error` без аудит-лога. Heartbeat-fail спама нет (де-дуп через `notified` map работает), но повторные dispatch-fail/realert/checkpoint-fail спамятся каждый тик. Агент на Keenetic-роутере — практически не диагностируем удалённо: только логи в stderr, нет ни stack-dump endpoint, ни rolling buffer, который можно было бы стянуть через TG-кнопку.

Если backend упадёт в feature-failure (например, callbacks-router зависнет на `getUpdates`), оператор узнает об этом ТОЛЬКО когда: (а) кто-то обнаружит, что бот не отвечает на `📊 Что происходит?`, или (б) /healthz перестанет отвечать (но он тривиальный, не проверяет внутренних компонентов). Backend сам в TG не пишет «у меня сломалась подписка» / «realert poller exited».

---

## Findings

### Критичные (CRIT)

#### OBS-01 — `/healthz` не проверяет внутренние компоненты [CRIT]
Файл: `internal/backend/handler.go:80-82`
```go
mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
    _, _ = io.WriteString(w, "ok\n")
})
```
Возвращает `200 ok` всегда — даже если `db` отвалилось, `getUpdates` цикл застрял, `realert poller` exit'нул, или `cb.Run` упал. Caddy и wizard `doctor` будут считать систему здоровой при сломанном backend.

**Рекомендация:** `/healthz` оставить тривиальным (liveness), добавить `/readyz`, который:
- `db.Ping()` (через `d.SQL().Ping()`)
- проверяет, что `lastSuccessfulGetUpdates < 2*pollTimeout`
- проверяет, что `realertPoller.LastTickAt < 2*TickEvery`
- возвращает `503` с JSON-описанием сломанного компонента
Backend держит указатели на `cb`, `rp`, `watcher` — добавить им `LastHealthy() time.Time` и опросить из хендлера.

#### OBS-02 — Тихий exit фоновых горутин фатален и невидим извне [CRIT]
Файлы:
- `cmd/backend/main.go:144-148` (`cb.Run`)
- `cmd/backend/main.go:155-159` (`rp.Run`)
- `cmd/backend/main.go:131` (`watcher.Run` — exit вообще без error-канала)

Если callbacks router выйдет с ошибкой (TG token revoked, network sustained-fail), backend продолжит слушать `:18080` и отвечать `/healthz=ok`, но НИ ОДНА команда из TG не дойдёт до агентов. Только лог `"callbacks router exited"` уйдёт в stdout — в `journalctl -f` его никто не читает.

**Рекомендация:** при exit любой из 3 ключевых фоновых горутин (cb/rp/watcher):
1. Отправить алерт в группу TG: `"⚠️ wg-monitor backend: callbacks router exited: <err>"` (через прямой `tgClient.SendMessage` в `summary` или `systemic` topic).
2. Снять `readyz` (см. OBS-01).
3. Опционально: `os.Exit(3)` — systemd рестартует, лучше падение чем зомби.

#### OBS-03 — TG-клиент не логирует ни одного запроса/ошибки [CRIT]
Файл: `internal/backend/tg/client.go` (весь файл, 284 строки, ноль `slog.*`)

Все `c.call(ctx, "sendMessage", ...)` — silent. Если TG возвращает `400 Bad Request: chat_id not found` или `429 Too Many Requests`, ошибка пробулькивает до вызывающего, который часто `slog.Warn` без HTTP-кода и без `description`. При прод-инциденте ("сообщения не приходят") — нет таймлайна вызовов, нет distinguishing 400/403/429.

**Рекомендация:** в `callWith` добавить:
```go
defer func() {
    slog.Debug("tg api", "method", method, "status", resp.StatusCode, "ok", ar.OK, "tg_code", ar.ErrorCode, "duration_ms", ...)
}()
```
И на error-path — `slog.Warn("tg api error", "method", method, "tg_code", ar.ErrorCode, "desc", ar.Description)`. На 429 (`retry_after`) — ОБЯЗАТЕЛЬНО `Warn`, чтобы оператор увидел rate-limit.

#### OBS-04 — Auth-failures на `/v1/report` без аудит-лога [CRIT]
Файл: `internal/backend/auth.go:28-44`

Невалидный token или missing header → `http.Error(w, "unauthorized", 401)` без `slog`. Сценарий: ротировали токен агента, забыли обновить — все агенты возвращают 401, никто не знает. Также security: brute-force попытки на `/v1/report` будут невидимы.

**Рекомендация:** при 401 `slog.Warn("auth rejected", "remote", r.RemoteAddr, "ua", r.UserAgent(), "reason", ...)`. При 500 (DB lookup failed) — `slog.Error`. Опционально: rate-limit по IP с counter в `slog.Warn`.

#### OBS-05 — Agent-side checks никогда не логируют [CRIT]
Файлы: `internal/agent/checks/*.go` — все 14 файлов, **0 вызовов slog**.

`TunnelsCheck`, `DNS`, `HydraRouteCheck`, `AwgManagerCheck`, `ExternalReachCheck` — каждый раз генерирует `wire.Check{Status: "fail", Details: {error: ...}}` и забывает. Если агент 200 раз подряд получает `awgmgr connection refused`, в stderr ничего не пишется — только в backend events table. Удалённый дебаг ("почему все checks fail на этом router?") требует `ssh root@router && tail -f /var/log/messages` — бесполезно, потому что туда `slog.Stderr` не пишет (агент в systemd, journal ловит, но Keenetic Entware не systemd).

**Рекомендация:** в `Fail(...)` добавить параметр `logger *slog.Logger` или вызвать `slog.Default().Warn("check failed", "name", name, "err", errMsg, "duration_ms", ...)` в helper. Это даст хотя бы локальный rolling-log для grep `failed`.

#### OBS-06 — Agent на роутере не имеет remote-debug канала [CRIT]
Контекст: Keenetic Entware, `pidof wg-monitor`, `journalctl` отсутствует, log goes to `os.Stderr` который теряется в init.d.

**Рекомендация:** добавить в `wire.Command` action `dump_logs` (последние 200 строк log buffer) или `pprof_goroutine` — агент держит in-memory rolling buffer (`log/slog` + custom handler с ring-buffer), и по команде из TG возвращает это в `CommandResult.Output`. Без этого "почему агент молчит" дебагается только физическим SSH к роутеру.

---

### Высокие (HIGH)

#### OBS-07 — `alerts.Dispatcher.Handle` возвращает err без слоя логов [HIGH]
Файл: `internal/backend/alerts/dispatcher.go:43-122`

`ensureTopic`, `SendMessageWithKeyboard`, `State().Save` — все ошибки возвращаются caller'у. Caller (`handler.go:142-144`) пишет `Logger.Warn("dispatch", "check", ..., "err", err)` — это логируется. НО детали (какой именно из 3 шагов сломался: TG send, ensureTopic, State.Save?) теряются.

**Рекомендация:** обернуть каждый return в `fmt.Errorf("ensure topic for %s: %w", nickname, err)` (часть уже есть в `case state.Hard`, но не везде), и в `dispatcher.go` для `Recovery` / `SendOffline` — то же самое.

#### OBS-08 — Cmd Queue: enqueue/dequeue/result silent [HIGH]
Файл: `internal/backend/cmd/queue.go` (208 строк, **0 slog**)

Команда от админа в TG → enqueue → агент dequeue → execute → result. Если что-то теряется на пути, ничего не залогировано. Даже `RecordResult` валидация ошибки → `return errors.New(...)` без лога. `ConsumeOriginRef` — silent.

**Рекомендация:** добавить `Logger *slog.Logger` поле в `Queue`, логировать `Enqueue`/`Dequeue`/`RecordResult` на Debug, error-paths на Warn. Дополнительно exposing метрики: `pending_count{user_id}`, `result_buffer_size`.

#### OBS-09 — FSM transitions не логируются [HIGH]
Файл: `internal/backend/state/fsm.go`

`Apply()` — pure function, возвращает Transition (Noop/Soft/SoftFlap/Hard/Recovery). Caller (`handler.go:141`) НЕ логирует transition, кроме как при dispatch-error. Восстановить таймлайн "когда check `tunnel_AMS` ушёл из ok→soft→hard" из логов нельзя — только по events table SQL.

**Рекомендация:** в `handler.go:141` добавить `slog.Debug("fsm transition", "user", nick, "check", c.Name, "kind", tr.Kind, "prev", prev.Status, "next", tr.Next.Status)`. На `Hard` и `Recovery` — `Info` уровень. Это дешёво и резко улучшает post-mortem.

#### OBS-10 — Heartbeat-watcher логи без `user_id` [HIGH]
Файл: `internal/backend/heartbeat/watcher.go:118,158`

```go
slog.Warn("heartbeat scan: list users", "err", err)         // нет user_id (понятно, общая ошибка)
slog.Warn("heartbeat: send offline failed", "nickname", u.Nickname, "err", err)  // user_id мог бы помочь
```
Также: `if latest.IsZero() { continue }` — никогда не отчитавшийся пользователь молча пропускается. И `if w.d.Events().LatestPerUser(u.ID); err != nil { continue }` — ошибка БД проглочена.

**Рекомендация:** добавить `user_id` в structured logs последовательно. На `LatestPerUser err` — `slog.Warn`. Для never-reported user (`latest.IsZero()`) — `slog.Debug` раз в час, не на каждом скане.

#### OBS-11 — Realert poller спамит при упорной TG-ошибке [HIGH]
Файл: `internal/backend/realert/poller.go:144-148`

Если TG `chat_id` стал невалидным, на каждом тике (5 мин) для каждого `stale` инцидента будет `slog.Error("realert: tg send failed", ...)`. При 5 hard-incidents и проблеме TG за неделю — 2016 одинаковых ERROR, заглушающих реальные проблемы.

**Рекомендация:** error-sampling per-(user,check) — лог только на 1й, 5й, 50й fail (token-bucket или counter). Альтернатива: при `tg send failed > 3 раз подряд по всем users` — отправить ОДИН summary-лог + признать проблему через OBS-02 алерт.

#### OBS-12 — cmdloop backoff бесконечный без alarm [HIGH]
Файл: `internal/agent/cmdloop/loop.go:65`

При sustained-fail backend (агент не может poll), backoff капится на 60s, спам `slog.Warn("cmdloop poll failed", ...)` каждую минуту, **навсегда**. Никакой эскалации, никакого "после 30 fails переходим в circuit-broken state". Backend не узнает, что агент в это время не получает команды (он же не отчитывается, watcher через staleAfter сработает только если ещё и `/v1/report` сломан, что обычно одна и та же проблема — но не всегда).

**Рекомендация:** counter "consecutive_poll_failures", при ≥10 — лог `slog.Error("cmdloop chronically failing")` и в следующем `/v1/report` добавить `agent_diag.poll_failures: N` чтобы backend в healthcheck это увидел. Также: после 30+ fails — снизить частоту до 5min чтобы не жечь батарею mobile-router'а.

---

### Средние (MED)

#### OBS-13 — Нет метрик / Prometheus [MED]
Контекст: фактическое состояние fleet (10 агентов, 1 backend) делает Prometheus optional, но...

Сейчас невозможно ответить на вопросы:
- "Сколько HARD-инцидентов сейчас открыто?" (только SQL по `incident_state`)
- "Сколько отчётов в минуту приходит?" (нет счётчика)
- "Какова p95 latency `/v1/report`?"
- "Сколько TG-сообщений отправили за день?"

**Рекомендация:** добавить `expvar` (zero deps) на `/debug/vars` или Prometheus client с минимальным набором:
- `wgm_reports_total{nickname, status}`
- `wgm_alerts_sent_total{kind, check}`
- `wgm_tg_api_errors_total{method, code}`
- `wgm_cmd_queue_pending{user_id}`
- `wgm_heartbeat_offline_users` (gauge)

#### OBS-14 — `report` лог содержит весь `checkSummary` без сэмплинга [MED]
Файл: `internal/backend/handler.go:146-150`

Каждый report (раз в 60-90с с агента) пишет Info с полным списком `["awg_manager=ok","tunnel_AMS=ok",...]`. На 10 агентах это ~10/мин = ~14k Info/день только от reports. JSON handler, в /var/log/wg-monitor-backend.log без ротации (если не настроен logrotate) — загадит диск.

**Рекомендация:** Info при первом-после-старта или при изменении статуса; Debug при stable. Альтернатива: статус rolled up в одно поле `"checks_summary": "9ok/1fail"` без массива.

#### OBS-15 — Upstream GitHub API ошибки тихо кэшируются [MED]
Файл: `internal/backend/upstream/versions.go:84-87`

Если GitHub вернёт 403 (rate-limit) или 404 (repo переименован), error попадёт в `Entry.Err` и закэширован на TTL — silent. Smart-reply просто не покажет «доступна обновка», оператор будет думать что всё свежо.

**Рекомендация:** при `c.fetch err != nil` логировать `slog.Warn("upstream fetch failed", "source", name, "repo", repo, "err", err)`. На 403 — Warn с подсказкой про rate-limit.

#### OBS-16 — awgmgr-клиент агента не логирует [MED]
Файл: `internal/agent/awgmgr/client.go` (275 строк, 0 slog)

Каждый `get()`/`post()` к `127.0.0.1:2222` — silent. Если awg-manager локально умер, агент по сути не знает почему `awg_manager` check fails — error message от Go HTTP («connection refused») попадёт в Details, но локально нет лога с timing/path/status-code.

**Рекомендация:** в `get`/`post` добавить `slog.Debug("awgmgr", "method", "GET", "path", path, "status", resp.StatusCode, "duration_ms", ...)`. Error-path — `slog.Warn`.

#### OBS-17 — Retention vacuum/checkpoint лог без дюрации на ошибке [MED]
Файл: `internal/backend/retention/policy.go:71`

`retention: operation failed` — не указано сколько прошло до fail. VACUUM может зависнуть на 30s и упасть; знать duration важно для capacity-planning.

**Рекомендация:** добавить `"duration_ms"` в Warn-лог error path.

#### OBS-18 — Нет request_id / trace_id [MED]
Контекст: один `report` вызывает 5+ DB операций + N `Dispatcher.Handle` + потенциально TG-send. Если что-то падает в середине, нечем сшить логи.

**Рекомендация:** в `reportHandler` сгенерировать `req_id := uuid7()` или `time.Now().UnixNano()`, положить в `r.Context()` и в `slog.With("req_id", id)` для всех downstream-логов того запроса. Аналогично — для `cmd_id` (уже частично есть).

---

### Низкие (LOW)

#### OBS-19 — `slog.Default()` смешивается с инжектированным logger'ом [LOW]
Файлы: `realert/poller.go` использует `slog.<X>(...)` (default), а `retention/policy.go` использует `p.Logger.<X>(...)` (инжектированный). Тесты у retention могут собирать логи в buffer, у realert — нет. Несогласованность.

**Рекомендация:** один стиль. Либо все принимают `*slog.Logger` как поле, либо все используют `slog.Default()`. Рекомендую первое (тестируемость).

#### OBS-20 — Agent text-handler vs backend JSON-handler [LOW]
Файл: `cmd/agent/main.go:34`, `cmd/backend/main.go:43`

Агент пишет text format, backend — JSON. Consistent для backend под Loki/grafana, но при сборе агентских логов в централизованное место их парсить сложнее.

**Рекомендация:** опционально: env-var `LOG_FORMAT=json|text` для агента, default text (читаемо при ssh), prod=json.

#### OBS-21 — `tunnel_import` не логирует имя файла / размер [LOW]
Файл: `internal/agent/actions/tunnel_import.go` (предположительно — не читал)

Контекст: callbacks/router логирует `document-upload` с file/size/kind (router.go:840), но что произошло на агенте — silent.

**Рекомендация:** в `ImportTunnel(...)` добавить `slog.Info("tunnel import", "name", name, "replace", replace, "result", "ok|fail")`.

#### OBS-22 — `AwaitResult` timeout — silent [LOW]
Файл: `internal/backend/cmd/queue.go:180-207`

Если callback ждёт результат и timeout — caller получает `(nil, false)`, никакого лога. При прод-инциденте "почему toast не показал результат" — расследовать сложно.

**Рекомендация:** `slog.Debug("AwaitResult timeout", "user_id", userID, "id", id, "waited", timeout)` (Debug — потому что это normal flow при неактивном агенте).

#### OBS-23 — DB-open не логирует путь / migration count [LOW]
Файл: `internal/backend/db/db.go:20-43`

Тихая инициализация. На свежем deploy без существующего файла поведение неотличимо от случая с corrupted DB.

**Рекомендация:** в `Open` добавить `slog.Info("db opened", "path", path, "exists", existed, "migrations_run", n)`.

#### OBS-24 — `report.AgentVersion` логируется, но не tracked [LOW]
Файл: `internal/backend/handler.go:147`

`agent_version` в каждом report-логе — useful, но не агрегируется. Для фактической операционной метрики "сколько агентов на старой версии" нужно SQL по событиям + tail логов.

**Рекомендация:** добавить `users.last_agent_version` колонку (UPDATE на report), или metric `wgm_agent_version{nickname, version}`.

---

## Сводная таблица

| ID | Severity | Файл | Тема |
|---|---|---|---|
| OBS-01 | CRIT | `internal/backend/handler.go:80` | `/healthz` пустышка, нет `/readyz` |
| OBS-02 | CRIT | `cmd/backend/main.go:144,155` | Тихий exit фоновых горутин, бот молчит |
| OBS-03 | CRIT | `internal/backend/tg/client.go` | TG client zero logging |
| OBS-04 | CRIT | `internal/backend/auth.go:28` | Auth fails без аудит-лога |
| OBS-05 | CRIT | `internal/agent/checks/*` | Все checks не логируют |
| OBS-06 | CRIT | `cmd/agent/main.go` | Нет remote-debug канала для агента |
| OBS-07 | HIGH | `internal/backend/alerts/dispatcher.go` | Dispatcher errors без deep context |
| OBS-08 | HIGH | `internal/backend/cmd/queue.go` | Cmd queue silent |
| OBS-09 | HIGH | `internal/backend/state/fsm.go` + handler.go:141 | FSM transitions не логируются |
| OBS-10 | HIGH | `internal/backend/heartbeat/watcher.go:127` | Heartbeat-fail спам не дедуплицирован per-user |
| OBS-11 | HIGH | `internal/backend/realert/poller.go:147` | Realert error не сэмплится |
| OBS-12 | HIGH | `internal/agent/cmdloop/loop.go:65` | Cmdloop backoff без эскалации |
| OBS-13 | MED | (нет файла) | Нет метрик / Prometheus |
| OBS-14 | MED | `internal/backend/handler.go:146` | report-лог raw |
| OBS-15 | MED | `internal/backend/upstream/versions.go:85` | GitHub fetch errors тихо кэшируются |
| OBS-16 | MED | `internal/agent/awgmgr/client.go` | awgmgr client silent |
| OBS-17 | MED | `internal/backend/retention/policy.go:71` | Vacuum-fail без duration |
| OBS-18 | MED | (cross-cutting) | Нет request_id / trace_id |
| OBS-19 | LOW | `realert/poller.go` vs `retention/policy.go` | slog.Default vs injected mixed |
| OBS-20 | LOW | agent text vs backend json | Logger format несогласованность |
| OBS-21 | LOW | `internal/agent/actions/tunnel_import.go` | tunnel_import без agent-side log |
| OBS-22 | LOW | `internal/backend/cmd/queue.go:180` | AwaitResult timeout silent |
| OBS-23 | LOW | `internal/backend/db/db.go:20` | DB Open без лога |
| OBS-24 | LOW | `internal/backend/handler.go:147` | agent_version не агрегируется |

---

## Приоритетный план

### MVP (1-2 дня, дёшево, огромный ROI)
1. **OBS-01** `/readyz` с реальной проверкой db + cb + rp.
2. **OBS-02** Алерт в TG `summary` topic при exit фоновой горутины.
3. **OBS-03** Базовый log в `tg/client.go callWith` (Debug success / Warn 4xx-5xx).
4. **OBS-04** `slog.Warn("auth rejected", ...)` в auth.go.
5. **OBS-09** `slog.Info("fsm transition", ...)` для Hard/Recovery в handler.go.

### Стадия 2 (структурированный observability)
6. **OBS-08** Logger в Queue.
7. **OBS-11** + **OBS-10** Error-sampling в realert и watcher.
8. **OBS-18** request_id middleware.
9. **OBS-13** expvar или минимальный Prometheus.
10. **OBS-15** Лог GitHub fetch errors.

### Стадия 3 (agent debug)
11. **OBS-05** + **OBS-16** Agent-side logging.
12. **OBS-06** `dump_logs` action с in-memory ring buffer.
13. **OBS-12** Cmdloop эскалация.

### Чистка
14. OBS-07, 14, 17, 19-24 — мелкие улучшения по ходу.

---

## Replay-сценарий "оператор у приборки"

**Жалоба:** "бот не отвечает на `📊 Что происходит?` уже 30 минут, hard-инциденты не приходят".

С текущим логированием:
1. `journalctl -u wg-monitor-backend -n 200` — увидим reports приходят, ничего особенного.
2. Где callbacks router? Нет логов от `getUpdates` (только при ошибке). Висит ли `cb.Run`? Не понятно.
3. `/healthz` → `200 ok`. Бесполезно.
4. Посмотреть в БД, идут ли events — да, идут. Значит /v1/report ОК.
5. Перезапустить backend наугад. **Угадал — починилось**, но почему — неясно.

После реализации MVP (OBS-01,02,03):
1. `/readyz` → `503 {"callbacks":"stale, last_getupdates=12m ago"}` — сразу ясно.
2. В TG `summary` уже висит алерт от OBS-02.
3. `journalctl ... | grep tg api` показывает `429 Too Many Requests` от 30 минут назад.
4. Понятно: rate-limit, ждать или поднять token.
