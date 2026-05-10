# wg-monitor — Bug Audit

Project: c:/Users/user/Projects/wg-monitor
Scope: backend (cmd/backend, internal/backend/*) + agent (cmd/agent, internal/agent/*).
Out of scope: cmd/deploy (already audited rc12-rc18), generated test files, .claude/worktrees/*.

Severity scale:
- **Критические** — может ломать прод-функцию, портить state, или открывать атаку.
- **Средние** — приводит к спорадическим сбоям, ложным алертам, утечкам ресурсов.
- **Низкие** — defense-in-depth, code-quality, edge-cases.

---

## Критические

### BUG-01 — Agent HTTP timeout строго меньше long-poll wait → cmd-channel сломан в продакшене

**File:** `cmd/agent/main.go:50`, `internal/agent/client.go:23-29`

```go
// main.go:50
client := agent.NewClient(cfg.Backend.URL, cfg.Backend.Token, Version, 10*time.Second)
// client.go:28
http: &http.Client{Timeout: timeout},   // 10s
// main.go:104
loop := cmdloop.New(client, runner, 30) // waitSec=30
```

`http.Client.Timeout` покрывает **всё** время от Dial до закрытия body. `Client.PollCommand` шлёт `?wait=30`, бэкенд держит соединение до 30 секунд — а клиент рвёт его на 10-й секунде с `context deadline exceeded` / `Client.Timeout exceeded`. Каждый long-poll **гарантированно** падает, cmdloop уходит в backoff (1s → 2s → ... → 60s). На практике это значит:

1. Кнопки restart_tunnel / diag_now / force_recheck / opkg_upgrade / route_status / version_audit / firmware_status / firmware_install почти не доходят до агента (только если случайно попали между ретраями).
2. Каждые 10 секунд бэк получает кучу 499/timeout-обрывов, нагрузка на goroutine queue.Dequeue.

**Fix:** разделить http-клиент. Либо ввести второй `*http.Client` для long-poll с `Timeout: 90*time.Second` (как в `tg.Client.LongPollHTTP`), либо вообще убрать `Client.Timeout` и положиться на `ctx` (передать deadline через `http.NewRequestWithContext`):

```go
type Client struct {
    ...
    http         *http.Client  // короткие RPC: Send/Post
    longPollHTTP *http.Client  // long-poll
}

func NewClient(baseURL, token, version string, timeout time.Duration) *Client {
    return &Client{
        ...
        http:         &http.Client{Timeout: timeout},
        longPollHTTP: &http.Client{Timeout: timeout + 90*time.Second}, // или 0 + ctx
    }
}

func (c *Client) PollCommand(ctx context.Context, waitSec int) (*wire.Command, error) {
    // ... use c.longPollHTTP.Do(req)
}
```

Проверить тестом: `TestPollCommand_LongPollTimeout` с сервером, удерживающим соединение 25 секунд.

---

### BUG-02 — Dispatcher.Handle (case Hard): state НЕ сохраняется при ошибке TG

**File:** `internal/backend/alerts/dispatcher.go:53-94`

```go
case state.Hard:
    threadID, err := di.ensureTopic(ctx, userID, nickname)
    if err != nil { return ... }
    ...
    mid, err := di.tg.SendMessageWithKeyboard(ctx, ..., text, ..., &kb)
    if err != nil {
        return err   // ← state НЕ сохранён
    }
    next := tr.Next
    next.LastAlertMsgID = &mid
    ...
    return di.d.State().Save(userID, checkName, next)
```

Если TG отдаёт 5xx / network error / rate-limit (TG любит 429), функция возвращает err, бэкенд пишет warn-лог и идёт дальше. Но `tr.Next.HardSince` (выставленный FSM) **никогда** не попадает в БД. На следующем `fail`-репорте FSM начинает с `prev.CurrentStatus = "fail"`, `ConsecutiveFails = N` (то, что было), снова кроссит порог, опять делает Hard-транзишн с **новым** HardSince=now, и снова шлёт alert. В итоге каждые 60 секунд (interval агента) при недоступности TG — пере-генерация HARD с свежим HardSince, как только TG отгладится — приходит alert с искажённым "since" временем.

**Fix:** разделить запись state и доставку TG. Сохранять FSM transition сразу, отправлять TG отдельно, при сбое TG — fallback на realert poller:

```go
case state.Hard:
    next := tr.Next
    if err := di.d.State().Save(userID, checkName, next); err != nil {
        return fmt.Errorf("persist hard state: %w", err)  // FSM stays consistent
    }
    threadID, err := di.ensureTopic(...)
    if err != nil { return err }
    ...
    mid, sendErr := di.tg.SendMessageWithKeyboard(...)
    if sendErr != nil {
        // state уже сохранён; realert podcatcher позже добьёт STILL-DOWN
        return sendErr
    }
    // upsert mid post-send
    next.LastAlertMsgID = &mid
    nowT := time.Now()
    next.LastAlertAt = &nowT
    return di.d.State().Save(userID, checkName, next)
```

Аналогично пересмотреть Recovery (строка 95-119) — там же.

---

### BUG-03 — events.LatestPerUser парсит только один формат timestamp; перестанет работать если modernc/sqlite поменяет формат

**File:** `internal/backend/db/events.go:38-46`

```go
s := tsStr.String
if i := strings.Index(s, " m="); i > 0 {
    s = s[:i]
}
ts, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", s)
```

`parseEventTS` (строка 189-208) уже умеет обходить **5 разных** форматов (`RFC3339Nano`, `RFC3339`, Go-стиль с/без наносекунд, etc.) — сделан после реальной поломки. А `LatestPerUser` копирует **только один** формат и не использует общий хелпер. Если modernc.org/sqlite в новой версии начнёт возвращать MAX(ts) как RFC3339 (стандартный SQLite ISO8601 timestamp), `time.Parse` упадёт и `LatestPerUser` всегда вернёт ошибку → heartbeat watcher навсегда не пошлёт OFFLINE (он `continue` на err).

**Fix:** заменить ручной парс на shared helper:
```go
ts, err := parseEventTS(tsStr.String)
if err != nil {
    return time.Time{}, err
}
return ts, nil
```

(плюс юнит-тест с RFC3339, RFC3339Nano и Go-стилем как параметрами).

---

### BUG-04 — pendingMaint.consume / consumePendingRebind: атакующий может вытеснить чужой токен указав не свой userID

**File:** `internal/backend/callbacks/maint.go:49-61`, `internal/backend/callbacks/router.go:952-962`

```go
// maint.go:49
func (s *pendingMaintStore) consume(userID int64, token string) (*pendingMaint, bool) {
    s.mu.Lock(); defer s.mu.Unlock()
    p, ok := s.m[token]
    if !ok { return nil, false }
    delete(s.m, token)              // ← удаляет ДО проверки userID
    if p.UserID != userID || time.Now().After(p.ExpiresAt) {
        return nil, false
    }
    return p, true
}

// router.go:952 (consumePendingRebind) — та же ошибка
pr, ok := r.pendingRebinds[token]
if !ok || pr.UserID != userID || time.Now().After(pr.ExpiresAt) {
    delete(r.pendingRebinds, token)  // ← удаляет даже если userID не совпал
    return nil, false
}
```

Поток атаки (или просто ошибки UI):
1. Юзер A нажал "🔁 Reboot router" (или "Confirm rebind"). Backend сохраняет `pendingMaint{UserID: A, Token: T, Expires: now+5m}`.
2. Кто-то в чате жмёт **другую** кнопку, в callback_data которой случайно или намеренно обыгран токен T (8 hex = 32 бита; не secure для guess attack, но и не нужно — достаточно одного нажатия любого пользователя в чате).
3. `consume(B, T)` с `B != A` находит запись, **удаляет** её, возвращает `(nil, false)`. Юзер A теперь не может подтвердить — снова открывает панель → 5 минут потеряно.
4. На реальной кнопке "✅ Подтвердить" чата `args.UserID` приходит из callback_data, а не из `q.From.ID` — это просто payload, который контролирует бот. Но любой пользователь в чате может ткнуть button и отправить callback с тем же payload.

В худшем случае это позволяет тупо DoS-ить функцию подтверждения у админа.

**Fix:** не удалять при `UserID` mismatch (только при expired):

```go
func (s *pendingMaintStore) consume(userID int64, token string) (*pendingMaint, bool) {
    s.mu.Lock(); defer s.mu.Unlock()
    p, ok := s.m[token]
    if !ok { return nil, false }
    if p.UserID != userID {
        return nil, false           // ← НЕ удалять
    }
    if time.Now().After(p.ExpiresAt) {
        delete(s.m, token)
        return nil, false
    }
    delete(s.m, token)
    return p, true
}
```

Дополнительно: перевести проверку `args.UserID` на привязку к `q.From.ID` (политика «only AdminUserID can confirm» уже есть в HandleMessage; в HandleCallback её нет — после policy reversal от 2026-04-30 любой member чата может тапать). Минимум — verifу что `args.UserID == r.cfg.AdminUserID` для destructive actions (router reboot, firmware install, route_rebind).

---

### BUG-05 — cmdloop.backoff: math.Pow для большого attempt → +Inf → time.Duration отрицательная → busy-loop

**File:** `internal/agent/cmdloop/loop.go:84-90`

```go
func (l *Loop) backoff(attempt int) time.Duration {
    d := time.Duration(math.Pow(2, float64(attempt-1))) * l.BackoffBase
    if d > l.BackoffMax {
        d = l.BackoffMax
    }
    return d
}
```

При attempt=63 `math.Pow(2,62) = 4.6e18` → `time.Duration` (int64 nanoseconds) clip-нется в `MaxInt64`. После умножения на `BackoffBase` (1s) переполнится и `d` станет отрицательной (или даже `MinInt64` — undefined в Go float→int64 при overflow). `d > l.BackoffMax` будет `false` (отрицательное < 60s), `sleepCtx` получит отрицательный duration, `time.NewTimer(-d)` сработает мгновенно → tight-loop без сна.

В сочетании с BUG-01 (10s timeout, каждый poll фейлится за 10s) `attempt` дойдёт до проблемного значения за ~10 минут после старта если бэкенд недоступен.

**Fix:** clamp attempt перед Pow:

```go
func (l *Loop) backoff(attempt int) time.Duration {
    if attempt > 30 { attempt = 30 }     // cap exponent
    d := time.Duration(math.Pow(2, float64(attempt-1))) * l.BackoffBase
    if d <= 0 || d > l.BackoffMax {
        d = l.BackoffMax
    }
    return d
}
```

Тот же паттерн в `callbacks/router.go:173` `wait := time.Duration(math.Min(math.Pow(2, float64(attempt)), 60)) * time.Second` — там `math.Min` спасает от Inf, но всё равно `attempt` не сбрасывается между сессиями (хотя он в основном цикле reset-ится на success — OK).

---

## Средние

### BUG-06 — Reporter.ForceResumed состыкуется с Run-таймером — двойной POST

**File:** `internal/agent/reporter.go:85-90`, `68-80`

```go
func (r *Reporter) ForceResumed(ctx context.Context) {
    r.mu.Lock(); r.forceResumed = true; r.mu.Unlock()
    r.sendOnce(ctx)        // ← может конкурить с Run-tick
}
```

`Run` — отдельная горутина, `ForceResumed` вызывается из cmdloop-горутины. Если ticker почти выстрелил, обе sendOnce могут запуститься одновременно → два concurrent runAll, два POST /v1/report, два набора events в БД с одним timestamp. На бэкенде это не race-condition но шум в логах и double FSM applies.

**Fix:** добавить `inFlight sync.Mutex` или `chan struct{} signal` чтобы sendOnce был serialized:

```go
type Reporter struct {
    ...
    sendMu sync.Mutex   // serialise sendOnce calls
}

func (r *Reporter) sendOnce(ctx context.Context) {
    r.sendMu.Lock()
    defer r.sendMu.Unlock()
    ...
}
```

---

### BUG-07 — heartbeat.Watcher: time.NewTicker(0) panic if ScanEvery=0 (no defaulting in NewWatcher)

**File:** `internal/backend/heartbeat/watcher.go:77-86`, `97-111`

```go
func NewWatcher(d *db.DB, off OfflineSender, cfg Config) *Watcher {
    if cfg.ResumeGrace <= 0 {
        cfg.ResumeGrace = defaultResumeGrace   // только Resume default
    }
    return &Watcher{...}
}

func (w *Watcher) Run(ctx context.Context) {
    ...
    t := time.NewTicker(w.cfg.ScanEvery)   // panic if 0
```

Сейчас спасает default в `LoadConfig` (config.go:144 устанавливает 30 если 0), но это hidden coupling. Любой тест, который строит Watcher с пустым Config, или будущий вызов из CLI — схватит panic.

**Fix:**
```go
func NewWatcher(d *db.DB, off OfflineSender, cfg Config) *Watcher {
    if cfg.ResumeGrace <= 0 { cfg.ResumeGrace = defaultResumeGrace }
    if cfg.ScanEvery <= 0   { cfg.ScanEvery = 30 * time.Second }
    ...
}
```

То же самое в `realert.NewPoller` (poller.go:36-38) — `TickEvery` не дефолтится; `RealertEvery` не дефолтится.

```go
func NewPoller(d *db.DB, tg TGSender, cfg Config) *Poller {
    if cfg.TickEvery <= 0    { cfg.TickEvery = 5 * time.Minute }
    if cfg.RealertEvery <= 0 { cfg.RealertEvery = 6 * time.Hour }
    return &Poller{d: d, tg: tg, cfg: cfg}
}
```

---

### BUG-08 — Race: handler.reportHandler не транзакционный — concurrent reports теряют FSM-update

**File:** `internal/backend/handler.go:127-145`

```go
for _, c := range rep.Checks {
    ...
    prev, _ := d.DB.State().Get(uid, c.Name)             // SELECT
    tr := state.Apply(prev, c.Status, time.Now(), ...)
    if err := d.Dispatcher.Handle(...); err != nil {     // INSERT/UPDATE incident_state
        ...
    }
}
```

Read-modify-write без БД-транзакции. На практике один агент шлёт по одному report за раз (single-goroutine reporter), но:
1. ForceResumed (см. BUG-06) может состыковаться с Tick → две конкурентные `Apply` → один сохранил Hard-state, другой перетёр вернувшись в Soft.
2. realert.Poller параллельно вызывает `BumpLastAlertAt` (это узкий update) — здесь риска нет, но если когда-нибудь добавят update полнее — будет lost-update.

**Fix:** обернуть весь блок per-check в транзакцию `d.DB.SQL().BeginTx(ctx, nil)` с retry на SQLITE_BUSY. Или хотя бы добавить optimistic-lock колонку `version` в incident_state и UPDATE WHERE version=?.

Минимальный fix без транзакций: исправить BUG-06 (serialize sendOnce на агенте) — снижает вероятность гонки до нуля в нормальной конфигурации.

---

### BUG-09 — paginate бьёт UTF-8 по байтам — mojibake в TG для длинной диагностики на русском

**File:** `internal/backend/alerts/command_result.go:82-110`

```go
for i := 0; i < len(body); i += per {
    end := i + per
    if end > len(body) { end = len(body) }
    chunks = append(chunks, body[i:end])
}
...
if len(rendered) > tgMaxMessageBytes {
    excess := len(rendered) - tgMaxMessageBytes
    rendered = rendered[:len(rendered)-excess]   // ← обрезает byte-by-byte
}
```

Кириллические символы в UTF-8 — 2 байта, эмодзи — 4 байта. Срез на байтовой границе разбивает code-point. Telegram **отображает** invalid-UTF-8 как `?` или replacement-char `<U+FFFD>`. Пользователь видит мусор на стыке.

Конкретный риск: `diag_now` от awg-manager обычно содержит много латиницы и небольшое количество кириллицы — но `opkg_upgrade` SmartUpgrade output на русском, и при упаковке (3500 байт) — почти гарантированно ломается.

**Fix:** перейти на rune-aware split:

```go
import "unicode/utf8"

func paginate(header, body string, maxChars int) []string {
    per := maxChars
    if per < 100 { per = 100 }
    var chunks []string
    runes := []rune(body)
    // оценка: ASCII = 1 byte/rune, кириллица = 2, эмодзи = 4. С учётом запаса делим
    // на bytes, не runes — оставляя room для UTF-8 умножения. Грубая верхняя оценка:
    // per_runes = per / 4
    // Точнее — пройти по байтам, но округлить до начала следующего code point.

    for i := 0; i < len(runes); {
        end := i
        chunkBytes := 0
        for end < len(runes) && chunkBytes+utf8.RuneLen(runes[end]) <= per {
            chunkBytes += utf8.RuneLen(runes[end])
            end++
        }
        if end == i { end = i + 1 } // safety
        chunks = append(chunks, string(runes[i:end]))
        i = end
    }
    ...
}

// и тот же подход к финальной обрезке rendered.
```

Альтернатива — использовать `golang.org/x/text/unicode/norm` или просто `runes := []rune(rendered); rendered = string(runes[:N])`.

---

### BUG-10 — TunnelsCheck эскалирует HARD от RestartCount навечно

**File:** `internal/agent/checks/tunnels.go:147-149`

```go
if pc.TunnelID != "" {
    if pc.Status != "" && pc.Status != "alive" {
        reasons = append(reasons, ...)
    }
    if pc.RestartCount > 0 {        // ← кумулятивный счётчик с awg-manager
        reasons = append(reasons, fmt.Sprintf("auto-restarted %dx", pc.RestartCount))
    }
}
```

`pc.RestartCount` — кумулятивное число рестартов pingCheck'ом с момента старта awg-manager. Один раз pingCheck перезапустил туннель → `RestartCount = 1` навсегда (до перезагрузки awg-manager или /pingcheck/reset, если такой endpoint есть). Это означает: если pc.Status=alive (туннель работает) **И** RestartCount>0, чек всё равно FAIL → FSM в HARD → алерт.

Нормальная логика: «один раз был auto-restart» — это история, а не текущая проблема. Должна быть OK если сейчас alive.

**Fix:** убрать `pc.RestartCount > 0` из reasons (это контекст, не failure). Оставить только в Details для отображения:

```go
// убрать
// if pc.RestartCount > 0 {
//     reasons = append(reasons, ...)
// }
// — RestartCount уже в details["ping_check_restart_count"], formatter покажет
//   его в "(auto-restart 2x)" в alerts/format.go writeTunnelBody
```

Либо: сравнивать с baseline (RestartCount, увиденный при предыдущем "alive" — требует state на агенте).

---

### BUG-11 — Reporter.persistLastReportAt может прятать tmp-file в случае краша между WriteFile и Rename

**File:** `internal/agent/reporter.go:198-215`

```go
tmp := r.statePath + ".tmp"
if err := os.WriteFile(tmp, body, 0o644); err != nil { return }
if err := os.Rename(tmp, r.statePath); err != nil { return }
```

Если процесс убит между WriteFile и Rename — `*.tmp` остаётся как мусор. Накапливается со временем (но при следующем persistLastReportAt перезаписывается тем же именем — WriteFile делает truncate). На самом деле OK — не накапливается, просто остаётся 1 stale tmp-файл.

Однако `os.Rename` на Linux atomic, на Windows — нет (но агент на Keenetic = Linux, OK).

Реальный bug: `loadLastReportAt` молча возвращает zero, если JSON broken. Это ОК (зайти в "первый запуск" режим), но **не логирует ошибку**. Если кто-то накосячил с tmp-файлом и он залип, агент молча будет считать каждый запуск "первым" — все resumed-логики ломаются.

**Fix:** залогировать `slog.Warn` при unmarshal-fail:

```go
if json.Unmarshal(body, &s) != nil {
    slog.Warn("reporter state corrupt; ignoring", "path", r.statePath)
    return time.Time{}
}
```

---

### BUG-12 — awgmgr Client: tunnelID идёт в URL без encoding

**File:** `internal/agent/awgmgr/client.go:218-231`

```go
func (c *Client) StartTunnel(ctx context.Context, tunnelID string) error {
    return c.post(ctx, "/api/control/start?id="+tunnelID, nil, nil)   // ← raw concat
}

func (c *Client) DeleteTunnel(ctx context.Context, tunnelID string) error {
    return c.post(ctx, "/api/tunnels/delete?id="+tunnelID, nil, nil)
}

func (c *Client) ReplaceConf(ctx context.Context, tunnelID, rawConf, name string) (*Tunnel, error) {
    return c.confPost(ctx, "/api/tunnels/replace?id="+tunnelID, rawConf, name)
}
```

Если `tunnelID` содержит `&` (например, "awg11&z"), запрос ломается. Сейчас awg-manager сам генерирует ID-шники из ascii ([a-z0-9]), но defense-in-depth.

**Fix:** `url.QueryEscape(tunnelID)`.

---

### BUG-13 — DNS rkn-probe порог захардкожен (≥2 of 3 domains) — сломается при <3 domain'ов в конфиге

**File:** `internal/agent/checks/dns.go:191-217`

```go
func (c DNS) rknProbe(ctx context.Context, ep ..., domains []string, ...) (blocked bool, ...) {
    susCount := 0
    for _, dom := range domains { ... }
    return susCount >= 2, perDomain    // ← magic number 2
}
```

Если оператор сконфигурировал `rkn_test_domains: ["rutracker.org"]` (1 домен) — порог 2 никогда не достигается, RKN-probe бесполезен. Хуже: порог 2 / 3 = 66% подозрительных. Если оператор поставил 5 доменов и 2 из них в реальности упали по сетевым причинам — false positive.

**Fix:** относительный порог:

```go
if len(domains) == 0 {
    return false, perDomain
}
threshold := (len(domains) * 2 + 1) / 3   // ceil(2/3 * N), минимум 1
return susCount >= threshold, perDomain
```

Или ещё проще: `susCount * 2 >= len(domains)`.

---

### BUG-14 — Reporter.Run: time.NewTicker(r.interval) panic if Interval=0

**File:** `internal/agent/reporter.go:68-80`

```go
func (r *Reporter) Run(ctx context.Context) {
    r.sendOnce(ctx)
    t := time.NewTicker(r.interval)       // ← panic if 0
```

`cfg.Agent.Interval()` (см. config) применяет default. Но защита снаружи — fragile.

**Fix:** в NewReporter:
```go
if r.interval <= 0 { r.interval = 60 * time.Second }
```

---

### BUG-15 — TG-relay-горутины в cmdResultHandler не отменяются при shutdown

**File:** `internal/backend/handler.go:246-277`

```go
go func(...) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    ...
}(ref, res)
```

`context.Background()` не привязан к серверному shutdown. При SIGTERM `srv.Shutdown` ждёт активные HTTP-обработчики, но эти горутины уже после `return` от хэндлера — их 30-секундный таймаут истечёт сам, но shutdown не дождётся. Это значит: при graceful-shutdown TG-relay'и могут отправить сообщения в TG **после** того как процесс уже думает что остановлен.

Минорно для работы, но стрёмно для логов и наблюдаемости.

**Fix:** передать в Deps shutdown-ctx как поле (доступный всем хэндлерам):

```go
// Deps
type Deps struct {
    ...
    ShutdownCtx context.Context   // baseCtx, signal-aware
}

// в хэндлере:
go func(...) {
    ctx, cancel := context.WithTimeout(d.ShutdownCtx, 30*time.Second)
    defer cancel()
    ...
}(...)
```

И в main.go добавить `wg.Wait()` после `srv.Shutdown` чтобы релеи успели завершиться.

---

### BUG-16 — Recovery handler: prev лукапится после tr.Next.HardSince потерян; ошибка swallow'ed

**File:** `internal/backend/alerts/dispatcher.go:100-102`

```go
prev, _ := di.d.State().Get(userID, checkName)
var hardSince time.Time
if prev.HardSince != nil {
    hardSince = *prev.HardSince
}
```

1. Ошибка от Get игнорируется. Если БД упала, prev — zero-value: hardSince остаётся zero, Recovery message покажет `Downtime: <unknown>` или ломает форматтер.
2. Read-after-write race: между Hard transition (Save) и Recovery (Get) другой код мог изменить state. Это OK, но при ошибке Get → silent zero.

**Fix:** при ошибке Get логировать и не падать, а использовать tr.Next или fallback nickname-only:

```go
prev, err := di.d.State().Get(userID, checkName)
if err != nil {
    slog.Warn("recovery: state.Get failed", "err", err)
}
```

---

### BUG-17 — Auth middleware принимает любой whitespace кроме space между "Bearer" и токеном

**File:** `internal/backend/auth.go:32-35`

```go
presented := strings.TrimPrefix(hdr, prefix)
if presented == "" || strings.HasPrefix(presented, " ") {
    http.Error(...)
    return
}
```

`prefix = "Bearer "` (с пробелом). Если в заголовке `"Bearer  token"` (два пробела), TrimPrefix снимает один — `presented = " token"` начинается с пробела → 401. OK. Если `"Bearer\ttoken"` (tab) — TrimPrefix не сработает (другой prefix), 401. OK. Если `"BearerTOKEN"` без пробела — TrimPrefix не сработает → 401. OK.

Но: `"Bearer\t"` сам по себе — `presented = "\t"`, не "" и не начинается с пробела → idёт в `lookup.GetByToken("\t")` → ErrUserNotFound → 401. Нормально.

Низкий — это OK, не баг. Снимаю с списка.

---

### BUG-18 — `dispatch_smart_reply` — collectActiveIncidents игнорирует acked, но не silenced

**File:** `internal/backend/callbacks/router.go:716-720`

```sql
SELECT check_name, hard_since, consecutive_fails
  FROM incident_state
 WHERE user_id = ? AND current_status = 'hard'
   AND (silenced_until IS NULL OR silenced_until < CURRENT_TIMESTAMP)
   AND acked = 0
```

ОК-логика, **но**: `CURRENT_TIMESTAMP` в SQLite возвращается как UTC string `'2026-05-10 14:30:00'`. `silenced_until` хранится как Go `time.UTC()` (events.go:18 `ts.UTC()` — strings). Формат сравнения должен работать (TEXT lexicographic в ISO порядке).

Однако: `SilencedUntil` сохраняется через `utcPtr` (state.go:195) который возвращает `interface{}` с `t.UTC()`. modernc/sqlite сериализует `time.Time` как RFC3339 по умолчанию — **не** как формат CURRENT_TIMESTAMP. RFC3339: `'2026-05-10T14:30:00Z'`. CURRENT_TIMESTAMP: `'2026-05-10 14:30:00'` (без Z, без T). Lex-сравнение `'2026-05-10T14:30:00Z' < '2026-05-10 14:30:00'` — `T` (0x54) > ` ` (0x20), значит `T-...Z > 'space-...'` → **always true** → silenced никогда не считается expired в SQL.

CRITICAL upgrade этого: smart-reply активно показывает silenced incidents. То же самое в `db.StaleHards` (state.go:170) — realert считает silenced incident "non-stale" (т.е. **никогда** не отправляет re-alert если silenced)? Wait:

```sql
AND (silenced_until IS NULL OR silenced_until < CURRENT_TIMESTAMP)
```

Это «не silenced». Если cравнение `T` vs ` ` всегда даёт `T > ' '`, то условие `silenced_until < CURRENT_TIMESTAMP` всегда **false** для уже-проставленных silenced. Значит:
- `StaleHards`: возвращает только incident'ы где `silenced_until IS NULL` → silenced **никогда** не получит realert. (probably **intended**.)
- `collectActiveIncidents`: в smart-reply silenced incident **скрыт** (по той же причине). Это, вероятно, **intended** behavior.

Я думал ловлю баг, но это поведение задумано: показывать только не-silenced. Однако стоит проверить, что **expired** silence-окно работает правильно — а оно как раз НЕ работает из-за форматов:

Сценарий: админ silenced incident на 1 час. Окно истекло. `silenced_until` = `'2026-05-10T14:30:00Z'`, `CURRENT_TIMESTAMP` = `'2026-05-10 16:00:00'`. Сравнение лексикографически: `'2026-05-10T14...' > '2026-05-10 16...'` (потому что T > space). Условие `silenced_until < CURRENT_TIMESTAMP` = **false**. Realert/smart-reply продолжают считать incident silenced **навсегда**.

**Это критичный баг наблюдаемости**: silence-окно **не истекает** в SQL.

**Severity: пере-классификация → Критические.**

**File:** `internal/backend/db/state.go:169`, `internal/backend/callbacks/router.go:719`.

**Fix:** не использовать SQL `CURRENT_TIMESTAMP`, передавать `time.Now().UTC()` через параметр в RFC3339:

```go
// state.go: StaleHards
func (s *StateRepo) StaleHards(cutoff time.Time) ([]StaleHard, error) {
    rows, err := s.d.db.Query(
        `SELECT user_id, check_name, hard_since FROM incident_state
         WHERE current_status = 'hard'
           AND last_alert_at < ?
           AND (silenced_until IS NULL OR silenced_until < ?)
           AND acked = 0`,
        cutoff.UTC(), time.Now().UTC())
    ...
}

// router.go: collectActiveIncidents
q, err := r.d.SQL().Query(`...
    AND (silenced_until IS NULL OR silenced_until < ?)
    AND acked = 0`, userID, time.Now().UTC())
```

(такая же проверка нужна у acked_until, если он используется в SQL — посмотри grep `acked_until`).

**Перенёс этот пункт в Критические как BUG-18-CRIT.**

---

### BUG-18-CRIT (был перенесён, см. выше) — Silence/Mute окна не истекают в SQL из-за format mismatch

См. подробности выше.

---

### BUG-19 — heartbeat.Watcher: `now := time.Now()` в начале scan'а — стареет за время прохода

**File:** `internal/backend/heartbeat/watcher.go:115-160`

```go
now := time.Now()
for _, u := range users {
    latest, err := w.d.Events().LatestPerUser(u.ID)
    ...
    stale := now.Sub(latest)              // ← now stale если scan долгий
    ...
    if err := w.off.SendOffline(ctx, u.ID, u.Nickname, stale); err != nil { ... }
}
```

Если в флоте ~100 пользователей и каждый `LatestPerUser` берёт 50ms (медленный диск), полный scan = 5s. Юзер #100 получает алерт с `stale` на 5s меньше реального. Минор. Также `now.Sub(rt) < cfg.ResumeGrace` использует stale `now` — в худшем случае грейс окно реально на 5 секунд короче.

**Fix:** обновлять `now := time.Now()` на каждой итерации:

```go
for _, u := range users {
    latest, err := ...
    if err != nil || latest.IsZero() { continue }
    now := time.Now()          // ← refresh
    stale := now.Sub(latest)
    ...
}
```

---

### BUG-20 — cmdResultHandler: при ConsumeOriginRef для ref.Action не в whitelist'е (route_*/version_audit/...) фолбэк падает в `default` через TGNotifier — но при этом ref был consumed; повторно нельзя

