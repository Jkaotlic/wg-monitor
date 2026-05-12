# Wizard Menu Reorg — Design

**Date:** 2026-05-12
**Status:** Draft
**Targets:** v0.12.0-rc3

## Summary

Меню `wg-monitor-deploy` накопило 9 пунктов с матрёшечными дублями
(status ⊂ smoke ⊂ doctor) и разделом install-agent vs add-router, которые
по сути одно действие в разных условиях. Сжимаем до 7 пунктов, [3] делаем
умным с авто-детектом через VPS sync.

## Текущее → Новое

| Старый пункт | Новый пункт | Действие |
|---|---|---|
| [1] Первичная установка бэкенда на VPS | [1] Установить бэкенд | без изменений |
| [2] Обновить компоненты | [2] Обновить компоненты | без изменений |
| [3] Первичная установка агента на роутер | (удаляется как top-level) | становится smart-fallback внутри нового [3] |
| [4] Добавить новый роутер | [3] Установить агента на роутер | merge — добавление + установка с авто-детектом |
| [5] Проверить статус (быстрая) | (удаляется) | покрыто Doctor'ом |
| [6] Smoke-тест | (удаляется) | покрыто Doctor'ом |
| [7] Doctor | [4] Проверить состояние | renamed, остаётся единственным health-чеком |
| [8] Управление known_hosts | [7] Забыть known_hosts alias | renumber, остаётся |
| [9] Открыть wizard.toml в редакторе | [6] Открыть wizard.toml | renumber |
| [10] Синхронизация с VPS | [5] Синхронизация с VPS | renumber |

## Новый [3] — smart install agent

Точка входа единая: «хочу поставить или переустановить агента на роутер».

Алгоритм:

1. Спросить nickname (валидация regex `cliNicknameRe` остаётся).
2. Параллельно собрать факты о роутере:
   - `state.FindAgent(nick)` → есть ли в локальном `wizard.toml`
   - `vpsUserExists(bs, nick)` → есть ли в `users` table на VPS (нужен SSH к VPS, как сейчас в [4])
3. Разветвление:

   | Local | VPS | Действие |
   |---|---|---|
   | нет | нет | **Новый роутер** — текущая [4]-логика: `wg-monitor-cli add-user` → токен → TG-топик → дефолты → install |
   | нет | есть | **Recover** — токен из `secrets.env` (если есть) или error «токен утерян, удали запись и пробуй заново» (текущая логика [4]), затем install |
   | есть | нет | **Drift** — print warn «локально есть, на VPS нет, регистрирую заново» → register + install |
   | есть | есть | **Reinstall** — спросить y/N «Установлено `v0.10.3`, переустановить тем же бинарём? (для обновления — выйди и используй [2])» |

4. Финальный шаг во всех ветках — `installAgentBinary(ag)` — extracted helper
   из текущего `actionInstallAgent`. Дублирующая логика (download + arch
   detect + upload + service restart) общая.

**Что меняется в коде:**
- Новый `actionAgentSmartInstall(state, secrets, dl)` объединяет [3]+[4].
- Существующий `actionInstallAgent` извлекаем в `installAgentBinary` (тот же файл, переименование) — это helper-функция без диалога с пользователем; параметризуется указателем на `AgentState`.
- Существующий `actionAddRouter` склеиваем с install в новый smart action.
- `actionStatus` ([actions.go:934](cmd/deploy/actions.go#L934)) и `actionSmokeTest` ([smoke.go:22](cmd/deploy/smoke.go#L22)) — **удаляем целиком** вместе с файлом `smoke.go`. Их helper'ы (`smokeCheckHealthz` etc.) тоже dead — удаляем.

## Меню — финальный вид

```
  [1] Установить бэкенд                  (первичная установка на VPS)
  [2] Обновить компоненты                (проверка релиза + выбор что обновить)
  [3] Установить агента на роутер        (новый или ре-установка существующего)
  [4] Проверить состояние                (Doctor: local + VPS + каждый агент)
  [5] Синхронизация с VPS                (подтянуть список роутеров с бэкенда)
  [6] Открыть wizard.toml в редакторе
  [7] Забыть known_hosts alias           (если физически заменил роутер)
  [Q] Выход
```

## CLI mode

`wg-monitor-deploy` принимает CLI-команды (`install-backend`, `install-agent`,
`update-backend`, `update-agent`) помимо интерактивного меню. Маппинг:

| Старая CLI команда | Что делать |
|---|---|
| `install-backend` | без изменений |
| `update-backend` / `update-agent` | без изменений |
| `install-agent` | **deprecate → alias на новый smart-install** (внутри проверяет registration state и делает правильное действие) |
| `add-router` | **deprecate → alias на smart-install** |

Старые имена остаются для бэк-совместимости (если у кого-то скрипты), но
печатают `PrintInfo("deprecated alias: use wg-monitor-deploy install-agent")`.

## Non-Goals

- **Не добавляем submenu** под advanced — KISS, 7 top-level пунктов лучше чем 5+1submenu.
- **Не объединяем доктора с known_hosts cleanup** — оставляем как отдельную утилиту.
- **Не выбрасываем `actionAddRouter` логику регистрации** — она вся переезжает в smart-install ветку «новый роутер».
- **Не меняем wire-протокол или DB.** Только wizard UX + dead code removal.

## Failure modes

| Сценарий | Поведение |
|---|---|
| Пользователь набрал nick, который не подходит под regex | Текущий PrintFail + return — не меняем |
| VPS unreachable во время `vpsUserExists` | Print warn → fallback в локальное состояние (state.FindAgent only) → если есть локально, идём в reinstall; если нет — error «не могу проверить, VPS unreachable» |
| Reinstall — пользователь ответил N | Return silently (как сейчас при N в [3]) |
| Бинарь не загрузился / SSH fail | Текущие пути обработки в `actionInstallAgent`/`installAgentBinary` — не меняем |

## Testing

Минимум — это UX-рефакторинг без новой бизнес-логики:

1. **Smoke:** build clean + ручной запуск меню в trace-режиме на dev-машине, прохожусь по новым [3], [4], [5].
2. **Unit:** новый `actionAgentSmartInstall` сложно покрыть end-to-end (нужен реальный SSH), поэтому extracted `installAgentBinary` остаётся без отдельного теста — он не меняет поведение, просто wrap.

Никаких новых тестов не добавляем.

## Rollout

Один PR / один rc — нет сюрпризов для прода, бэкенд не меняем. Релиз
`v0.12.0-rc3`.

## Open questions

— Нет.
