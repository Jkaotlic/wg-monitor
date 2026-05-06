# Деплой wg-monitor

> Инструкция для самостоятельного развёртывания. Нужны: VPS с Linux, Keenetic с Entware и awg-manager, Telegram-бот.

---

## Содержание

- [Что нужно заранее](#что-нужно-заранее)
- [1. Настройка Telegram-бота](#1-настройка-telegram-бота)
- [2. Деплой бэкенда на VPS](#2-деплой-бэкенда-на-vps)
- [3. Деплой агента на роутер](#3-деплой-агента-на-роутер)
- [4. Проверка работы](#4-проверка-работы)
- [Обновление бэкенда](#обновление-бэкенда)
- [Обновление агента](#обновление-агента)

---

## Что нужно заранее

| Компонент | Требования |
| --- | --- |
| **VPS** | Linux amd64, минимум 256 МБ RAM, открытый порт 443, systemd |
| **Роутер** | Keenetic OS 4/5, Entware установлен, [awg-manager](https://github.com/hoaxisr/awg-manager) 2.8+ |
| **Telegram** | Аккаунт, созданный бот через [@BotFather](https://t.me/BotFather) |
| **Сборочная машина** | Go 1.22+, Python 3.10+ (`pip install paramiko`) |
| **DNS** | Домен или DuckDNS, указывающий на VPS |

---

## 1. Настройка Telegram-бота

### 1.1 Создать бота

```
/newbot → получить токен вида 1234567890:AAF...
```

### 1.2 Создать супергруппу с топиками

1. Создать новую группу в Telegram
2. Зайти в **Настройки группы → Темы** → включить **Topics** (форум-режим)
3. Добавить бота в группу и выдать ему права **администратора** (нужно для отправки сообщений в топики)
4. Для каждого роутера создать отдельный топик (тему), например `testkeen`

### 1.3 Узнать Chat ID и Thread ID

Отправить любое сообщение в топик роутера, потом выполнить:

```bash
curl "https://api.telegram.org/bot<TOKEN>/getUpdates" | python3 -m json.tool | grep -E "chat|message_thread_id"
```

Нужны:
- `chat.id` — отрицательное число, например `-1001234567890`
- `message_thread_id` — ID топика конкретного роутера

### 1.4 Узнать свой User ID (для AdminUserID)

Написать [@userinfobot](https://t.me/userinfobot) — он ответит вашим ID.

---

## 2. Деплой бэкенда на VPS

### 2.1 Собрать бинарник

```bash
# на сборочной машине
GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.Version=$(git describe --tags --always)" \
  -o dist/wg-monitor-backend \
  ./cmd/backend/
```

### 2.2 Создать пользователя и директории на VPS

```bash
ssh root@<VPS_IP>

useradd --system --no-create-home --shell /usr/sbin/nologin wgmonitor
mkdir -p /etc/wg-monitor /var/lib/wg-monitor
chown wgmonitor:wgmonitor /var/lib/wg-monitor
```

### 2.3 Написать конфиг `/etc/wg-monitor/backend.yaml`

```yaml
listen: 127.0.0.1:8080
log_level: info
db_path: /var/lib/wg-monitor/state.db

telegram:
  bot_token: "1234567890:AAFxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
  chat_id: -1001234567890        # ID супергруппы
  admin_user_id: 123456789       # ваш Telegram User ID

agents:
  - nickname: testkeen
    # генерация: openssl rand -hex 32
    token: "deadbeefcafebabedeadbeefcafebabe0123456789abcdef0123456789abcdef"
    thread_id: 42                # message_thread_id топика этого роутера

state:
  fail_threshold: 2
  recovery_threshold: 2
  mute_cutoff_hour: 23           # не слать алерты после 23:00
  realert_every_sec: 3600
  realert_tick_sec: 60

heartbeat:
  stale_after_sec: 120
  stale_after_static_sec: 180
  stale_after_mobile_sec: 300
  resume_grace_sec: 30
  scan_interval_sec: 30
```

```bash
chmod 600 /etc/wg-monitor/backend.yaml
```

### 2.4 Установить systemd-юнит

```bash
# скопировать из репозитория
cp deploy/backend/wg-monitor-backend.service /etc/systemd/system/

systemctl daemon-reload
systemctl enable wg-monitor-backend
```

### 2.5 Загрузить бинарник и запустить

```bash
# с локальной машины
scp dist/wg-monitor-backend root@<VPS_IP>:/usr/local/bin/
ssh root@<VPS_IP> "chmod 755 /usr/local/bin/wg-monitor-backend && systemctl start wg-monitor-backend"
```

Или автоматически через скрипт:

```bash
VPS_MAIN_PASS=<пароль> python deploy/backend/deploy_vps_main.py \
  --binary dist/wg-monitor-backend
```

### 2.6 Настроить Caddy (reverse proxy + TLS)

```bash
# установить Caddy: https://caddyserver.com/docs/install
apt install -y debian-keyring debian-archive-keyring apt-transport-https
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main" > /etc/apt/sources.list.d/caddy-stable.list
apt update && apt install caddy
```

Содержимое `/etc/caddy/Caddyfile` (пример из `deploy/backend/Caddyfile`):

```
your-domain.duckdns.org {
    reverse_proxy 127.0.0.1:8080 {
        header_up Host {host}
        header_up X-Real-IP {remote_host}
    }
    request_body {
        max_size 1MB
    }
}
```

```bash
systemctl enable --now caddy
```

### 2.7 Проверить

```bash
systemctl status wg-monitor-backend
journalctl -u wg-monitor-backend -f
curl -s https://your-domain/health
```

---

## 3. Деплой агента на роутер

### 3.1 Убедиться что awg-manager работает

```bash
ssh root@192.168.0.1 -p 222
curl -s http://127.0.0.1:2222/api/system/info -H "X-Requested-With: XMLHttpRequest" | grep version
```

### 3.2 Собрать бинарник для ARM64

```bash
# большинство современных Keenetic (Giga, Ultra, Peak, Air, Hero 4G и др.)
GOOS=linux GOARCH=arm64 go build \
  -ldflags "-X main.Version=$(git describe --tags --always)" \
  -o dist/wg-monitor-agent \
  ./cmd/agent/

# для старых роутеров с MIPS (Knight, Start 2)
GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build \
  -ldflags "-X main.Version=$(git describe --tags --always)" \
  -o dist/wg-monitor-agent-mips \
  ./cmd/agent/
```

> **PowerShell (Windows):**
> ```powershell
> $env:GOOS="linux"; $env:GOARCH="arm64"
> go build -ldflags "-X main.Version=0.8.0" -o dist/wg-monitor-agent ./cmd/agent/
> $env:GOOS=""; $env:GOARCH=""
> ```

### 3.3 Написать конфиг агента

Скопировать `deploy/agent/config.yaml.example` и заполнить:

```yaml
backend:
  url: https://your-domain.duckdns.org
  token: "deadbeefcafebabedeadbeefcafebabe0123456789abcdef0123456789abcdef"

agent:
  nickname: testkeen           # уникальный ID роутера (совпадает с backend.yaml)
  interval_sec: 60

awg_manager:
  url: http://127.0.0.1:2222   # awg-manager API, менять не нужно

checks:
  awg:
    handshake_max_age_sec: 180  # алерт если handshake старше 3 минут

  dns:
    auto_discover: true          # брать резолверы из running-config Keenetic
    test_domain: "example.com"
    fail_threshold: 2

  # Опционально: проверка внешней доступности через туннель
  # external_reach:
  #   enabled: true
  #   bind_to_default: true
  #   targets:
  #     - name: cloudflare
  #       url: https://1.1.1.1

state:
  path: /opt/var/wg-monitor/state.json
```

### 3.4 Первичный деплой (бинарник + конфиг + init.d)

```bash
python deploy/agent/deploy_keenetic.py \
  --host 192.168.0.1 \
  --port 222 \
  --password <пароль_root> \
  --bin dist/wg-monitor-agent \
  --config deploy/agent/configs/myrouter.yaml
```

Скрипт:
1. Останавливает старый агент
2. Создаёт `/opt/bin/`, `/opt/etc/wg-monitor/`, `/opt/var/wg-monitor/`
3. Загружает бинарник через stdin-pipe (обходит ограничения dropbear)
4. Проверяет sha256
5. Записывает конфиг в `/opt/etc/wg-monitor/config.yaml`
6. Устанавливает `/opt/etc/init.d/S99wg-monitor`
7. Запускает агент

### 3.5 Проверить запуск

```bash
ssh root@192.168.0.1 -p 222
ps | grep wg-monitor
# ожидаемо: 1234 root S wg-monitor -config /opt/etc/wg-monitor/config.yaml
```

Через ~60 секунд в Telegram-топике роутера должен появиться первый отчёт.

---

## 4. Проверка работы

После запуска обоих компонентов:

- Через 1 минуту бот пишет первый отчёт в топик роутера
- Все туннели `ok`, DNS `ok`, heartbeat `ok`
- Если что-то `fail` — смотреть логи бэкенда: `journalctl -u wg-monitor-backend -n 50`

**Тест tunnel import:**  
Отправить `.conf` файл AmneziaWG в топик роутера → бот предложит «Заменить» или «Добавить» → после подтверждения туннель появится в awg-manager и запустится автоматически.

---

## Обновление бэкенда

```bash
# пересобрать
GOOS=linux GOARCH=amd64 go build -o dist/wg-monitor-backend ./cmd/backend/

# задеплоить
VPS_MAIN_PASS=<пароль> python deploy/backend/deploy_vps_main.py \
  --binary dist/wg-monitor-backend
```

Скрипт атомарно меняет бинарник: stop → upload → sha256 → swap → start → проверка journal.

---

## Обновление агента

```bash
# пересобрать
GOOS=linux GOARCH=arm64 go build -o dist/wg-monitor-agent ./cmd/agent/

# задеплоить (только бинарник, конфиг не трогает)
python deploy/agent/deploy_keenetic_binonly.py \
  --bin dist/wg-monitor-agent
```

---

## Добавление второго роутера

1. В `backend.yaml` добавить в `agents:` новый никнейм + токен + thread_id
2. Перезапустить бэкенд
3. Создать конфиг для нового роутера (скопировать, поменять nickname и token)
4. Запустить `deploy_keenetic.py` с новым `--host` и `--config`
