# wg-monitor

Telegram-fronted monitoring bot for an AmneziaWG fleet (~10 Keenetic routers with Entware).

- Agent (Go, mipsel/aarch64) on each router pushes per-minute reports.
- Backend (Go) on VPS Main behind Caddy at `https://wgmonitor.jkaotlic.duckdns.org/`.

See `docs/superpowers/specs/2026-04-25-wg-monitor-design.md` for the approved design.

## Build

```
make build-host        # local OS, for tests/dev
make build-mipsel      # Keenetic with MIPS little-endian softfloat
make build-aarch64     # Keenetic with ARM64
make pack              # UPX --best on cross-compiled binaries
```

## Stage status

- Stage 0 (bootstrapping): ✅ complete — 2026-04-26 — agent + backend + Caddy live, heartbeat-only e2e verified (1 Keenetic, 3 reports/5 min through `https://wgmonitor.jkaotlic.duckdns.org/`)
- Stage 1 (4 checks + state machine + TG basic): not started
