# wg-monitor — Design Spec

**Дата:** 2026-04-25 (UPD 2026-04-26: все 5 open questions resolved, см. секцию «Resolved questions» в конце)
**Автор:** Anatoly (asnekhaev@gmail.com)
**Статус:** Approved → ready for Stage 0 implementation plan

## 1. Контекст и мотивация

Я администрирую WireGuard/AmneziaWG-инфраструктуру для ~10 пользователей. У всех однородная топология:
- Роутер Keenetic с Entware
- AmneziaWG-менеджер
- Опционально: Hydra Route Neo, nfqws2-keenetic

Когда у юзера «не работает интернет», причина обычно в одной из 4 точек: упал AWG-туннель, сломалась маршрутизация через VPN, заблокирован DoH/DoT провайдер, или роутер вообще офлайн. Сейчас я узнаю об инцидентах от самих юзеров с задержкой в часы и без контекста. Цель проекта — получать оповещения первым, с точной диагностикой, и иметь возможность реагировать прямо из Telegram без ssh-сессии в большинстве случаев.

## 2. Цели и не-цели

### Цели
- Раннее обнаружение инцидентов (обнаружение в пределах 3 минут)
- Доставка алертов **только мне** (получатель — единственный admin)
- Контекст в каждом алерте: какой клиент, какая проверка, динамика событий
- Возможность типичных действий из TG без ssh: silence, диагностика, рестарт AWG, opkg upgrade
- Минимальный footprint на роутере (overlay-память Keenetic ограничена)
- Масштабируется до ~30 юзеров без архитектурных изменений

### Не-цели
- Алерты юзерам и UI для них (только мне)
- Web-дашборд (TG топики и есть дашборд)
- Метрики Prometheus / Grafana
- Multi-tenant / RBAC (single-admin)
- Произвольное удалённое исполнение shell-команд (только whitelisted)

## 3. Высокоуровневая архитектура

```
┌──────────────┐
│ Keenetic 1   │ ──┐
│  + agent     │   │                                   ┌──────────────────────────┐
└──────────────┘   │                                   │ Telegram Supergroup      │
┌──────────────┐   │   HTTPS (push events,             │  ├─ General              │
│ Keenetic 2   │ ──┼── long-poll commands)             │  ├─ 📊 Сводка            │
│  + agent     │   │                                   │  ├─ 🔧 Системное         │
└──────────────┘   ├──→ ┌─────────────────────────┐ ──→│  ├─ 👤 Вася              │
       ...         │    │ Backend (Go)            │    │  ├─ 👤 Петя              │
                   │    │ on VPS Main (.253)      │    │  └─ ...                  │
┌──────────────┐   │    │  + SQLite state         │    └──────────────────────────┘
│ Keenetic N   │ ──┘    │  + tg bot               │             ↑ admin (me)
│  + agent     │        │  + weekly cron          │             ↓ button callbacks
└──────────────┘        └─────────────────────────┘
```

Коммуникация:
- **Agent → Backend**: HTTPS POST `/v1/report` (push событий каждые 60 сек)
- **Backend → Agent**: long-poll. Агент GET `/v1/cmd?token=X` с холдом до 30 сек, бэкенд отдаёт команды по мере поступления.
- **Backend → Telegram**: Bot API, sendMessage с `message_thread_id` для топиков.
- **Telegram → Backend**: getUpdates long-poll (для обработки inline button callbacks).

## 4. Компонент: Agent

### 4.1 Технология и размер
- Язык: Go, кросс-компиляция под `mipsel-3.4` и `aarch64-3.10` (две основные архитектуры Keenetic под Entware)
- Build flags: `-ldflags="-s -w" -trimpath`, упаковка `upx --best`
- Целевой размер бинарника: **1.5–3 MB**
- RSS в работе: ~5–10 MB
- Установка: бинарник в `/opt/bin/wg-monitor`, конфиг в `/opt/etc/wg-monitor/config.yaml`, autostart через `/opt/etc/init.d/S99wg-monitor`
- Локальный кэш: `/opt/var/wg-monitor/` (буфер событий, lock-файлы, логи)

### 4.2 Конфиг (`config.yaml`)

