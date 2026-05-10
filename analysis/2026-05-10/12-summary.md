# Сводный отчёт аудита wg-monitor v0.11.0-rc18

Дата: 2026-05-10. Аудит прошёлся 11 параллельными агентами по 11 категориям. Всего найдено ~177 пунктов. Сводка по severity и приоритету ниже; детали в одноимённых файлах рядом.

## Цифры по категориям

| Файл | Категория | Critical | High | Medium | Low/Info |
|---|---|:-:|:-:|:-:|:-:|
| [01-security.md](01-security.md) | Security | 0 | 0 | 4 | 6 |
| [02-bugs.md](02-bugs.md) | Bugs | 5 | 0 | ~10 | ~15 |
| [03-dead-code.md](03-dead-code.md) | Dead code | 0 | 4 | 5 | 5 |
| [04-dependencies.md](04-dependencies.md) | Dependencies | 1 | 1 | 0 | 5 |
| [05-logic-issues.md](05-logic-issues.md) | Logic/Architecture | 0 | 2 | 9 | +9 deferred refactors |
| [06-performance.md](06-performance.md) | Performance | 0 | 1 | 14 | 3 |
| [07-observability.md](07-observability.md) | Observability | 6 | 6 | 8 | 4 |
| [08-testing-quality.md](08-testing-quality.md) | Testing | 0 | 4 | 8 | 4 |
| [09-deployment.md](09-deployment.md) | Deployment | 0 | 5 | 12 | 18 |
| [10-database.md](10-database.md) | Database | 0 | 5 | 6 | 3 |
| [11-api-contract.md](11-api-contract.md) | API contract | 0 | 5 | 5 | 4 |

## Что критично — закрывать первой партией

### Production-breaking (≤ 1 день)

| ID | Файл | Что | Чинить как |
|---|---|---|---|
| **BUG-01 / PERF-01** | `internal/agent/client.go:23-30` | HTTP-клиент агента имеет `Timeout: 10s`, но `PollCommand` шлёт `?wait=30`. Каждый long-poll рвётся → кнопки restart_tunnel / diag_now / firmware_install / route_status / etc. **никогда** не доходят до агента. Хроническое состояние прода. | Отдельный `*http.Client` для long-poll (как в `tg.Client.LongPollHTTP`) с timeout 60s+ |
| **BUG-18** | `internal/backend/db/state.go:169` | `silenced_until` сохраняется как RFC3339 (`'2026-05-10T14:30:00Z'`), сравнивается с SQL `CURRENT_TIMESTAMP` (`'2026-05-10 14:30:00'`). Лексикографически 'T' > ' ' → silenced_until ВСЕГДА > now → mute навечно. | Передавать `time.Now().UTC()` параметром, не использовать SQL `CURRENT_TIMESTAMP` |
| **BUG-02** | `internal/backend/alerts/dispatcher.go:53-94` | Hard-state не сохраняется при TG-фейле. На следующем report'е FSM снова кроссит порог → alert дубль. | Сохранять FSM transition ДО TG-send |
| **BUG-04** | `internal/backend/callbacks/maint.go:49-61` + `router.go:952-962` | `delete(s.m, token)` ДО проверки `UserID` → любой member чата может DoS'нуть подтверждение чужого maintenance. | Не удалять при mismatch |
| **BUG-05** | `internal/agent/cmdloop/loop.go:84-90` | `math.Pow(2, attempt)` overflow → busy-loop при ≥10 минутах оффлайна. | Clamp `attempt > 30` перед Pow |
| **DEP-01** | `go.mod:3` | Go 1.26.2 содержит 2 reachable stdlib CVEs (HTTP/2 infinite loop, net.Dial NUL panic Win). | Bump → 1.26.3 одной строкой |
| **API-05** | `cmd/backend/main.go:124-128` | Нет `ReadTimeout`/`WriteTimeout` → slow-loris на `/v1/report`. | Set timeouts (с учётом long-poll 60s) |
| **API-04** | `pkg/wire/types.go:69-73` | `IsValidCommandResultStatus` rejects unknown с 400 → schema evolution теряет данные. | Log-and-accept |

### High-impact (≤ 1 неделя)

