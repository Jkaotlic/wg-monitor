# Deploy identity & cleanup bundle — design

**Дата:** 2026-05-15
**Спринт:** v0.13.0-rc5 (или новая минорная цепочка)
**Связанные специ:**
- [2026-05-15-cold-install-reliability-design.md](2026-05-15-cold-install-reliability-design.md) — предыдущий rc3 bundle (routing detection + MAC-pin update guard). Расширяем оба.

## Проблема

Wizard 2026-05-15: при подключении по SSTP к удалённому Keenetic'у (192.168.31.1) и попытке install-агента (`[3]` или `[4]→[3]`), wizard молча ставит агента на **локальный** домашний роутер с тем же LAN-IP. Никаких ошибок: localу нет агента, identity-banner печатается-но-не-блокирует, OS-side routing решает за wizard'а кто получит пакеты.

Корней две:

1. **Пассивная детекция коллизии.** `detectRoutingCollision` ([cmd/deploy/routing.go:39-87](../../../cmd/deploy/routing.go#L39-L87)) проверяет только `iface.Addrs()` — попадает ли target в CIDR любого UP-интерфейса. SSTP типично пушит route `192.168.31.0/24 → sstp-gw` отдельной строкой в таблицу маршрутизации, а сам имеет `/32` (например `10.x.x.x/32`). `ipnet.Contains(192.168.31.1)` для SSTP-интерфейса возвращает `false` → коллизия не обнаружена → `/32`-фикс не предлагается → пакеты уходят через LAN.

2. **Cold-install без identity-gate.** `actionInstallAgent` ([cmd/deploy/actions.go:495-541](../../../cmd/deploy/actions.go#L495-L541)) печатает hostname+MAC, но НЕ требует подтверждения. `existingNick == ""` (на локальном агент не стоял) → wizard считает «чистый роутер, OK» → cold install идёт. `verifyExpectedMAC` тоже no-op потому что `ExpectedMAC == ""` для первого install'а.

3. **Нет уборки.** Когда оператор всё-таки осознал «я установил не туда» — снять агента можно только руками: SSH + 6 команд rm + killall. Нет wizard-action, нет inline-предложения после детекта ошибки.

## Решение — 4 слоя

```
┌─────────────────────────────────────────────────────────────┐
│ Layer 1: Active path discovery                              │
│   wizard перебирает все live интерфейсы (LAN + P2P),        │
│   пробует TCP-handshake через каждый, показывает кто        │
│   ответил. Выбирает SSTP-путь авто или спрашивает на        │
│   ambiguity. Закрепляет /32 на время SSH-сессии.            │
├─────────────────────────────────────────────────────────────┤
│ Layer 2: Cold-install identity confirm-gate                 │
│   после identity-banner (hostname+MAC+arch) для cold-       │
│   install'а — обязательный `[y/N]` дефолт N. На `y`         │
│   фиксируем MAC в wizard.toml как ExpectedMAC.              │
├─────────────────────────────────────────────────────────────┤
│ Layer 3: Reachability diagnostic + heartbeat cross-check    │
│   когда никто не отвечает — cascade:                        │
│   • есть ли live P2P intf?                                  │
│   • что говорит VPS heartbeat для этого nickname?           │
│   • TCP refused vs timeout — разные подсказки               │
├─────────────────────────────────────────────────────────────┤
│ Layer 4: Uninstall action                                   │
│   `[N] Удалить агента с роутера` — обратное к install,      │
│   сносит /opt-артефакты после двойного confirm'а. На detect │
│   «агент на двух роутерах одновременно» Layer 1 предлагает  │
│   запустить cleanup inline.                                 │
└─────────────────────────────────────────────────────────────┘
```

Слои независимы по реализации, но связаны логически. Layer 1 заменяет существующую rc3 пассивную детекцию (`detectRoutingCollision` уходит). Layer 2 расширяет существующий identity-banner. Layer 3 — новый. Layer 4 — новый action + интеграция в Layer 1.

---

## Layer 1: Active path discovery

### Цель

Перед SSH wizard сам должен **найти удалённый роутер**, перебрав все доступные сетевые пути, а не положиться на default-route выбор ОС.

### API

```go
// PathReport — что выяснилось при попытке достучаться до target:port.
// Все интерфейсы пробуются параллельно с общим timeout'ом.
type PathReport struct {
    Target     string          // "192.168.31.1:222"
    Candidates []PathCandidate // отсортированы: P2P первыми, рабочие выше
    Chosen     *PathCandidate  // nil если никто не ответил
    Multiple   bool            // ≥2 кандидатов ответили — оператора спрашивали
}

type PathCandidate struct {
    Iface       string        // "Ethernet" / "SSTP-Client"
    Index       int           // OS iface index
    LocalIP     string        // адрес интерфейса
    Kind        PathKind      // PathLAN / PathP2P
    Latency     time.Duration // TCP HS latency, 0 если fail
    Err         error         // nil если success
}

type PathKind int
const (
    PathLAN PathKind = iota // regular eth/wifi
    PathP2P                 // FlagPointToPoint — SSTP/VPN/PPP
)

// stepFindReachablePath probes target via every UP interface, with optional
// /32 host route per P2P iface to force the dial through that path. Returns
// the chosen candidate (or nil) and a cleanup func that removes any temporary
// routes installed during probing.
func stepFindReachablePath(target string, port int) (*PathReport, func(), error)
```

### Алгоритм

```
1. Enumerate net.Interfaces() — фильтруем UP, не lo. Классифицируем:
   - есть FlagPointToPoint → PathP2P (SSTP, OpenVPN, WG, PPP)
   - иначе → PathLAN

2. errgroup с общим timeout 5с, параллельно:
   a. Default-route probe: net.DialTimeout(target, 3с). На success — из
      conn.LocalAddr() резолвим имя iface (по местному IP сопоставляем с
      enum'ом). На fail — отмечаем "default → <err>".

   b. Per-P2P probe: для каждого P2P-интерфейса:
      - addTempHostRoute(target_ip, iface.Index)  // platform-specific
      - net.DialTimeout(target, 3с)
      - delTempHostRoute(target_ip)
      - record latency / err

   c. Per-LAN probe (опционально): обычно default-route уже покрывает
      LAN-путь, но если у оператора 2+ LAN-интерфейса в одной /24 —
      пробуем каждый отдельно (тот же trick с /32).

3. Aggregate в PathReport. Decision tree:

   a. Один кандидат ответил:
      - P2P → auto-pick, удерживаем /32 на время сессии (cleanup при выходе)
      - LAN → принимаем как-есть (нет SSTP, оператор работает локально)

   b. Multiple ответили:
      print таблицу всех кандидатов с латенси, спрашиваем:
        > Кто из них правильный? [1=SSTP-Client (~140мс) / 2=Ethernet (~5мс)]
      На выбор P2P — установить /32. На выбор LAN — без /32 (default route и
      так туда идёт).

   c. Никто не ответил:
      → передаём управление в Layer 3 cascade.

4. Return PathReport + cleanup. Cleanup идемпотентен (safe to call multiple
   times), удаляет ВСЕ временные /32 которые добавил probe.
```

### Платформенная реализация

**Windows** (`routing_windows.go`):
- `addTempHostRoute(ip, ifIdx)`: `route ADD <ip> MASK 255.255.255.255 0.0.0.0 IF <ifIdx> METRIC 1`
- `delTempHostRoute(ip)`: `route DELETE <ip>`
- Требует прав администратора. Если `addTempHostRoute` падает с access denied — печатаем «запусти wizard от Администратора, без этого не могу гарантировать через какой путь пойдёт SSH» и пробуем default-route без force.

**Linux** (`routing_unix.go`, build tag `linux`):
- `ip route add <ip>/32 dev <iface> metric 1` — пробуем direct
- На permission denied — `sudo ip route add ...` с heads-up печатью
- Cleanup: `ip route del <ip>/32`

**macOS** (`routing_unix.go`, build tag `darwin`):
- `route -n add -host <ip> -interface <iface>` direct → `sudo route ...` fallback
- Cleanup: `route -n delete -host <ip>`

**Прочее** (`routing_other.go`): no-op, fall back на default routing.

### Интеграция в actions

В `actionInstallAgent` и `actionUpdateAgent` — заменить текущий `defer setupRouteFix(ag.Host)()` на:

```go
PrintStep(0, N, "Поиск роутера")
report, cleanup, err := stepFindReachablePath(ag.Host, ag.Port)
defer cleanup()
if err != nil || report.Chosen == nil {
    return diagnoseUnreachable(ag, report, state, secrets) // Layer 3
}
if report.Multiple {
    PrintInfo("используется " + report.Chosen.Iface + " (" + report.Chosen.Latency.String() + ")")
}
// Дальше — обычный SSH к ag.Host через закреплённый путь
```

### Кэширование чтобы не пробивать каждый раз

В `wizard.toml` `AgentState` добавляем:
```go
PreferredIface string `toml:"preferred_iface,omitempty"` // "SSTP-Client"
```

На успешный install/update — сохраняем `report.Chosen.Iface`. На следующем запуске Layer 1 пробует ТОЛЬКО его сначала; если ответил — пропускаем full enumeration. Если не ответил — full probe + перезаписываем `preferred_iface`.

---

## Layer 2: Cold-install identity confirm-gate

### Цель

Гарантировать что **первый** install под nickname идёт на правильный физический бокс. Поздно ловить после write — `verifyExpectedMAC` срабатывает только на update'ах.

### Изменение

В `actionInstallAgent` ([cmd/deploy/actions.go:518](../../../cmd/deploy/actions.go#L518)) после `PrintInfo(fmt.Sprintf("hostname=%q  mac=%s", ...))`:

```go
// Cold-install identity gate: до первого install'а ExpectedMAC ещё пуст,
// поэтому verifyExpectedMAC не сработает. Это последний шанс операторской
// проверки — что физический бокс совпадает с тем, который мы регистрируем
// под nickname'ом. WG_YES_TO_ALL=1 пропускает (скриптовые прогоны).
if ag.ExpectedMAC == "" && os.Getenv("WG_YES_TO_ALL") != "1" {
    msg := fmt.Sprintf(
        "Это правильный роутер для install под nickname=%q? "+
            "(hostname=%q mac=%s arch=%s) [y/N]",
        ag.Nickname, hostname, mac, arch,
    )
    ans := strings.ToLower(strings.TrimSpace(Ask(msg, "")))
    if ans != "y" && ans != "yes" && ans != "д" && ans != "да" {
        PrintFail("install отменён оператором")
        return fmt.Errorf("install cancelled — identity not confirmed")
    }
}
```

Default — N (пустой ответ = N). Безопасно для случая «оператор отлучился и нажал Enter не глядя».

### Что НЕ меняем

- Update flow — `verifyExpectedMAC` уже жёсткий fail-stop, дополнительный prompt не нужен.
- Re-install (existingNick == ag.Nickname) — banner показывает существующий nickname на роутере, оператор видит «переустановка». Гейт всё равно сработает потому что для re-install'а тоже first-time-on-this-PC может быть.

### Логика захвата MAC после confirm

`ag.ExpectedMAC = normalised` ([cmd/deploy/actions.go:524-526](../../../cmd/deploy/actions.go#L524-L526)) — этот блок остаётся, выполняется ПОСЛЕ confirm'а. То есть MAC пинится только если оператор сказал `y`.

---

## Layer 3: Reachability diagnostic cascade

### Цель

Когда Layer 1 не нашёл ни одного отвечающего пути — дать оператору ясный диагноз и actionable hint, вместо cryptic SSH timeout.

### `diagnoseUnreachable(ag, report, state, secrets)`

```
1. Print список того что пробовали + результат:
     Default route → Ethernet: timeout
     Forced via SSTP-Client:   timeout
     Forced via OpenVPN:       не пробивали — оператор не админ

2. Проверки:
   a. Live P2P интерфейсы:
      - len(P2P UP) == 0 → "у тебя нет ни одного VPN/SSTP интерфейса.
        Если ожидал, что роутер через тоннель — подними сначала клиент."
      - есть P2P, но все timeout → "SSTP/VPN up, но через них роутер не
        отвечает. Возможно сервер не маршрутизирует target, или удалённый
        firewall блокирует :222."

   b. VPS heartbeat для этого nickname (если ag.Nickname в state.Agents):
      - GET /v1/wizard/agents → найти {nickname: ag.Nickname}
      - last_seen_at < 5мин назад → "роутер ОТЧИТЫВАЛСЯ на бэкенд 47с
        назад. Он жив на сети, проблема в сетевом пути ОТ ТЕБЯ. SSTP реально
        активен? Может тебе нужен другой VPN?"
      - last_seen_at > 5мин назад → "роутер не отчитывался Nмин — возможно
        выключен или агент упал. Проверь питание, попробуй позже."
      - never → "впервые ставим. Нужен out-of-band доступ — попроси клиента
        выслать его LAN-IP или роутер вживую."

   c. Особые случаи Dial error:
      - "connection refused" → ":222 закрыт. SSH на роутере либо не на 222,
        либо port forward сломан, либо удалённый firewall."
      - "no route to host" / "network unreachable" → ICMP-ответ есть от
        промежуточного хопа, но сам host не доступен. На той же сети?
      - "i/o timeout" → совсем глухо. Чаще всего — wrong subnet / SSTP down.

3. Возврат → bail из deploy с одним из этих сообщений. НЕ retry-loop SSH.
```

### Backend: добавить `last_seen_at` в `/v1/wizard/agents`

[internal/backend/wizard_handler.go:55-65](../../../internal/backend/wizard_handler.go#L55-L65) — в `wizardAgent` struct:

```go
type wizardAgent struct {
    Nickname            string     `json:"nickname"`
    Kind                string     `json:"kind"`
    ThreadID            int64      `json:"thread_id"`
    SSHHost             string     `json:"ssh_host"`
    SSHPort             int64      `json:"ssh_port"`
    SSHUser             string     `json:"ssh_user"`
    Arch                string     `json:"arch"`
    LastDeployedVersion string     `json:"last_deployed_version"`
    LastSeenAt          *time.Time `json:"last_seen_at,omitempty"` // NEW
    HasTopic            bool       `json:"has_topic"`
}
```

Handler уже читает `users` таблицу — `LastSeenAt` там есть как `*time.Time`. Один маппинг в `wizardListAgentsHandler`.

В `cmd/deploy/vps_sync.go` `RemoteAgent` зеркалит поле. `MergeAgents` его НЕ применяет к state.Agents (transient view, не пишем в wizard.toml).

### Утилита `heartbeatStatus(domain, token, nickname) (string, error)`

В `cmd/deploy/vps_sync.go`:

```go
// HeartbeatStatus returns human-readable heartbeat freshness:
//   "fresh 47s" / "stale 14m" / "never" / "" if backend unreachable.
func (c *VPSClient) HeartbeatStatus(ctx context.Context, nick string) string
```

Вызывается из `diagnoseUnreachable`. На любой ошибке (offline, 401) — пустая строка, тогда соответствующая ветка диагноза пропускается.

---

## Layer 4: Uninstall action

### Цель

Снять агента с роутера, на который он попал ошибочно. Полная очистка `/opt`-артефактов + опционально сбросить MAC-pin в wizard.toml для повторной попытки.

### Меню

`cmd/deploy/menu.go` — добавить пункт:
```
[N] Удалить агента с роутера
```
Между `[5] Доктор` и `[6] Выход` (точный номер от текущей нумерации).

CLI:
```bash
wg-monitor-deploy --uninstall <nickname>       # из wizard.toml
wg-monitor-deploy --uninstall-host <host> [--port 222] [--user root]
```

### `actionUninstallAgent`

```go
func actionUninstallAgent(state *State, secrets *SecretStore, target UninstallTarget) error {
    // target: либо из state.Agents[nick], либо введён руками
    // 1. SSH connect (с Layer 1 path discovery — может тебе нужно через SSTP)
    // 2. Identity banner: hostname + MAC + existingNick из config.yaml
    // 3. Confirm: "Снести агента с %s (nickname на роутере: %q)? [y/N]"
    // 4. Cleanup pipeline:
    //    a. /opt/etc/init.d/S99wg-monitor stop 2>/dev/null
    //    b. killall -9 wg-monitor 2>/dev/null
    //    c. sleep 1
    //    d. rm -f /opt/bin/wg-monitor /opt/bin/wg-monitor.bak /opt/bin/wg-monitor.new
    //    e. rm -rf /opt/etc/wg-monitor/
    //    f. rm -f /opt/etc/init.d/S99wg-monitor
    //    g. rm -rf /opt/var/wg-monitor/
    // 5. Verify: pidof wg-monitor пусто, ls /opt/bin/wg-monitor → not exist
    // 6. Optional secondary prompt: "Снять expected_mac для nickname=X в
    //    wizard.toml, чтобы можно было заново установить? [y/N]"
    //    На y — ag.ExpectedMAC = "" + SaveState.
    // 7. PrintOK("чисто на " + host)
}
```

### Что НЕ трогаем

- `users` таблица на VPS — оставляем. Токен на бэкенде тот же, понадобится для install'а на правильный бокс.
- awg-manager / HR-Neo / прочие Entware пакеты — гости.
- `/opt` саму — гости.

### Интеграция в Layer 1

Когда Layer 1 находит два рабочих пути И на обоих стоит агент под одним nickname (resolved через `stepReadExistingAgentNickname` после SSH через каждый путь) — это маркер «ошибочный двойной деплой». Wizard inline предлагает:

```
⚠ агент "client_smith" найден на ДВУХ роутерах:
  • локальный (192.168.31.1 через Ethernet)   hostname=keenetic-home  mac=aa:bb:cc:..
  • удалённый (192.168.31.1 через SSTP)        hostname=keenetic-xyz   mac=11:22:33:..

[1] Снять с локального → продолжить install на удалённом
[2] Отмена, разберусь сам
```

На `[1]` запускает `actionUninstallAgent(target=localPath)` потом продолжает с remote.

### Тест-стратегия

`cmd/deploy/actions_test.go` — `TestUninstall_RemovesAllArtifacts`:
- Fake SSH session с записью команд
- Запускаем uninstall, ожидаем все 6 команд в логе
- Verify-фаза тоже мокается (pidof пуст, ls fail)

---

## Конфликты и совместимость

### Существующий код, который удаляем

- `detectRoutingCollision` / `describeCollision` / `setupRouteFix` в `cmd/deploy/routing.go` — заменяются Layer 1.
- `routeCollision` / `ifaceMatch` структуры — заменяются `PathReport` / `PathCandidate`.
- `addHostRouteThroughInterface` (`routing_windows.go`/`routing_unix.go`) — переименовать в `addTempHostRoute`, расширить под параметризацию (target + ifIdx).
- `routing_other.go` no-op остаётся.

### Существующий код, который остаётся

- `verifyExpectedMAC` ([cmd/deploy/steps.go:493](../../../cmd/deploy/steps.go#L493)) — Layer 2 захватывает MAC, эта функция продолжает проверять на update.
- `stepReadExistingAgentNickname` ([cmd/deploy/steps.go:524](../../../cmd/deploy/steps.go#L524)) — Layer 1 использует её для детекта «агент на двух роутерах».
- `actionDoctor` ([cmd/deploy/doctor.go:82](../../../cmd/deploy/doctor.go#L82)) — независим, остаётся как есть.

### Wizard.toml schema

Новые опциональные поля в `AgentState`:
- `ExpectedMAC string `toml:"expected_mac,omitempty"`` — УЖЕ ЕСТЬ (rc3)
- `PreferredIface string `toml:"preferred_iface,omitempty"`` — НОВОЕ (Layer 1 cache)

Старые wizard.toml без этих полей — back-compat работает (пустое значение = нет кэша, Layer 1 делает full probe).

### Env-vars

Старые сохраняем как-есть:
- `WG_YES_TO_ALL=1` — теперь пропускает и Layer 2 confirm, и Layer 1 ambiguity-prompt
- `WG_NO_BANNER=1` — не трогаем
- `WG_NO_PULL=1` — не трогаем (pull-flow остаётся)

Новых не вводим.

## Outstanding / решено за рамками этого спека

- **Active probe требует прав администратора на Windows.** Делаем graceful degradation: если `route ADD` упал с access-denied — печатаем подсказку, продолжаем с default-route probe only. Не пытаемся auto-elevate (UAC промпт посреди flow — UX ужасный).
- **Permission elevation на Linux/macOS.** rc4 уже умеет sudo-fallback, расширяем под несколько вызовов (probe → возможно несколько iface'ов подряд).
- **Cleanup на panic/Ctrl-C.** Если probe-route добавили и wizard упал — route останется. Подписываемся на `signal.Notify(SIGINT, SIGTERM)`, на signal — синхронно вызываем cleanup перед exit.
- **VPS-side deletion агента при cleanup.** Не делаем — токен на VPS остаётся, ре-install на правильный бокс использует тот же токен. Если оператор хочет удалить и из DB — это явное действие в wg-monitor-cli, не в wizard.

## Файлы (полный список)

| Файл | Изменение |
|---|---|
| `cmd/deploy/routing.go` | replace `detectRoutingCollision` + `setupRouteFix` → `stepFindReachablePath`, `PathReport`, `PathCandidate`, `PathKind` |
| `cmd/deploy/routing_windows.go` | rename `addHostRouteThroughInterface` → `addTempHostRoute`, idempotent cleanup |
| `cmd/deploy/routing_unix.go` | то же + sudo-fallback расширен под повторные вызовы |
| `cmd/deploy/routing_other.go` | stub под новую сигнатуру |
| `cmd/deploy/actions.go` | `actionInstallAgent` / `actionUpdateAgent` — заменить `defer setupRouteFix` на `stepFindReachablePath` + cleanup; добавить cold-install gate после banner; новый `actionUninstallAgent` + `diagnoseUnreachable` |
| `cmd/deploy/state.go` | добавить `PreferredIface string` в `AgentState` |
| `cmd/deploy/menu.go` | пункт `[N] Удалить агента` + диспатч; auto-detect двойного деплоя при install |
| `cmd/deploy/main.go` | CLI flag `--uninstall` |
| `cmd/deploy/vps_sync.go` | `LastSeenAt *time.Time` в `RemoteAgent`; `HeartbeatStatus(ctx, nick) string` |
| `internal/backend/wizard_handler.go` | `LastSeenAt` в `wizardAgent` JSON response |
| `cmd/deploy/routing_test.go` | unit-tests с fake-listener'ом на нескольких портах |
| `cmd/deploy/actions_test.go` | tests на cold-install gate (positive/negative), uninstall cleanup, diagnoseUnreachable cascade ветки |
| `internal/backend/wizard_handler_test.go` | тест что `last_seen_at` в JSON output для пользователя с не-nil last_seen_at |

## Что НЕ в этом спеке

- Cleanup VPS-side state — отдельная задача (`wg-monitor-cli delete-user --keep-history`)
- UAC elevation на Windows — out of scope (handle с graceful degradation)
- TG-side notification про "wrong-router cleanup" — не нужно, операторская локальная операция
- Bootstrap-kit (cold-install без SSTP, Вариант 2 из project memory) — отдельный спек

## Acceptance smoke

1. **Layer 1 happy path:** SSTP up, default route в LAN, target отвечает на оба пути → wizard спрашивает кого выбрать → выбираю SSTP → `/32` добавлен → SSH через SSTP → install проходит. На exit `route DELETE` снимает /32.
2. **Layer 1 SSTP only:** SSTP up, LAN target не отвечает (нет коллизии IP) → wizard auto-выбирает SSTP → /32 устанавливается.
3. **Layer 1 fail:** SSTP down, target unreachable → Layer 3 cascade: «нет P2P intf, VPS heartbeat 14мин назад — роутер вероятно выключен».
4. **Layer 2:** cold install под `client_smith`, нажимаю Enter (default N) → install отменяется. Запускаю снова, нажимаю `y` → install идёт, MAC зафиксирован.
5. **Layer 3 heartbeat fresh:** ставим агент, отрубаем SSH-доступ на роутере (`iptables -A INPUT -p tcp --dport 222 -j DROP`), но heartbeat продолжается → запускаем `[2] Обновить` → wizard говорит «heartbeat 30с назад, проблема в SSH-пути, не в самом боксе».
6. **Layer 4 uninstall:** на роутере где случайно поставили — `[N] Удалить`, hostname/MAC показал, confirm `y` → все 6 артефактов удалены, `pidof wg-monitor` пуст.
7. **Layer 4 inline-suggest:** ставим агент сначала на local (ошибка), потом запускаем `[3]` снова с SSTP up → Layer 1 видит агент на обоих путях → предлагает [1] снять с local → cleanup → продолжает install на remote.