```yaml
backend:
  url: https://monitor.your.tld
  token: <per-user secret, hex 32 bytes>

agent:
  nickname: vasya              # для дебага локально, источник истины — БД бэкенда
  interval_sec: 60             # период основного цикла
  buffer_max_bytes: 1048576    # 1 MB rolling JSONL
  command_poll_sec: 30         # long-poll cmd channel

checks:
  awg:
    interface: awg0
    handshake_max_age_sec: 180
    expected_exit_ip: 89.125.101.122   # ожидаемый IP при curl ifconfig.me через туннель
    marker_url: https://www.youtube.com/-/manifest
  dns:
    providers:
      - { name: cloudflare, type: dot, host: 1.1.1.1 }
      - { name: google,     type: dot, host: 8.8.8.8 }
      - { name: quad9,      type: dot, host: 9.9.9.9 }
    test_domain: example.com
    fail_threshold: 2          # считать FAIL если упали ≥2 из 3 провайдеров
```

### 4.3 Цикл проверок (каждые `interval_sec`)

| Check | Метод | Fail-критерий |
|---|---|---|
| `awg_handshake` | parse `wg show <iface> latest-handshakes` | last handshake > 180 сек |
| `awg_routing` | `curl --interface <iface> --max-time 5 https://api.ipify.org` | exit IP ≠ `expected_exit_ip` |
| `awg_marker` | `curl --interface <iface> --max-time 8 <marker_url>` | HTTP не 2xx за 3 попытки с экспоненциальным backoff |
| `dns_doh` | `dig +tls @<host> <test_domain>` для каждого провайдера | упали ≥ `fail_threshold` из N |

После каждого цикла агент шлёт POST `/v1/report`:

```json
{
  "ts": "2026-04-25T20:00:00Z",
  "agent_version": "0.1.0",
  "checks": [
    {"name": "awg_handshake", "status": "ok",   "duration_ms": 12, "details": {"handshake_age_sec": 47}},
    {"name": "awg_routing",   "status": "fail", "duration_ms": 5023, "details": {"error": "timeout"}},
    {"name": "awg_marker",    "status": "ok",   "duration_ms": 312, "details": {"http_code": 200}},
    {"name": "dns_doh",       "status": "ok",   "duration_ms": 89,  "details": {"failed_providers": []}}
  ]
}
```

### 4.4 Resilience при недоступности бэкенда
- Если POST не прошёл → буферим запись в `/opt/var/wg-monitor/buffer.jsonl`
- При восстановлении доставки — флашим буфер (батчем по 50 записей)
- Если `buffer.jsonl` превышает `buffer_max_bytes` → дропаем самые старые записи (rolling)

### 4.5 Long-poll для команд (Уровень 2 кнопок)
Параллельно основному циклу — отдельная горутина:
- `GET /v1/cmd?token=X` с holding до 30 сек
- При получении команды — выполняет (только whitelisted, см. 4.6) и POST'ит результат на `/v1/cmd/result`
- ack-token из payload команды передаётся обратно в результат для idempotency

### 4.6 Whitelist команд (handlers)

| cmd_type | Что делает | Безопасность |
|---|---|---|
| `diag_snapshot` | dump: `wg show all`, `ip route`, `ip rule`, `cat /proc/net/dev`, last 50 строк `logread \| grep -i wg` | read-only |
| `test_routing_now` | внеочередной полный цикл проверок, результат в команду + следом обычный POST | read-only |
| `restart_awg` | `wg-quick down <iface> && wg-quick up <iface>` (или эквивалент через AWG-менеджер). Lock 30 сек. | средняя — рвёт туннель |
| `preflight_upgrade` | см. раздел 7, фаза 1 | read-only |
| `upgrade` | см. раздел 7, фаза 3. Требует ack-token (single-use, 5 мин TTL). Lock 10 мин. | высокая |

Любая команда не из whitelist → ошибка `unknown_cmd_type`, лог.

## 5. Компонент: Backend

