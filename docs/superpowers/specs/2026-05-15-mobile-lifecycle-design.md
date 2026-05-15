# Mobile Router Lifecycle — design

**Дата:** 2026-05-15
**Статус:** spec, ждёт plan
**Контекст:** v0.13.0-rc cycle (after pingcheck panel + diag drill-down).

## Проблема

Текущая семантика `kind=mobile` (mobile router в машине, 4G WAN) — только
один override: heartbeat threshold (`StaleAfterMobile`, по template 300s
= 5 min). По истечении этого окна без heartbeat'а агента backend шлёт
HARD-OFFLINE алерт + renotify каждые 6 часов. Для мобильного клиента это
означает спам: каждое выключение машины → ROUTER-OFFLINE в TG-топике, и
ещё повторно через каждые 6 часов «всё ещё offline».

Желаемая семантика: lifecycle модель "spящий ↔ бодрствует":

- **Пробуждение** (машина включилась, 4G поднялся): один adaptive wake-card
  в TG-топик — «🚗 в сети, всё ок» если check'и зелёные, или подробный
  card с проблемами и action-кнопками если есть failures.
- **Засыпание** (нет heartbeat'ов): одно одноразовое info-сообщение
  «🌙 вышел из сети (last seen 14:32)». Без HARD-статуса, без renotify.

## Решение

### Архитектурные принципы

1. **Wake detection живёт в агенте, не в backend.** Агент уже хранит
   `last_report_at` локально (`/opt/var/wg-monitor/reporter-state.json`,
   persisted через restart). На gap > `ResumedThreshold` (5 min)
   `Report.Resumed=true` уже выставляется
   ([reporter.go:113](../../../internal/agent/reporter.go#L113)).
   Backend доверяет флагу.
2. **Никаких новых полей в wire-протоколе.** `Resumed=true` уже несёт
   нужный сигнал. Backend различает kind=mobile + Resumed=true как
   wake-event.
3. **Mobile-lifecycle включается через config flag** `mobile_lifecycle:
   true` (default true). Если оператор хочет старое поведение —
   `mobile_lifecycle: false` возвращает legacy HARD-OFFLINE через
   `stale_after_mobile_sec`.
4. **Static-роутеры не затронуты вообще.** Все изменения скоупом mobile.
5. **In-memory `sleepNotified[uid]` map.** Backend restart может прислать
   одно повторное «вышел из сети» — acceptable. Persistence в БД для
   one-shot info-сообщения — overkill.

### Изменения по компонентам

**Agent (`internal/agent/reporter.go`)** — изменений нет. Существующий
`Resumed`-механизм уже корректен для этой цели.

**Wire (`pkg/wire/types.go`)** — изменений нет.

**Backend handler (`internal/backend/handler.go::handleReport`)** —
добавить ветку после `MarkResumed`:

```go
if rep.Resumed && d.Resumer != nil {
    d.Resumer.MarkResumed(uid)
    if d.WakeNotifier != nil && userIsMobile {
        go d.WakeNotifier.SendWake(context.Background(), uid, nick, rep.Checks)
    }
}
```

`userIsMobile` — определяется через одну SELECT из `users` по uid (тот же
запрос можно использовать и для MarkResumed, чтобы не делать second
roundtrip).

**Backend watcher (`internal/backend/heartbeat/watcher.go`)** —
разветвление в `scan()` по `u.IsMobile() && cfg.MobileLifecycle`:

- Для mobile-lifecycle: threshold = `MobileSleepAfter` (5 min default).
  При превышении → `sleepNotified[uid]` guard → `SleepNotifier.SendSleeping(...)`
  один раз. На любом успешном Report (resumed grace или fresh heartbeat)
  → `delete(sleepNotified, uid)`. Никаких HARD, FSM-write, renotify.
- Для static и mobile с `MobileLifecycle=false`: существующий path
  (`SendOffline` → HARD → renotify через `RenotifyEvery`).

`Config` обновляется:
```go
type Config struct {
    StaleAfter       time.Duration  // deprecated
    StaleAfterStatic time.Duration
    StaleAfterMobile time.Duration  // applied только если MobileLifecycle=false
    MobileSleepAfter time.Duration  // NEW: default 5 min
    MobileLifecycle  bool           // NEW: default true
    ResumeGrace, ScanEvery, RenotifyEvery time.Duration
}
```

`Watcher.SetSleepNotifier(s SleepNotifier)` — setter (не ломаем существующий
`NewWatcher` constructor signature).

**Sleep notifier (`internal/backend/alerts/sleep_info.go` — новый)**:

```go
type SleepNotifier interface {
    SendSleeping(ctx context.Context, userID int64, nickname string, lastSeen time.Time) error
}
```

Имплементация резолвит user → thread_id → шлёт через `tg.Client` короткое
info-сообщение: `Card{Badge:"🌙", Summary:"<nick> вышел из сети
(последний heartbeat 14:32)"}`. Без кнопок, без HARD-индикатора.

**Wake notifier (`internal/backend/alerts/wake_report.go` — новый)**:

```go
type WakeNotifier interface {
    SendWake(ctx context.Context, userID int64, nickname string, checks []wire.Check) error
}
```

Adaptive renderer:
- `len(failed) == 0` (исключая `agent_heartbeat`) →
  `Card{Badge:"🚗", Summary:"<nick> в сети — всё ок"}`. Без кнопок.
- `len(failed) > 0` → `Card{Badge:"🚗⚠", Summary:"<nick> в сети, есть
  проблемы", Details: bullet-list через `humaniseCheckName`}` +
  inline-keyboard `[📊 Diag] [🔁 Restart tunnel] [✖ Закрыть]`.

Использует существующий `alerts/card.go` builder.

**Config (`internal/backend/config.go`)** — добавить:
```go
type HeartbeatConfig struct {
    // ...existing fields...
    MobileLifecycle     *bool `yaml:"mobile_lifecycle"`        // default true
    MobileSleepAfterSec int   `yaml:"mobile_sleep_after_sec"`  // default 300
}
```

Defaults в `LoadConfig`. `*bool` чтобы explicit `false` различался от
пропуска поля.

**Template (`cmd/deploy/templates/backend.yaml.tmpl`)** — добавить новые
ключи с дефолтными значениями + комментарии.

**Wiring (`cmd/backend/main.go`)** — собрать `WakeNotifier` +
`SleepNotifier`, передать первый в `mux.Deps`, второй через
`watcher.SetSleepNotifier`.

### Edge cases

| Сценарий | Поведение |
|---|---|
| Mobile только что добавлен, агент ещё не стартанул | scan через 5 min → SendSleeping (правильно — оператор видит что не на связи). |
| Backend restart с активным «sleeping» mobile | Может прислать повторное «вышел из сети» один раз. Acceptable. |
| Race: agent шлёт Resumed=true одновременно со scan() | Существующий `resumed[uid]` map + `ResumeGrace` уже защищает обе ветки. |
| Первый старт после install-agent (`prev.IsZero()`) | `Resumed=false` → wake-card НЕ показывается. One-time edge case. |
| `mobile_lifecycle: false` в yaml | Legacy HARD-OFFLINE через `StaleAfterMobile`. Wake-card не показывается. |
| Старый mobile user в HARD-OFFLINE на момент апдейта | Renotify прекращается. Может прийти одно «🌙» сообщение. Старый HARD остаётся в TG как есть. |

### Migration

- **SQL миграции нет.** `users.kind` уже существует.
- **YAML миграции нет.** Defaults в `LoadConfig` дают `MobileLifecycle=true`
  + `MobileSleepAfter=5min` без правки `/etc/wg-monitor/backend.yaml`.
- **Wizard update-backend** не перезаписывает yaml — operator после restart
  backend сразу в новом режиме.

## Тесты

**Адаптировать:**
- `TestWatcherMobileUsesLongerGrace`, `TestWatcherMobileFiresAfterMobileThreshold`
  → явно передавать `MobileLifecycle: false` (тестируют legacy режим).

**Новые watcher unit-тесты:**
- `TestWatcherMobileLifecycle_SleepInfoAfter5Min`
- `TestWatcherMobileLifecycle_NoRenotifyOnRepeatScan`
- `TestWatcherMobileLifecycle_ResumeClearsSleepFlag`
- `TestWatcherMobileLifecycle_FreshUserNoSleepInfo`
- `TestWatcherStaticUnaffectedByMobileLifecycle`

**Handler unit-тесты:**
- `TestHandleReport_MobileResumed_TriggersWakeCard` (all-ok + failures)
- `TestHandleReport_StaticResumed_NoWakeCard`
- `TestHandleReport_MobileNotResumed_NoWakeCard`

**Renderer unit-тесты:**
- `TestRenderWakeReport_AllOk_SingleLineCard`
- `TestRenderWakeReport_WithFailures_BulletDetails`
- `TestRenderWakeReport_SkipsAgentHeartbeat`

**Integration (`cmd/backend/backend_mobile_lifecycle_integration_test.go`):**
- `TestIntegration_MobileLifecycle_RoundTrip` — wake → idle → sleep info →
  no renotify → wake снова.

## Out-of-scope (deliberate YAGNI)

- Trip history (отдельная таблица `trips`, panel «История поездок»). Можно
  надстроить позже на той же wake/sleep boundary.
- Hardware ignition detection (GPIO/USB-modem). Не нужно — пользователю
  важно «в сети / нет», причина second-order.
- Per-user override mobile_lifecycle (некоторые mobile хотят HARD, другие
  нет). Сейчас global flag. Если возникнет need — `users.lifecycle_mode`
  столбец позже.
- Изменение wizard add-router prompt (опциональный текст-улучшение, не
  блокирует функциональность).

## Файлы, которые будут затронуты

**NEW:**
- `internal/backend/alerts/wake_report.go`
- `internal/backend/alerts/wake_report_test.go`
- `internal/backend/alerts/sleep_info.go`
- `internal/backend/alerts/sleep_info_test.go`
- `cmd/backend/backend_mobile_lifecycle_integration_test.go`

**MODIFY:**
- `internal/backend/heartbeat/watcher.go` — branch для mobile-lifecycle
- `internal/backend/heartbeat/watcher_test.go` — новые тесты, два legacy
  теста с явным `MobileLifecycle: false`
- `internal/backend/handler.go` — wake-card trigger
- `internal/backend/handler_test.go` — новые тесты
- `internal/backend/config.go` — два новых поля + defaults
- `cmd/backend/main.go` — wiring WakeNotifier / SleepNotifier
- `cmd/deploy/templates/backend.yaml.tmpl` — новые yaml ключи
