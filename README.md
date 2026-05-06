# wg-monitor

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![AmneziaWG](https://img.shields.io/badge/AmneziaWG-2.8+-blue?logo=wireguard&logoColor=white)](https://github.com/amnezia-vpn/amneziawg-go)
[![Keenetic](https://img.shields.io/badge/Keenetic-OS5-green)](https://keenetic.com)
[![Telegram Bot](https://img.shields.io/badge/Telegram-Bot-26A5E4?logo=telegram&logoColor=white)](https://core.telegram.org/bots)
[![Self-hosted](https://img.shields.io/badge/self--hosted-VPS-orange)](https://github.com/Jkaotlic/wg-monitor)

Self-hosted monitoring and ops bot for an **AmneziaWG** fleet on **Keenetic** routers.  
A lightweight Go agent lives on each router and pushes health reports to a Go backend on your VPS. The backend drives a **Telegram bot** — alerts, per-router topic threads, and remote operations.

---

## Features

| Feature | Details |
| --- | --- |
| **Health monitoring** | Per-minute reports: AWG tunnel state, last-handshake age, DNS (plain/DoT/DoH), HydraRoute, external-reach probes |
| **RKN probes** | On-demand connectivity checks against blocked-domain lists, triggered from Telegram |
| **Telegram alerts** | Threshold-based fail/recovery alerts with mute schedules and re-alert cadence |
| **Tunnels panel** | Per-tunnel enable/disable toggle via Keenetic NDMC, shown in a Telegram inline keyboard |
| **Tunnel import** | Send a `.conf` file to the router's topic → bot imports it via awg-manager API (create or replace), auto-starts it |
| **opkg upgrades** | Smart upgrade flow: `opkg update` → space-check → `opkg upgrade`, lock-file guarded, progress in Telegram |
| **Force recheck** | Tap 🔁 in Telegram to trigger an immediate report with `resumed=true` |
| **Heartbeat watchdog** | Backend detects stale agents and sends a "router offline" alert |

## Architecture

```
┌─────────────────────────┐        HTTPS/JSON        ┌─────────────────────────┐
│   Keenetic router        │ ──── reports + cmds ───► │   VPS (Go backend)       │
│                          │                           │                          │
│  wg-monitor agent (Go)  │                           │  ┌──────────────────┐   │
│  arm64 / mipsel          │                           │  │  SQLite state DB │   │
│                          │                           │  └──────────────────┘   │
│  Checks every 60s:       │ ◄──── long-poll cmds ─── │  ┌──────────────────┐   │
│  • AWG tunnel handshakes │                           │  │  Telegram Bot    │   │
│  • DNS (plain/DoT/DoH)  │                           │  │  (alerts, ops)   │   │
│  • HydraRoute status     │                           │  └──────────────────┘   │
│  • External reach        │                           │                          │
│  • RKN domain probes     │                           │  Behind Caddy TLS        │
└─────────────────────────┘                           └─────────────────────────┘
         ▲
         │ awg-manager API (127.0.0.1:2222)
         │
   ┌─────────────┐
   │ awg-manager │  (hoaxisr/awg-manager 2.8+)
   └─────────────┘
```

## Build

```bash
make build-host        # local OS — for tests and dev
make build-mipsel      # Keenetic with MIPS little-endian softfloat
make build-aarch64     # Keenetic with ARM64 (most modern Keenetics)
make pack              # UPX --best on cross-compiled binaries
```

## Components

| Component | Path | Target |
| --- | --- | --- |
| Agent | `cmd/agent/` | arm64 / mipsel (Keenetic + Entware) |
| Backend | `cmd/backend/` | amd64 (VPS, behind Caddy) |
| CLI | `cmd/wg-monitor-cli/` | host — manual ops |
| Shared protocol | `pkg/wire/` | both |

## Requirements

- **Router:** Keenetic OS 4/5 with Entware, [awg-manager](https://github.com/hoaxisr/awg-manager) 2.8+
- **VPS:** any Linux amd64, Caddy or nginx for TLS termination
- **Telegram:** a bot token + supergroup with per-router topics (forum mode)

## Config

Agent config at `/opt/etc/wg-monitor/config.yaml`, backend config at `/etc/wg-monitor/backend.yaml`.  
See `docs/superpowers/specs/` for the full design spec.