| ID | Что | Где |
|---|---|---|
| **OBS-01** | `/healthz` всегда 200, не проверяет ничего | `internal/backend/handler.go:80` — добавить `/readyz` который пингует DB + cb + rp |
| **OBS-02..06** | Backend deg silently. TG client/auth/checks без slog'а. Нет remote-debug канала для агента. | См. [07-observability.md](07-observability.md) |
| **DB-01..04** | Missing critical indexes: `incident_state.user_id`, `users.telegram_thread_id`, etc. + `idx_events_user_ts` неоптимален для check_name-фильтров. | Migration с 5 индексами |
| **DB-05** | `/v1/report` делает `UpdateLastSeen` + N×(`events.Insert` + `state.Save`) без транзакции — crash mid-report = inconsistent state. | Обернуть в `BeginTx` |
| **DEAD-01** | `internal/backend/tg/control_panel.go` — целиком orphan, помечен "remove in v0.7.0", сейчас v0.11. Удалить. | |
| **DEAD-12** | Три locked worktrees в `.claude/worktrees/` от прошлых subagent-задач — все смерджены в main. Удваивают grep-результаты. | `git worktree remove` ×3 + `branch -D` |
| **DEPLOY-10** | systemd unit hardening — отсутствуют ~15 директив (PrivateDevices, ProtectKernel*, RestrictAddressFamilies, MemoryDenyWriteExecute, SystemCallFilter, MemoryMax, и т.д.). | Расширить unit |
| **DEPLOY-19** | Caddyfile без security headers (HSTS, X-Content-Type-Options, etc.) | Добавить `header { ... }` блок |
| **DEPLOY-29** | Нет backup `/var/lib/wg-monitor/state.db`. Потеря = full re-onboard всех агентов. | systemd timer + `sqlite3 .backup` + retention |
| **PERF-02..05** | `mscLoc()` дёргает `time.LoadLocation` каждый раз; heartbeat scan N+1; thundering-herd на upstream cache miss; cold-start блокирует первый report ~10с. | См. [06-performance.md](06-performance.md) |
| **TEST-01** | Race detector никогда не запускался. CI без `-race` — concurrency-баги невидимы. | `make test-race` + Linux CI job с CGO |
| **API-01** | `/v1/report` не идемпотентен — TCP/scheduler retry дублирует events. | `INSERT OR IGNORE` + UNIQUE index или `Idempotency-Key` |

### Medium-impact (планомерно)

- 11 LOGIC-items + 9 deferred refactors (god-file `callbacks/router.go` 1207 строк, etc.)
- 14 medium PERF-items
- 12 medium DEPLOY-items
- 8 medium TEST-items + missing `Now func()` seam в heartbeat/realert/dispatcher
- 5 medium API-items (errors as JSON, Content-Type validation, OpenAPI docs)
- 5 medium DEAD-items (deprecated AWGCheckConfig поля, OpkgRunner.DryRun, DB-колонки awg_iface/expected_exit_ip)

## Что хорошо

Подтверждено правильное:
- SQL injection — нет (все параметризовано)
- Auth chain (SHA-256 + ConstantTimeCompare) корректен
- WAL + foreign_keys + busy_timeout на DB настроены
- `pkg/wire` shared types предотвращают type drift agent↔backend
- `io.LimitReader` body caps + `maxCmdWait=60s`
- `bot_token_file` отделён от backend.yaml
- `loopback` bind + Caddy reverse proxy
- `fail_on_unmatched_files: true` в release.yml
- 44.6% coverage с правильным распределением (критичные пути 78-100%)
- `log/slog` где есть — JSON-handler на backend, Text на agent, контекстные поля присутствуют
- Все 22 пакета `go test` зелёные
- Никаких CVE в third-party deps
- ASCII-only в путях шаблонов

## Предложение по plan'у

См. [13-fix-plan.md](13-fix-plan.md). Разбит на:

1. **P0 production-breaking** — 8 items, 1 commit, ~3-4 часа реальной работы
2. **P1 high-impact** — 12 items, ~3 дня, 4-5 коммитов по тематике (observability, DB indexes, hardening, dead code, тесты)
3. **P2 medium** — ~50 items, недели, отдельный milestone
4. **Refactoring (deferred)** — 9 god-file/architectural items, отдельный PR/майлстоун
