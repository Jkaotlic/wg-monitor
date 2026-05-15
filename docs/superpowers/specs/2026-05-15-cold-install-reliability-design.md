# Cold-install reliability bundle — design

**Дата:** 2026-05-15
**Статус:** approved by user, fast-track (без TDD)
**Связано с:** [pull-based self-update](2026-05-15-pull-based-self-update-design.md) — этот документ закрывает оставшийся cold-install case

## Проблема

Pull-based `self_update` решает 99% случаев деплоя, но **cold install** (новый роутер; recovery после битого pull; смена `nickname`) всё ещё идёт через wizard SSH → `192.168.31.1`. Корневая проблема не уходит:

- У всех Keenetic LAN-IP `192.168.31.1`
- У оператора локалка тоже обычно `192.168.31.0/24` → Windows ставит две equivalent routes /24 → kernel может предпочесть локальный интерфейс → TCP-SYN не доходит до клиентского роутера через SSTP
- "Срабатывает через раз" = routes flap или metric race после SSTP-handshake

Дополнительно: даже если SSTP корректно работает, оператор может перепутать активный туннель → залить агента не туда. Текущая защита: known_hosts alias-based TOFU (`HostKeyCallbackFor`) ловит уже-известный fingerprint под другим nickname'ом, но НЕ ловит "новый роутер, тот же LAN-IP, операторская локалка".

## Решение

Два независимых патча в `cmd/deploy`:

### A. Auto-detect routing collision + temporary /32 host route

1. Перед `ConnectSSH` к роутеру: пройти `net.Interfaces()`, найти ВСЕ интерфейсы оператора, чьи subnets содержат `target_host`.
2. Если ≥2 интерфейса → collision detected. Категоризировать: point-to-point (FlagPointToPoint = SSTP/PPP) vs обычные (LAN/Wi-Fi).
3. Показать оператору список кандидатов; если есть единственный point-to-point — предложить его, иначе оператор выбирает.
4. На подтверждение `[y/N]` (`WG_YES_TO_ALL=1` → auto-yes) → выполнить `route ADD <target_ip> MASK 255.255.255.255 0.0.0.0 IF <ifindex> METRIC 1` через `exec.Command("route", ...)`.
5. Защёлкнуть `defer cleanup()` — после успеха ИЛИ ошибки деплоя удалить тот же маршрут (`route DELETE <target_ip>`).
6. Cleanup best-effort: если route delete упал — печатаем warning с ручной командой, не валим действие.

Платформо-зависимое: только Windows реализует add/del. На Linux/macOS detection работает (могут быть аналогичные конфликты), но операторы там редко натыкаются — print warning, оставить ручной `ip route add` оператору. Build tags разносят `routing_windows.go` и `routing_other.go`.

### B. MAC pinning

1. В `actionInstallAgent` после `stepDetectPrimaryMAC` (уже выполняется на step 2): записать MAC в `state.Agents[i].ExpectedMAC` (новое поле в TOML, lower-case без `:`).
2. В `actionUpdateAgent` (и cold-recover path в `actionInstallAgent` если `ExpectedMAC != ""`): после успешного `ConnectSSH`, ПЕРЕД любой записью — повторить `stepDetectPrimaryMAC`, сравнить.
3. Mismatch → bail с `"MAC роутера %s, ожидаю %s — это другое устройство. Проверь активный SSTP / поправь wizard.toml."`. Никаких писем на /opt/bin/* пока mismatch не разрешён.
4. Поле опциональное — если пустое (агенты, добавленные до этого патча), skip check + capture на следующем install.

MAC проверяется ПОСЛЕ TOFU host-key check, ПЕРЕД любой записью. Эта защёлка ловит сценарий "роутер ещё не имел агента, известного `nickname` в config.yaml нет (existingNick == ""), но физически это не тот хост" — текущий guard `stepReadExistingAgentNickname` это пропускает.

## Файлы

| Новый/изменён | Что |
|---|---|
| `cmd/deploy/routing.go` (new) | `detectRoutingCollision(host)` cross-platform на `net.Interfaces` |
| `cmd/deploy/routing_windows.go` (new, `//go:build windows`) | `addHostRoute(ip, ifIndex) (rollback, err)` через `route ADD` |
| `cmd/deploy/routing_other.go` (new, `//go:build !windows`) | stub: noop add, всегда возвращает err |
| `cmd/deploy/steps.go` | `verifyExpectedMAC(s, expected)` + `extractMAC(stepReadOrEmpty output)` |
| `cmd/deploy/state.go` | `AgentState.ExpectedMAC string \`toml:"expected_mac"\`` |
| `cmd/deploy/actions.go` | `actionInstallAgent`: capture MAC. `actionUpdateAgent` / `actionAddRouter`: setup routing + verify MAC. Все 3 — defer cleanup. |

## Что НЕ делаем

- Linux/macOS auto-route-fix — только Windows. Operator на Mac/Linux получит warning с ручной командой.
- Multi-SSTP heuristic auto-pick — если оператор имеет 2+ point-to-point интерфейса с тем же subnet, спрашиваем.
- ARP-based MAC pre-check ДО SSH — оставляем post-SSH verify, этого достаточно (никаких writes до verify). ARP на Windows требует прогрева кеша, ROI слабый.
- Юнит-тесты — пропускаем по запросу. Smoke на testkeen.

## Smoke на testkeen

1. **Routing collision detection (positive):** убедиться что у тебя локалка в 192.168.31.x AND SSTP активен → wizard `[3] Добавить новый роутер` → выводит "обнаружено N интерфейсов в 192.168.31.0/24" → предлагает SSTP-iface → `y` → `route print` показывает /32-маршрут с метрикой 1 → после деплоя `route print` чисто.
2. **Routing collision (negative):** отключить SSTP → `[3]` → должна быть только локалка в /24 → no-collision branch, проходит обычно.
3. **Routing cleanup на ошибке:** искусственно — поднять SSTP но перебить пароль агента → SSH-fail → route должен быть удалён всё равно (defer).
4. **MAC pin capture:** свежий agent install → проверить `wizard.toml` содержит `expected_mac` для нового nickname.
5. **MAC pin verify (positive):** `[2] Обновить компоненты` с pinned MAC → проходит.
6. **MAC pin verify (negative):** руками подменить `expected_mac` в `wizard.toml` на `"badmacc"` → `[2]` → fail с понятным сообщением, никаких writes не происходит.
