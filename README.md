# wg-monitor

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![AmneziaWG](https://img.shields.io/badge/AmneziaWG-2.8+-blue?logo=wireguard&logoColor=white)](https://github.com/amnezia-vpn/amneziawg-go)
[![Keenetic](https://img.shields.io/badge/Keenetic-OS5-green)](https://keenetic.com)
[![Telegram Bot](https://img.shields.io/badge/Telegram-Bot-26A5E4?logo=telegram&logoColor=white)](https://core.telegram.org/bots)
[![Self-hosted](https://img.shields.io/badge/self--hosted-VPS-orange)](https://github.com/Jkaotlic/wg-monitor)

Система мониторинга и удалённого управления флотом **AmneziaWG** на роутерах **Keenetic** через Telegram.

Лёгкий Go-агент живёт на каждом роутере и каждую минуту отправляет отчёт на VPS. Бэкенд превращает эти данные в Telegram-уведомления, алерты и позволяет управлять роутерами прямо из чата — без SSH, без web-интерфейса.

> Self-hosted monitoring and remote-ops system for an **AmneziaWG** fleet on **Keenetic** routers.
> A lightweight Go agent on each router pushes per-minute health reports to a VPS backend, which drives a Telegram bot — alerts, per-router topic threads, and remote operations without SSH.

---

## Возможности / Features

### 📊 Мониторинг / Monitoring

| Проверка | Описание |
| --- | --- |
| **AWG туннели** | Статус каждого туннеля, время последнего handshake, аномалии трафика |
| **DNS** | Работоспособность plain/DoT/DoH резолверов через нужный сетевой интерфейс |
| **HydraRoute** | Состояние службы обхода блокировок |
| **Внешняя доступность** | HTTP-проверки внешних ресурсов через туннель с `defaultRoute=true` |
| **RKN-пробы** | Проверка доступности заблокированных доменов по команде из Telegram |
| **Heartbeat** | Бэкенд детектирует молчащий агент и шлёт алерт «роутер недоступен» |

### 🤖 Telegram-операции / Telegram Ops

| Операция | Как работает |
| --- | --- |
| **Алерты** | Пороговые fail/recovery уведомления, расписание тишины, повторные алерты |
| **Панель туннелей** | Inline-кнопки включения/выключения каждого туннеля через Keenetic NDMC |
| **Импорт конфига** | Отправь `.conf` файл в топик роутера → бот добавит или заменит туннель в awg-manager |
| **opkg обновления** | `opkg update` → проверка места → `opkg upgrade` с прогрессом в чате |
| **Force recheck** | Кнопка 🔁 — немедленный отчёт без ожидания следующей минуты |

### 📥 Импорт туннелей через Telegram

Самая удобная часть: не нужно заходить на роутер, не нужен web-интерфейс awg-manager.

1. Экспортируй `.conf` из AmneziaWG-клиента
2. Перешли файл в **топик нужного роутера** в Telegram
3. Бот предложит: **🔄 Заменить** существующий туннель с таким именем или **➕ Добавить** новый
4. После подтверждения агент вызывает awg-manager API (`POST /api/import/conf`), туннель создаётся с тем же бэкендом что и остальные (`nativewg` / `kernel`) и сразу запускается
5. Если установлен HydraRoute — перезапускается автоматически

Работает с форматами **WireGuard** и **AmneziaWG** (включая поля `Jc`, `Jmin`, `Jmax`, `H1–H4`, `S1–S4` в форматах как одиночных значений, так и диапазонов).

---

## Архитектура / Architecture

```text
┌──────────────────────────┐       HTTPS/JSON        ┌──────────────────────────┐
│   Keenetic router         │ ── reports + cmds ────► │   VPS (Go backend)        │
│                           │                          │                           │
│  wg-monitor agent (Go)   │                          │  ┌───────────────────┐   │
│  arm64 / mipsel           │ ◄── long-poll cmds ───  │  │  SQLite state DB  │   │
│                           │                          │  └───────────────────┘   │
│  Checks every 60s:        │                          │  ┌───────────────────┐   │
│  · AWG handshake ages     │                          │  │  Telegram Bot     │   │
│  · DNS plain/DoT/DoH      │                          │  │  alerts + ops     │   │
│  · HydraRoute status      │                          │  └───────────────────┘   │
│  · External reach probes  │                          │                           │
│  · RKN domain probes      │                          │  Behind Caddy TLS         │
└──────────────────────────┘                          └──────────────────────────┘
          │
          │ awg-manager REST API (127.0.0.1:2222)
          ▼
   ┌─────────────┐
   │ awg-manager │  hoaxisr/awg-manager 2.8+
   └─────────────┘
```

---

## Сборка / Build

```bash
make build-host        # текущая ОС — для тестов и разработки
make build-mipsel      # Keenetic MIPS little-endian softfloat
make build-aarch64     # Keenetic ARM64 (большинство современных роутеров)
make pack              # UPX --best на cross-compiled бинарниках
```

## Компоненты / Components

| Компонент | Путь | Цель |
| --- | --- | --- |
| Agent | `cmd/agent/` | arm64 / mipsel (Keenetic + Entware) |
| Backend | `cmd/backend/` | amd64 (VPS, за Caddy) |
| CLI | `cmd/wg-monitor-cli/` | хост — ручные операции |
| Протокол | `pkg/wire/` | оба бинарника |

## Требования / Requirements

- **Роутер:** Keenetic OS 4/5 c Entware, [awg-manager](https://github.com/hoaxisr/awg-manager) 2.8+
- **VPS:** любой Linux amd64, Caddy или nginx для TLS
- **Telegram:** токен бота + супергруппа с топиками на каждый роутер (режим forum)

## Деплой / Deployment

Подробная пошаговая инструкция: **[DEPLOY.md](DEPLOY.md)**

Агент: `/opt/etc/wg-monitor/config.yaml`  
Бэкенд: `/etc/wg-monitor/backend.yaml`  
Примеры конфигов: `deploy/agent/config.yaml.example`, `deploy/backend/backend.yaml.example`