### 5.1 Стек
- Go + `net/http` + `mattn/go-sqlite3` + `go-telegram-bot-api/v5`
- systemd service на VPS Main (103.106.1.253), backend listens на `127.0.0.1:8080`
- **Публичный hostname:** `wgmonitor.jkaotlic.duckdns.org` — DuckDNS wildcard subdomain того же аккаунта, что обслуживает AdGuard на родительском `jkaotlic.duckdns.org`. Резолвится в `103.106.1.253` без отдельной регистрации (wildcard уже включён).
- **Reverse proxy:** Caddy (apt-package), конфиг:
  ```
  wgmonitor.jkaotlic.duckdns.org {
      reverse_proxy 127.0.0.1:8080
  }
  ```
  Caddy auto-https через **TLS-ALPN-01** challenge на :443 (не использует :80 — он занят AdGuard Web UI). Не конфликтует с certbot, обслуживающим parent-cert на `jkaotlic.duckdns.org` для AdGuard DoH/DoT (:8443 / :853).
- **Освобождение :443 уже сделано (2026-04-26):** прибили telemt-fallback (TSPU-режим, см. `feedback_tspu_dpi_telemt`) и mtg-Docker-контейнер. Бэкап в `/root/backup-pre-wgmonitor-2026-04-26/`.
- БД: SQLite `/var/lib/wg-monitor/state.db`
- Конфиг: `/etc/wg-monitor/backend.yaml` (TG bot token, admin chat_id, supergroup_id)

### 5.2 Схема БД

```sql
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  nickname TEXT UNIQUE NOT NULL,
  token_hash TEXT NOT NULL,          -- bcrypt от raw token
  expected_exit_ip TEXT NOT NULL,
  awg_iface TEXT NOT NULL,
  telegram_thread_id INTEGER,        -- nullable, заполняется при первом успешном createForumTopic
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  last_seen_at TIMESTAMP             -- updated на каждом /v1/report
);

CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  check_name TEXT NOT NULL,
  status TEXT NOT NULL,              -- ok | fail
  details_json TEXT,
  ts TIMESTAMP NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id)
);
CREATE INDEX idx_events_user_ts ON events(user_id, ts DESC);
-- Rolling: keep last 7 days, daily cron вычищает старое

CREATE TABLE incident_state (
  user_id INTEGER NOT NULL,
  check_name TEXT NOT NULL,
  consecutive_fails INTEGER NOT NULL DEFAULT 0,
  current_status TEXT NOT NULL,      -- ok | fail | hard
  hard_since TIMESTAMP,
  last_alert_msg_id INTEGER,
  silenced_until TIMESTAMP,
  acked_until TIMESTAMP,
  PRIMARY KEY (user_id, check_name)
);

CREATE TABLE pending_commands (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  cmd_type TEXT NOT NULL,
  args_json TEXT,
  ack_token TEXT,                    -- nullable, для destructive ops
  status TEXT NOT NULL,              -- pending | delivered | done | failed | timeout
  result_json TEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  delivered_at TIMESTAMP,
  finished_at TIMESTAMP
);

CREATE TABLE upgrade_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  started_at TIMESTAMP NOT NULL,
  finished_at TIMESTAMP,
  status TEXT NOT NULL,              -- running | success | partial_fail | timeout | aborted
  packages_json TEXT,
  log_excerpt TEXT,                  -- last 30 lines
  post_health_json TEXT
);

CREATE TABLE daily_soft_flaps (
  user_id INTEGER NOT NULL,
  check_name TEXT NOT NULL,
  flap_count INTEGER NOT NULL DEFAULT 0,
  date DATE NOT NULL,
  PRIMARY KEY (user_id, check_name, date)
);
```

### 5.3 State machine алертов

На каждое поступившее событие из `/v1/report`:

```
  current_status   |   incoming   |   action
─────────────────────────────────────────────────
  ok               |   ok         |   no-op
  ok               |   fail       |   consecutive_fails = 1, → fail
  fail             |   fail       |   consecutive_fails++
                   |              |   if consecutive_fails == 3 AND not silenced: send HARD alert,
                   |              |     status = hard, hard_since = now, save msg_id
  fail             |   ok         |   consecutive_fails = 0, → ok, increment daily_soft_flaps
  hard             |   fail       |   no-op (already alerted)
  hard             |   ok         |   if 2 OK подряд: send RECOVERY (reply to msg_id), → ok
```