**File:** `internal/backend/handler.go:242-279`

Если `RoutesNotifier == nil` для action="route_status", блок else не идёт в default — switch уже завершился (нет default'a в этом case). Ref consumed, никакой relay не сделан. Юзер видит "обновляется…" вечно.

```go
case "route_status", "route_rebind":
    if d.RoutesNotifier != nil {
        go func(...) { ... }
    }
    // ← если RoutesNotifier == nil, ничего не происходит, ref всё равно consumed
```

То же для maint actions.

**Fix:** или fallback на TGNotifier при `RoutesNotifier == nil`, или **не** consume'ить ref если notifier не настроен. Минимум — log:

```go
case "route_status", "route_rebind":
    if d.RoutesNotifier == nil {
        d.Logger.Warn("route notifier not configured, dropping result", "cmd_id", res.ID)
        break
    }
    go func(...) { ... }
```

---

## Низкие

### BUG-21 — Queue.Dequeue: time.After leak до hold-timeout duration после раннего close

**File:** `internal/backend/cmd/queue.go:130-140`

```go
go func() {
    select {
    case <-ctx.Done():
    case <-time.After(holdTimeout):  // timer не cancel'ится при stop
    case <-stop:
        return
    }
    q.signal.Broadcast()
}()
```

Если stop сработал первым, `time.After`'s underlying timer всё ещё живёт до истечения. Память от Timer'а — мизер, GC соберёт после fire. Не критично.

**Fix (косметический):** заменить `time.After` на `time.NewTimer + Stop`:

```go
t := time.NewTimer(holdTimeout)
defer t.Stop()
select {
case <-ctx.Done():
case <-t.C:
case <-stop:
    return
}
```

Аналогично в `AwaitResult` (строка 184-191).

---

### BUG-22 — `pending[userID] = cmds[1:]` не освобождает underlying array

**File:** `internal/backend/cmd/queue.go:147`

```go
head := cmds[0]
q.pending[userID] = cmds[1:]
```

`cmds[1:]` шарит underlying array, который не освобождается до **полной** очистки очереди или GC. Если юзер периодически получает 100 команд, все они держатся в RAM до полного дренажа.

**Fix:** переаллокация после порога:

```go
if len(cmds) > 16 && len(cmds)-1 < cap(cmds)/4 {
    new := make([]wire.Command, len(cmds)-1)
    copy(new, cmds[1:])
    q.pending[userID] = new
} else {
    q.pending[userID] = cmds[1:]
}
```

Или просто список slice вместо slice (`container/list`).

---

### BUG-23 — handler reportHandler: `time.Now()` использует local TZ, FSM хранит как-есть → консистентность

**File:** `internal/backend/handler.go:141`

```go
ts := rep.Timestamp
if ts.IsZero() {
    ts = time.Now().UTC()      // ← UTC для events
}
...
tr := state.Apply(prev, c.Status, time.Now(), d.Thresholds)   // ← local TZ для FSM
```

`time.Time` в Go — абсолютное время, TZ только для display. Сохранение в SQLite — оба пути проходят через `.UTC()` в state.go (utcPtr). Так что в БД оба нормализованы. Но в логах `tr.Next.HardSince` форматтер `format.go` уже `.In(mscLoc())` делает. OK, не баг.

Снимаю.

---

### BUG-24 — upstream.Cache.Latest — race + thundering herd

**File:** `internal/backend/upstream/versions.go:71-88`

См. описание выше. Rare condition, минор.

---

### BUG-25 — actions.tunnel_enable/disable: NDMSName не валидируется → command injection через ndmc

**File:** `internal/agent/actions/runner.go:130`, `internal/backend/callbacks/parse.go:136-140`

```go
// runner.go
out, err := r.Exec(ctx, "ndmc", "-c", fmt.Sprintf("interface %s %s", ndms, state))
```

`ndms` — это аргумент к `-c`, который ndmc парсит как cli-команду. Если ndms = `Wireguard0; system reboot`, ndmc выполнит обе команды.

Источник — `parse.go:136-140`:
```go
if action == "tunnel_enable" || action == "tunnel_disable" {
    if len(parts) < 4 || parts[3] == "" {
        return ..., fmt.Errorf("requires ndms_name")
    }
    a.NDMSName = parts[3]            // ← без валидации
}
```

NDMSName приходит из callback_data, которое генерится бэкендом из awg-manager-данных (`tu.NDMSName`). Если awg-manager скомпрометирован или вернул злой NDMSName, его можно прокинуть.

**Fix:** valider NDMSName в parse.go:

```go
var validNDMSRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,31}$`)
if action == "tunnel_enable" || action == "tunnel_disable" {
    if len(parts) < 4 || !validNDMSRe.MatchString(parts[3]) {
        return ..., fmt.Errorf("invalid ndms_name")
    }
    a.NDMSName = parts[3]
}
```

И/или на стороне runner.go перед формированием cli-строки.

---

### BUG-26 — UI MuteCutoffHour=0 трактуется как «не задано» вместо «00:00»

**File:** `internal/backend/config.go:159-161`

```go
if cfg.State.MuteCutoffHour == 0 {
    cfg.State.MuteCutoffHour = 9
}
```

False zero: оператор не может явно указать 00:00 (полуночь) как cutoff — всегда 9.

**Fix:** перейти на pointer `*int` (как уже сделано для `DeleteUserCommandMessages`):

```go
type StateConfig struct {
    ...
    MuteCutoffHour *int `yaml:"mute_cutoff_hour"`
}

