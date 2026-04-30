# Fleet monitoring landscape & ideas for wg-monitor

**Date:** 2026-04-30
**Branch:** `feature/stage-2` (HEAD `3998572` = `v0.5.0-awgmgr-pivot`)
**Status:** discovery — synthesised from a 2-round web survey of similar
projects + gap analysis vs current `v0.5.0-awgmgr-pivot` codebase.

The goal of this document is to be a thinking-out-loud capture of what the
broader open-source fleet/VPN-monitoring niche looks like in early 2026, what
features are already built into wg-monitor that the rest of the niche
doesn't have, and what concrete improvements could be picked up next. Treat
the priority blocks below as a menu, not a plan — actual work-orders should
go through `superpowers:brainstorming` → `writing-plans` per chosen feature.

## 1. Landscape — what others ship

### 1.1 WireGuard-specific Prometheus exporters

Five-plus projects, all reading `wg show all dump` and exposing
peer-handshake-age + RX/TX as Prometheus gauges:

| Project | Lang | Notes |
|---|---|---|
| [MindFlavor/prometheus_wireguard_exporter](https://github.com/MindFlavor/prometheus_wireguard_exporter) | Rust | de-facto reference; `wireguard_latest_handshake_delay_seconds` |
| [mdlayher/wireguard_exporter](https://github.com/mdlayher/wireguard_exporter) | Go | clean MIT, wgctrl-go-based |
| [kbknapp/wireguard_exporter](https://github.com/kbknapp/wireguard_exporter) | Rust | shells out to `wg show` |
| [itefixnet/prometheus-wireguard-exporter](https://github.com/itefixnet/prometheus-wireguard-exporter) | Bash | tiny, socat-only |
| [mirekdusin/Prometheus-Wireguard-Exporter](https://github.com/mirekdusin/Prometheus-Wireguard-Exporter) | Python | simple |

None of them know about AmneziaWG. None integrate with awg-manager.
None push alerts — they're purely pull-model exporters meant to feed
Grafana + Alertmanager.

### 1.2 WG-config admin UIs

- [WGDashboard](https://github.com/donaldzou/WGDashboard) — Python+Vue web UI
  for managing peers/keys. Read-write, no fleet observability.

### 1.3 Bot-based hobby monitors

- [dev.to "Self-Hosted VPN Monitoring"](https://dev.to/techresolve/solved-self-hosted-vpn-monitoring-wireguard-status-to-telegram-bot-106l)
  — single-host Python script + cron + Telegram. Plain text status dump.
- [capcom6/service-monitor-tgbot](https://github.com/capcom6/service-monitor-tgbot)
  — generic TCP/HTTP service monitor with TG output. No state machine, no
  inline buttons.
- Vendor-specific: MikroTik scripts, Vigor router event hooks. All
  push-only, no return channel.

### 1.4 Generic uptime / status-page tools (UX reference points)

- [Uptime Kuma](https://github.com/louislam/uptime-kuma) — fancy self-hosted
  monitor; sends to TG but only as one-way notifications. Status page UI
  is the "industry default".
- [Gatus](https://dev.to/smit-vaghasiya/monitor-your-services-with-gatus-docker-alternative-to-uptime-kuma-2b4m)
  — config-as-code YAML, multi-protocol. Closest in philosophy to wg-monitor
  (also config-driven), but no return channel from chat.
- [incidentbot](https://github.com/incidentbot/incidentbot) — open-source
  ChatOps incident framework. Slack-only — Telegram support is missing in
  the niche. Severity, digest channels, ack workflow, integrations.

### 1.5 Slack-native incident response (paid, but UX-relevant)

- [PagerDuty Slack ChatOps](https://www.pagerduty.com/blog/integration-slack-chatops/)
- [incident.io](https://incident.io/incident-response-slack)
- [Grafana Cloud + Slack ChatOps](https://grafana.com/blog/chatops-that-actually-works-grafana-cloud-slack-and-ai-powered-observability/)

These have inline ack/silence/resolve buttons + emoji-reaction shortcuts +
incident timeline auto-generation. Mature paradigm; nothing equivalent in
Telegram-native open-source land.

### 1.6 IoT fleet monitoring patterns (transferable design ideas)

- [balena.io blog: small-agent IoT monitoring](https://blog.balena.io/iot-fleet-monitoring-with-datadog-and-balenacloud-how-small-agent-containers-make-a-big-impact/)
- [Netdata IoT use case](https://www.netdata.cloud/solutions/use-cases/iot-monitoring/)

Common patterns in the IoT-fleet world that map well onto a router fleet:
**resilient buffered-replay agents**, **per-device health rollup** with
drill-down, **"telemetry gap = device-down" inference**, **anonymised
aggregate dashboards** for cross-fleet comparison.

## 2. What wg-monitor already has that no-one else does

These are the moats. They didn't show up anywhere else in the survey.

1. **awg-manager API integration** — wraps `127.0.0.1:2222` on Keenetic.
   See `internal/agent/awgmgr/`. None of the WG exporters have this.
2. **RKN-aware DNS check** — `rutracker.org` / `lostfilm.tv` / `linkedin.com`
   probes detect 0.0.0.0 / NXDOMAIN / single-IP-spoof. Western tools lack
   this entirely; they assume DNS is honest.
3. **Mobile-aware grace periods** — `users.kind = static|mobile` +
   `Resumed=true` flag from the agent on >5min gap. Backend suppresses
   OFFLINE alerts for 90s after a `Resumed=true` report.
4. **Topic-per-router segmentation** in a single TG supergroup — one chat
   for the operator, one topic per nickname. The hobby bots all spam a
   single channel.
5. **Inline buttons + FSM-backed actions** — `silence`/`ack`/`mute`/
   `history`/`diag_now`/`pingcheck_now`/`restart_tunnel` directly from the
   alert. Closest precedent (PagerDuty/incident.io) is paid Slack only.
6. **Lock-files for actions** — `opkg` 8min, `restart_awg` 30s. Protects
   against double-tap. Not seen in any other open monitor.
7. **Synthetic `awg_manager` and `tunnels` checks** — explicit health of
   the monitoring infra itself, not just the tunnels under monitoring.
   See `awgmgr_check.go:21` + `tunnels.go:58`.

## 3. Idea menu — prioritised by effort × value × safety

Each idea is tagged with **Effort** (S/M/L) and **Risk** (low/med/high).
"Risk" includes both implementation churn and exposure surface (does it
add a public-facing endpoint, etc).

### 3.1 Priority A — high value, low risk, small

**A.1 Prometheus `/metrics` endpoint on backend (loopback-only).** [M / low]

Bind to `127.0.0.1:9091`, serve current incident state + per-user
last-seen as Prometheus gauges. Zero public exposure when bound to
loopback; operator reads via SSH tunnel or local Grafana. Closes the
biggest gap vs Prom-exporter incumbents and lets the operator plug into
Grafana for historical visualisation without anyone else seeing the data.

Suggested metrics:
- `wgmon_check_status{nickname,check_name}` (gauge 0/1)
- `wgmon_handshake_age_seconds{nickname,tunnel}` (gauge)
- `wgmon_incident_active{nickname,check_name,severity}` (gauge)
- `wgmon_alerts_total{nickname,kind}` (counter)
- `wgmon_agent_last_seen_seconds{nickname}` (gauge)

Adds dep: `prometheus/client_golang`.

**A.2 `wg-monitor-cli list-users` with aggregates.** [S / low]

Local read-only SQL. Show table:
`nickname, kind, awg_iface, last_seen ago, expected_exit_ip`.
Optional follow-up columns once `state` lookups are wired in:
active HARD count, last alert age. **Picked for immediate
implementation in this session** (see §5).

**A.3 `wg-monitor-cli show-incidents` snapshot.** [S / low]

Local read-only. Print active incidents from `incident_state`:
`nickname, check_name, hard_since, silenced_until, acked, last_alert_at`.
Useful before deploys to confirm clean slate.

### 3.2 Priority B — moderate effort, real UX win

**B.1 Per-incident severity (P1/P2/P3).** [M / med]

Add a `severity` column to `incident_state`. Compute on transition into
HARD: tunnel-down + handshake>180s + pingcheck-fail = P1; DNS RKN-block
or single check fail = P2; degraded (handshake 60-180s, mobile gap >30m)
= P3. Render with severity-emoji prefix on alerts and add a `🚨 P1` tag
to the keyboard. Touches FSM + alert formatter + DB schema. Worth a
brainstorm round before plan.

**B.2 Trend-detection / flapping alert.** [M / med]

Lookback window: if a check has flipped state ≥5 times in the last
30min, emit a separate `flapping` incident with its own keyboard
(`mute_flapping_2h`). Pattern from Prometheus Alertmanager `for: 5m` +
hysteresis. Avoids alert fatigue when a tunnel keeps bouncing.

**B.3 `wg-monitor-cli health <nickname>` on-demand snapshot.** [M / low]

Trigger `force_recheck` via cmd channel + wait for response + print
rich JSON. Useful during onboarding or post-deploy verification — no
need to wait 60s for the next scheduled report.

**B.4 Weekly digest into `📊 Сводка` topic.** [M / low]

Five SQL queries + Go template, fired by a tick goroutine:
- top-3 flappiest users this week
- aggregate uptime % across the fleet
- count of opkg upgrades this week (and any post-upgrade incidents)
- longest-running active incident

Cron-style scheduler (cron.v3 or simple timer goroutine). Was deferred
during the awgmgr pivot; re-evaluating cost vs value now.

### 3.3 Priority C — bigger, brainstorm first

**C.1 Config-drift detection.** [L / med] — agent reports MTU /
endpoint / awg-manager pingCheck thresholds; backend stores last-seen
config and alerts on diff. Useful in a heterogeneous fleet where users
hand-edit configs.

**C.2 Speedtest baseline + degradation alert.** [L / med] — daily
speedtest via awg-manager `/api/speedtest/run`, 7-day moving baseline,
alert at <50% of baseline. Pays in user traffic; needs explicit user
consent per-router.

**C.3 Anonymised fleet aggregate dashboard.** [L / high] — opt-in
collector at backend that strips IPs, keeps router-type + WG kind +
uptime. With ~10 users this gives meaningful patterns
("kernel WG flaps less than native"). Privacy trade-off needs a real
design pass.

**C.4 Anomaly detection via per-tunnel percentiles.** [L / low] —
no ML, just rolling p99 of `handshake_age`. Alert if current > p99(7d).
Simple but needs ≥7 days of history per tunnel to be useful.

**C.5 TG mini-app live status page.** [XL / high] — HTML+SSE inside
TG WebApp, opened by `[📊 Live]` keyboard button. High wow-factor,
but a separate frontend project.

### 3.4 Explicitly skipped this session

- **Public read-only HTTP status page.** User constraint: the frontend
  must not be exposed to the internet. Re-evaluate if/when there's a
  reason to surface anything publicly.

## 4. Already implemented (don't re-build)

These appeared in the idea brainstorm but were verified to already
exist in the codebase, so they're crossed off:

- **`awgmgr_alive` synthetic check** — present as `AwgManagerCheck`
  emitting `awg_manager=ok|fail` from `/api/system/info` round-trip.
  See `internal/agent/checks/awgmgr_check.go:21`.
- **`tunnels` synthetic check** — present at `tunnels.go:58`,
  emits `tunnels=ok|fail` so the FSM can recover orphans when
  awg-manager flaps. (Was the fix for the recovery FSM gap noted in
  v0.5.0-awgmgr-pivot commit `7f99b07`.)

## 5. Acted on in this session

- **A.2 `wg-monitor-cli list-users`** — implemented in this commit.
  Read-only local SQLite, no network surface, no dep additions. See
  `cmd/wg-monitor-cli/list_users.go`.

## 6. Sources

Survey conducted via WebSearch on 2026-04-30. Two rounds, four queries each.

- [Awesome WireGuard list](https://github.com/cedrickchee/awesome-wireguard)
- [tuladhar/wireguard-connectivity-monitoring](https://github.com/tuladhar/wireguard-connectivity-monitoring)
- [How to Monitor WireGuard Connections (OneUptime)](https://oneuptime.com/blog/post/2026-01-28-monitor-wireguard-connections/view)
- [Top 8 Self-Hosted Uptime Kuma Alternatives 2026 (Better Stack)](https://betterstack.com/community/comparisons/uptime-kuma-alternative/)
- [Slack incident management guide (incident.io)](https://incident.io/incident-response-slack)