Дополнительно:
- **Heartbeat watcher** (отдельная горутина, тик 30 сек): для каждого юзера `MAX(events.ts)` > 5 мин назад → "🔴 ROUTER OFFLINE" alert (отдельный pseudo-check `agent_heartbeat`)
- **Re-alert на залипший hard**: если `hard_since` старше 6 часов и не silenced — repeat-алерт раз в 6 часов (защита от того что я пропустил первое сообщение)
- Silenced/acked состояния пропускают отправку HARD, но recovery всё равно идёт

### 5.4 Endpoints

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/v1/report` | Bearer token | Приём событий от агента |
| GET | `/v1/cmd` | Bearer token | Long-poll выдача команд агенту |
| POST | `/v1/cmd/result` | Bearer token | Приём результата команды |
| GET | `/install.sh` | none | Bootstrap-скрипт для онбординга (отдаёт sh-скрипт) |
| GET | `/agent/<arch>/latest` | none | Бинарник для self-update |
| GET | `/agent/<arch>/latest.sha256` | none | Контрольная сумма |
| GET | `/healthz` | none | Liveness |

## 6. Telegram delivery

### 6.1 Топология чата
- **Супергруппа** с включёнными Topics
- Бот — админ супергруппы с правом `Manage Topics`
- Системные топики (создаются вручную при инициализации):
  - `General` (id=1) — приветствие, стартовое сообщение бэкенда
  - `📊 Сводка` (id ~2) — дэйли-сводки и weekly upgrade summary
  - `🔧 Системное` (id ~3) — алерты бэкенда о себе (старт, ошибки, новые юзеры)
- Пер-юзерные топики — создаются автоматически при онбординге через `createForumTopic`, id сохраняется в `users.telegram_thread_id`

### 6.2 Inline-кнопки

**Под HARD-алертом** (всегда):

```
🔴 [Вася] AWG handshake — DOWN
   Last OK: 5 минут назад
   Fails:   3 подряд
   Hard since: 2026-04-25 20:03:00 МСК

[⏸ 1ч] [⏸ 4ч] [⏸ 24ч] [✅ Ack]
[📋 История 24ч] [🔇 Mute до утра]
[🔍 Diag] [🌐 Test now] [🔄 Restart AWG]
```

**В weekly upgrade summary (📊 Сводка, воскресенье 12:00 МСК)**:

```
📦 Доступные обновления (флот):
  Вася    — 12 пакетов, ~25MB,  free 67MB    ✅ safe
  Петя    — 3 пакета,   ~4MB,   free 89MB    ✅ safe
  Алёша   — 0 пакетов                         — up-to-date
  Гена    — 8 пакетов,  ~18MB,  free 11MB    ⚠️ tight
  Дима    — 15 пакетов, ~30MB,  free 8MB     🔴 abort