if cfg.State.MuteCutoffHour == nil {
    v := 9
    cfg.State.MuteCutoffHour = &v
}
```

Аналогично пересмотреть FailThreshold/RecoveryThreshold/RealertEverySec/etc — везде "0 = default" может разойтись с намерением оператора.

---

### BUG-27 — DB.Open не ставит SetMaxOpenConns(1) → SQLITE_BUSY на конкурентных пишущих транзакциях

**File:** `internal/backend/db/db.go:20-43`

modernc.org/sqlite поддерживает concurrent reads, но writes — single-writer (WAL). Стандартный паттерн:

```go
d.SetMaxOpenConns(1)  // или SetMaxOpenConns(N) + retry-on-busy
```

Без этого — sporadic `database is locked` errors при concurrent writes (heartbeat watcher + report handler + retention).

`busy_timeout(5000)` в DSN частично спасает, но не на 100%.

**Fix:**
```go
d, err := sql.Open(...)
...
d.SetMaxOpenConns(1)
```

Или две отдельные DSN: read-only и read-write.

---

### BUG-28 — alerts/format.go mscLoc() loadLocation на каждый вызов

**File:** `internal/backend/alerts/format.go:546-552`

Минорный perf-cost. Можно один раз в init().

---

### BUG-29 — Reporter.runAll нет cap на параллельность checks

**File:** `internal/agent/reporter.go:143-177`

Если у юзера 50 туннелей, MultiCheck выдаст 50 результатов в одной горутине + еще горутины на каждый Check. Не баг, но на слабом Keenetic CPU spike. Минорный.

---

### BUG-30 — runLoop в retention/policy.go: timer.Reset после fn — если fn>every, накапливается отставание (skew)

**File:** `internal/backend/retention/policy.go:61-75`

Не критично, retention spec'нут как best-effort.

---

## Сводка

| ID  | Severity     | Где                                          | Кратко |
|-----|--------------|----------------------------------------------|--------|
| 01  | Критический  | agent/client.go, cmd/agent/main.go           | 10s timeout vs 30s long-poll → cmd channel сломан |
| 02  | Критический  | alerts/dispatcher.go:53-94                   | Hard state не сохранён при TG-фейле → re-fire alerts |
| 03  | Критический  | db/events.go:38-46                           | LatestPerUser парсит только 1 формат |
| 04  | Критический  | callbacks/maint.go:49, router.go:952         | consume удаляет токен на user-mismatch → DoS |
| 05  | Критический  | cmdloop/loop.go:84-90                        | math.Pow overflow → busy-loop |
| 06  | Средний      | reporter.go:85-90                            | ForceResumed конкурирует с Run-tick |
| 07  | Средний      | heartbeat/watcher.go, realert/poller.go      | NewWatcher/NewPoller не дефолтят интервалы |
| 08  | Средний      | handler.go:127-145                           | Нет транзакции вокруг report processing |
| 09  | Средний      | alerts/command_result.go:82-110              | paginate бьёт UTF-8 по байтам |
| 10  | Средний      | agent/checks/tunnels.go:147-149              | RestartCount > 0 = HARD навечно |
| 11  | Средний      | agent/reporter.go:198-215                    | persistLastReportAt не логирует corrupt |
| 12  | Средний      | awgmgr/client.go:218-231                     | tunnelID без url.QueryEscape |
| 13  | Средний      | agent/checks/dns.go:191-217                  | RKN-probe порог захардкожен =2 |
| 14  | Средний      | reporter.go:68-80                            | Run panic if Interval=0 |
| 15  | Средний      | handler.go:246-277                           | Async-relay горутины не привязаны к shutdown |
| 16  | Средний      | alerts/dispatcher.go:100-102                 | Recovery: error на Get swallow'ed |
| 18  | Критический  | db/state.go:169, callbacks/router.go:719     | silenced_until vs CURRENT_TIMESTAMP format mismatch — silence не истекает |
| 19  | Средний      | heartbeat/watcher.go:121                     | scan(`now`) стареет в долгом проходе |
| 20  | Средний      | handler.go:242-279                           | route/maint actions без notifier — silent ref-leak |
| 21  | Низкий       | cmd/queue.go:130-140                         | time.After leak в Dequeue/AwaitResult |
| 22  | Низкий       | cmd/queue.go:147                             | pending slice не освобождает array |
| 25  | Низкий       | parse.go:136, runner.go:130                  | NDMSName injection в ndmc |
| 26  | Низкий       | config.go:159                                | MuteCutoffHour=0 не различается от unset |
| 27  | Низкий       | db/db.go:20-43                               | Без SetMaxOpenConns(1) — SQLITE_BUSY |
| 28  | Низкий       | alerts/format.go:546-552                     | mscLoc() loadLocation на каждом вызове |
| 29  | Низкий       | reporter.go:143-177                          | runAll без semaphore |
| 30  | Низкий       | retention/policy.go:61-75                    | runLoop reset skew |