[📦 Upgrade Вася] [📦 Upgrade Петя] [📦 Upgrade Гена ⚠️]
```

### 6.3 Callback обработка
- Backend держит long-poll `getUpdates` на TG bot
- Все callbacks проверяются: `from.id == admin_chat_id` (никто кроме меня нажать не должен, даже если бот в группе с другими)
- Callback data — короткий формат `<action>:<user_id>:<check_name>:<ack_token?>` (TG ограничен 64 байта)
- Edit-message для подтверждения действия (например, `[⏸ 1ч] → ⏸ Silenced до 21:03`)

### 6.4 Защита от устаревших кнопок
Кнопки в истории остаются кликабельными вечно. Поэтому:
- Все destructive callbacks несут `ack_token` с TTL 5 мин в `pending_commands`
- Просроченный или использованный ack → callback отвечает «expired», message edit'ится с пометкой
- Silence/ack не требуют ack_token — они идемпотентны и не разрушительны

## 7. Upgrade pipeline

### 7.1 Триггеры
- **Weekly cron** (sunday 12:00 МСК): backend очередью раскладывает `preflight_upgrade` каждому юзеру, собирает результаты, постит summary в "📊 Сводка"
- **По кнопке** в summary: `📦 Upgrade <имя>` → переход к фазе 2 (confirm) для одного юзера

### 7.2 Фазы

#### Фаза 1 — Pre-flight (агент)
1. `df /opt` → free MB
2. `opkg update` → освежить индекс
3. Парсит `opkg list-upgradable` + sizes из `/opt/var/opkg-lists/`
4. `required_mb = sum(sizes) * 2 + 5`
5. `verdict`:
   - `safe` если `free > required * 1.5`
   - `tight` если `required < free <= required * 1.5`
   - `abort` если `free <= required`
6. Возвращает JSON с packages, sizes, free, required, verdict

#### Фаза 2 — Confirm prompt (бэкенд)
Бэкенд получает результат, постит в топик юзера сообщение с детальным списком и кнопками `[✅ Запустить upgrade] [❌ Отмена]`. Кнопка несёт свежий `ack_token` (TTL 5 мин, single-use).

Для `verdict=abort` кнопка не показывается — только рекомендация чистки.

#### Фаза 3 — Apply (агент)
1. Создаёт lock `/opt/var/wg-monitor/upgrade.lock` (PID + ts). Если lock fresh (<10 мин) → отказ.
2. Snapshot `opkg list-installed > /opt/var/wg-monitor/pre-upgrade.list`
3. Запускает `opkg upgrade 2>&1 | tee /opt/var/wg-monitor/upgrade.log`
4. Каждые 10 сек — POST'ит на `/v1/cmd/result` partial с last 3 строками лога
5. Бэкенд `editMessageText` обновляет TG-сообщение (throttle 3 сек, чтобы не упереться в rate-limit)
6. Timeout 8 минут → kill процесса, статус `timeout`
7. Снимает lock, шлёт final result

#### Фаза 4 — Post-health check (агент)
Сразу после `opkg upgrade` агент внеочередно прогоняет основные проверки и шлёт результат отдельной командой `post_upgrade_health`. Бэкенд формирует финальное сообщение:

```
✅ Upgrade Вася завершён за 1m 47s
   awg_handshake:  ✓ OK (handshake 2s ago)
   awg_routing:    ✓ OK (exit 89.125.101.122)
   dns_doh:        ✓ OK (3/3 providers)
Updated: 12 packages
```

или при провале:

```
🚨 Upgrade Вася завершён, post-health FAILED:
   awg_handshake:  ✗ FAIL (no handshake)
   awg_routing:    ✗ FAIL (curl timeout)
   dns_doh:        ✓ OK
Last 5 lines:
  Configuring hr-neo.
  postinst: ipset rule failed: Kernel error received: ...
Recommended: ssh root@vasya-keenetic
```

## 8. Дэйли-сводка

В 09:00 МСК backend постит в "📊 Сводка":

```
📊 Сводка за 24-04-2026

Вася:    uptime 99.4%
  • AWG handshake — 4 флапа (max consec: 2)
  • DNS DoH — 1 флап
Петя:    uptime 100%   — без замечаний
Гена:    🔴 OFFLINE с 03:14 МСК (6h 12m)
  Last alert: <link to msg>
...

Total HARD incidents: 2
Total auto-recovered: 7
```

## 9. Self-update агентов

Тот же паттерн, что у `telemt`:
- Раз в сутки (jitter ±2 часа) агент GET `/agent/<arch>/latest.sha256`
- Если sha отличается от текущего — GET `/agent/<arch>/latest` → атомарная замена через `mv` → restart себя через init.d
- Rollback: если новая версия крашится 3 раза за 5 минут после старта — откат на `.previous` бинарник
- Версия передаётся в каждом `/v1/report` (поле `agent_version`), бэкенд может видеть, кто на какой версии

## 10. Онбординг

CLI `wg-monitor-cli` (отдельный бинарник, запускается с твоего ПК или прямо на VPS Main):

```bash
wg-monitor-cli add-user \
  --nickname=vasya \
  --awg-iface=awg0 \
  --expected-exit-ip=89.125.101.122
```

Действия:
1. Генерит токен (32 байта random hex)
2. Создаёт запись в `users` (token хранится как bcrypt hash)
3. Через TG Bot API создаёт топик `👤 vasya` в супергруппе, сохраняет `thread_id`
4. Постит в `🔧 Системное`: `New user: vasya, install command:`
5. Печатает install one-liner:

```bash
ssh root@vasya-keenetic 'curl -fsSL https://monitor.your.tld/install.sh | TOKEN=<raw_token> NICKNAME=vasya sh'
```

`install.sh` на роутере:
1. Определяет архитектуру (`uname -m` → mipsel/aarch64)
2. Качает `https://monitor.your.tld/agent/<arch>/latest` + проверяет sha256
3. Кладёт в `/opt/bin/wg-monitor`, ставит +x
4. Генерит конфиг `/opt/etc/wg-monitor/config.yaml` (token, nickname, дефолты для проверок)
5. Регает в `/opt/etc/init.d/S99wg-monitor`
6. Стартует
7. Делает первый POST на `/v1/report` для проверки связности; если 200 — успех

Прочие CLI-команды (минимально):
- `wg-monitor-cli list-users`
- `wg-monitor-cli rotate-token --nickname=vasya`
- `wg-monitor-cli remove-user --nickname=vasya`

## 11. Безопасность

| Угроза | Защита |
|---|---|
| Чужой бьёт `/v1/report` от имени юзера | Bearer token проверяется bcrypt-hash'ем |
| Бот в супергруппе кто-то добавит, чужой нажмёт кнопку | callback handler проверяет `from.id == admin_chat_id` |
| Старая кнопка из истории нажата случайно | ack_token TTL 5 мин, single-use |
| Concurrent upgrade на одном роутере | lock-файл (PID + ts), reject if fresh, force-override if stale |
| Concurrent restart_awg | lock 30 сек, max 1/мин |
| Произвольное shell исполнение через cmd | Whitelist `cmd_type`, никакого raw shell в payload |
| MITM между агентом и бэкендом | HTTPS-only, Caddy с LE-сертификатом |
| Token утечёт через лог | Логируем только `token_id` (первые 8 символов), не сам токен |
| Бэкенд скомпрометирован | Все деструктивные cmd дополнительно требуют ack_token, TTL 5 мин — атакующему придётся ждать триггера |

### Lock-файлы (свод)

| Операция | Lock file | Timeout | Защита |
|---|---|---|---|
| `upgrade` | `/opt/var/wg-monitor/upgrade.lock` | 8 мин | reject if fresh, force-override if stale |
| `restart_awg` | `/opt/var/wg-monitor/awg-restart.lock` | 30 сек | sequential, max 1/мин |
| `report` (буфер) | `/opt/var/wg-monitor/buffer.lock` | 5 сек | per-write flock |

## 12. YAGNI (явно НЕ делаем)

- Web-дашборд — TG топики достаточно
- Prometheus / Grafana — пока 10 юзеров, не нужно
- Алерты юзерам — ты единственный получатель
- Slash-команды (`/status`, `/diag` ...) — кнопок хватает
- Кнопка `🔥 Reboot router` — обходимся `restart_awg`, добавим если упрёмся
- `nfqws2-keenetic` спецпроверки — обновляется через `opkg upgrade` вместе со всем
- Шифрование БД, RBAC, multi-admin — single-admin система
- Локальный SQLite на агенте — JSONL-буфера хватает
- HA / failover бэкенда — single instance, downtime бэкенда = нет алертов, переживём

## 13. Объём работы

| Компонент | Строк (грубо) |
|---|---|
| Agent (Go) — основной цикл, проверки, буфер | 700–900 |
| Agent (Go) — long-poll cmd channel + handlers (включая upgrade pipeline) | 400–500 |
| Backend (Go) — endpoints, state machine, БД | 1000–1300 |
| Backend (Go) — TG bot, callbacks, edit-message | 600–800 |
| Backend (Go) — weekly cron, daily summary | 200–300 |
| CLI (Go) — add-user, rotate-token, list, remove | 200–300 |
| install.sh + init.d скрипт | 100–150 |
| Makefile (build matrix, upx, sha256) | 50–80 |
| Тесты (Go testing, минимум state machine + endpoints) | 600–900 |

**Реалистично:** 3–4 вечерних сессии (~12–16 часов) до рабочего MVP с 1 подключённым роутером. Накатывание остальных 9 — час с человеком на каждого.

## 14. Этапы поставки

1. **Этап 0 — bootstrapping** (1 сессия)
   - Repo + Makefile + cross-compile (mipsel, aarch64)
   - Skeleton agent (только heartbeat) + skeleton backend (только `/v1/report`, лог в stdout)
   - Verify: бинарник 2-3 MB, успешный POST с роутера на backend

2. **Этап 1 — checks + state machine + TG basic** (1–1.5 сессии)
   - 4 проверки в агенте
   - SQLite + state machine + heartbeat watcher
   - TG bot, топики, HARD/RECOVERY алерты (без кнопок)
   - install.sh + CLI add-user
   - Verify: подключить себя, спровоцировать AWG fail (down iface), увидеть алерт + recovery

3. **Этап 2 — Уровень 1 кнопок** (0.5 сессии)
   - silence / ack / history / mute (всё в бэкенде)
   - Verify: кнопки работают, силенс глушит

4. **Этап 3 — command channel + Уровень 2 кнопок** (1 сессия)
   - Long-poll endpoint, ack_tokens, lock-файлы
   - `diag_snapshot`, `test_routing_now`, `restart_awg`
   - Verify: рестартим AWG из TG, смотрим diag

5. **Этап 4 — upgrade pipeline + weekly summary** (1 сессия)
   - `preflight_upgrade`, `upgrade` + post-health
   - Weekly cron + summary с кнопками
   - Verify: реальный `opkg upgrade` на тестовом роутере, edit-progress, post-health алерт

6. **Этап 5 — self-update агентов + раскатка на флот** (0.5 сессии)
   - Self-update пайплайн (как у telemt)
   - Раскатка на 9 оставшихся роутеров с человеком
   - Verify: новый агент выкатился сам всем

## 15. Открытые вопросы — все resolved 2026-04-26

См. раздел «Resolved questions» ниже.

## Resolved questions

### 2026-04-26: Q1 — Subdomain backend

Выбран `wgmonitor.jkaotlic.duckdns.org` (DuckDNS wildcard, парент `jkaotlic.duckdns.org` обслуживает AdGuard Home через certbot-cert). Wildcard был уже включён в DuckDNS-панели — резолвится в `103.106.1.253` без отдельной регистрации.

В рамках подготовки на VPS Main освобождён :443: удалены `telemt.service`, `telemt-update.service`, `telemt-update.timer`, `/usr/local/bin/telemt*`, `/etc/telemt/`, `/var/lib/telemt/`, Docker-контейнер `mtg` и `/etc/mtg/`. Полный бэкап в `/root/backup-pre-wgmonitor-2026-04-26/` (42 MB). `mtproto.zig` на :8444 не задет — он сейчас основной TG-прокси.

URL-схемы для агентов:
- `POST https://wgmonitor.jkaotlic.duckdns.org/v1/report`
- `GET  https://wgmonitor.jkaotlic.duckdns.org/v1/cmd?token=X`
- `POST https://wgmonitor.jkaotlic.duckdns.org/v1/cmd/result`
- `GET  https://wgmonitor.jkaotlic.duckdns.org/install.sh`
- `GET  https://wgmonitor.jkaotlic.duckdns.org/agent/<arch>/latest{,.sha256}`
- `GET  https://wgmonitor.jkaotlic.duckdns.org/healthz`

### 2026-04-26: Q2 — Telegram supergroup

Используется существующая частная супергруппа пользователя. Конкретные id'ники, ссылки и токены — вне репозитория, в `local-values.yaml` (gitignored) на разработческой машине и в `/etc/wg-monitor/backend.yaml.local` + `/root/wgmon-secrets/` на VPS Main.

| Параметр | Где хранится | В репо? |
|---|---|---|
| Bot HTTP API token | `/root/wgmon-secrets/bot-token.txt` (chmod 600 owner root) | ❌ |
| Bot user id | local-values.yaml | ❌ |
| Bot username | local-values.yaml | ❌ |
| Group chat_id | local-values.yaml | ❌ |
| Group invite link | вообще не записываем | ❌ |
| Admin user id (callback allowlist) | local-values.yaml | ❌ |

Acceptance check выполнен 2026-04-26 через `getMe`/`getChat`/`getChatAdministrators`/`getChatMember` — все 4 запроса вернули ok=true: бот — administrator, `can_manage_topics: true`, `is_forum: true`, admin user is `creator` of the group.

**Backend конфиг (`/etc/wg-monitor/backend.yaml`)** должен ссылаться на токен через `bot_token_file: /root/wgmon-secrets/bot-token.txt`, а не хранить значение inline. Аналогично `chat_id` и `admin_user_id` берутся из `backend.yaml`, который deploy-only и не попадает в git.

### 2026-04-26: Q3 — Nicknames юзеров

Политика **A — реальные имена/прозвища**.

Регексп валидации: `^[a-z][a-z0-9_-]{1,15}$`
- ASCII lowercase, 2–16 символов, начинается с буквы
- Кириллицу → транслит (`vasya`, `papa`, `kolya`, `serg`, …)
- Не содержит spaces/dots/emoji — прозрачно для bash, systemd, filesystem, lograg

CLI `wg-monitor-cli add-user --nickname=<X>` валидирует регексп, отвергает с понятной ошибкой при несоответствии.

Список юзеров — заполняется по одному через CLI на этапе раскатки (Этап 5), не нужен в самом MVP.

### 2026-04-26: Q4 — Per-user routing (`expected_exit_ip` + `awg_iface`)

Топология **гетерогенная**: каждый из ~10 юзеров ходит через **свой** WG/AWG-выход (не через мою VPS Amnezia, не через VPS Main). Поэтому:

- `expected_exit_ip` — **жёстко per-user**, без глобального дефолта.
- `awg_iface` — **жёстко per-user**, без глобального дефолта (могут быть разные имена интерфейсов: `awg0`, `wg0`, `awg-amn`, etc.).
- Оба поля хранятся в `users` таблице (это уже было в схеме раздела 5.2).
- CLI `wg-monitor-cli add-user` делает оба аргумента **обязательными** без дефолта:
  ```
  wg-monitor-cli add-user \
      --nickname=<X> \
      --awg-iface=<X> \
      --expected-exit-ip=<X>
  ```

Динамическая логика «exit IP должен быть из списка X» **не требуется** — каждый юзер привязан к одному ожидаемому IP. Если когда-то у кого-то появится несколько exit-ов (multi-WG-конфиг) — отдельная фича после MVP.

### 2026-04-26: Q5 — Имя проекта

Остаётся **`wg-monitor`**. Все артефакты:
- repo: `wg-monitor`
- бинари: `wg-monitor` (agent), `wg-monitor-cli` (CLI), `wg-monitor-backend` (server)
- конфиг агента: `/opt/etc/wg-monitor/config.yaml`
- конфиг бэкенда: `/etc/wg-monitor/backend.yaml`
- init.d на роутерах: `/opt/etc/init.d/S99wg-monitor`
- systemd на VPS Main: `wg-monitor-backend.service`
- subdomain: `wgmonitor.jkaotlic.duckdns.org` (без дефиса — DuckDNS-style)

---

## Spec self-review

- ✅ Placeholder scan: `vasya/петя/etc` — намеренные плейсхолдеры имён, не TBD; реальные значения подставит пользователь при онбординге. `your.tld` снят 2026-04-26 → `wgmonitor.jkaotlic.duckdns.org`. Никаких `TBD` / `TODO` в обязательных решениях.
- ✅ Internal consistency: command channel описан в 4.5/4.6, использован в 6.2 (кнопки) и 7.3 (upgrade) — consistent. State machine из 5.3 совместим с silence/ack из 6.2.
- ✅ Scope: один MVP, поделён на 6 этапов с явным verify в каждом — годится для одного implementation plan.
- ✅ Ambiguity: пороги (3 fails, 6h re-alert, 5 min ack TTL) — конкретные числа, не «достаточно». DNS fail criterion явно: 2 из 3 провайдеров.

Open questions в разделе 15 — это легитимные user-input уточнения, не пробелы в дизайне.
