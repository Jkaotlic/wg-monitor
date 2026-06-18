# Notification false-positive & clarity audit, 2026-06-18

Follow-up to the 2026-06-17 notification audit. That pass covered TG **routing**
correctness (effective chat IDs, hard-state persistence, error-vs-success
rendering). This pass targets what it did not: **false-positive sources in the
agent-side check evaluation**, and the **actionability of the buttons** an
engineer actually taps.

Scope: every notification surface was re-mapped — core FSM alerts (HARD /
Recovery / STILL-DOWN realert / ROUTER-OFFLINE), smart-reply "📊 Что
происходит?", wake/sleep lifecycle, deploy/maint/routes notifiers, command
results — plus all seven agent checks (tunnel / dns / hydraroute / awg_manager /
tunnels-registry / external_reach / heartbeat).

## Fixed

### FP-1 — external_reach counted bot-rejection 4xx as an outage
`internal/agent/checks/external_reach.go`

The default targets include `https://www.instagram.com/favicon.ico` and other
real services. Instagram/YouTube routinely answer a non-browser User-Agent
(`wg-monitor/external-reach`) with **HTTP 403 / 429**. The check counted any
non-2xx/3xx as "unreachable", so a persistent Instagram 403 permanently ate one
of the `fail_threshold: 2` slots — one transient hiccup on another target then
tipped the whole check to `fail` → after 3 FSM ticks → false HARD **"Внешние
сервисы недоступны через туннель"**.

A 4xx *proves* the path works (DNS → TCP → TLS → HTTP round-trip all succeeded);
the service simply refused the bot. The real failure modes this check exists to
catch — RKN regional filter, blackholed CDN edge — surface as **transport
errors** (timeout / refused / reset) or **5xx**, both still caught.

Fix: a completed response with status `< 500` counts as reachable; only `>= 500`
and transport errors count as unreachable. New tests
(`external_reach_test.go`): 4xx (400/401/403/404/429/451) → reachable; 2xx/3xx →
reachable; 5xx → fail; dead endpoint → fail.

### FP-2 — periodic checks used the first defaultRoute, not the authoritative one
`internal/agent/checks/tunnels.go`, `cmd/agent/main.go`,
`internal/agent/awgmgr/types.go`

Found by cross-checking the operator's live router: **two tunnels both carry
`defaultRoute:true`** (awg10 and awg12), but awg-manager's authoritative default
is `settings.download.routeTag = "awg-awg12"` → the real egress is **awg12**
(which also carries ~10.5 GB vs awg10's ~1.7 MB). The 2026-06-18 morning HR-Neo
fix taught `route_status` (the Routes panel) to read `routeTag`, but two
**periodic** paths still took the first-listed `defaultRoute=true`:

- `tallyRouteCounts` credited the HR-Neo fall-through blast radius to awg10
  instead of awg12 — so the wrong tunnel's HARD alert showed the rule count.
- `pickDefaultRouteIface` (external_reach binding) probed **awg10's egress, not
  awg12's** — a false reachability signal about a tunnel the user barely uses.

Fix: promoted the `routeTag` resolver to a shared
`awgmgr.Settings.ActiveDefaultTunnelID()` (route_status now delegates to it), and
threaded the authoritative id into both periodic paths, with the legacy
first-defaultRoute heuristic preserved as a best-effort fallback when settings
are unavailable. New tests: `TestTallyRouteCounts_CreditsAuthoritativeDefault…`,
`TestTunnelsCheck_RouteTagCreditsRealDefault…`,
`TestPickDefaultRouteIfacePrefersRouteTagOverFirstListed`.

### UX-1 — STILL-DOWN reminder dropped the action buttons
`internal/backend/realert/poller.go`

The original tunnel HARD alert ships `[🔁 Перезапуск awg-manager][📊
Диагностика][▶ Тест связи]`; hydraroute ships the HR-Neo row; a mobile
heartbeat ships `[🔄 Дай отчёт сейчас]`. The 6h-later realert reminder built its
keyboard with `HardAlertKeyboard(uid, check)` and **no options**, so it carried
only silence/ack/mute/history. The operator acting on the reminder — the message
they're *more* likely to actually see than the 3am original — had no way to
restart or diagnose without digging into a panel.

Fix: the poller now mirrors `alerts.Dispatcher.Handle`'s per-category options
(tunnel / hydraroute / mobile-heartbeat). New test
(`TestTickTunnelRealertCarriesActionButtons`).

## Investigated and deliberately left unchanged

- **Handshake-age ≥ 180s → "есть подозрения" (smart-reply Degraded) / stale-
  handshake → HARD.** This looks like a false positive for idle AWG/WG tunnels
  without `PersistentKeepalive`, but it is a **deliberate early-warning design**:
  `smart_reply_test.go` explicitly asserts `{180s, StateDegraded}` even with a
  green pingCheck, and the threshold was intentionally raised 60→180 to align
  with the rendered text. The agent HARD path already suppresses the safe case
  (`suppressUnusedTunnelFailure`: no routes + pingCheck disabled → OK) and the
  threshold is operator-tunable (`checks.awg.handshake_max_age_sec`). Suppressing
  it further would risk **false negatives** (missing a genuinely dead idle
  tunnel). Left as-is — this is an operator policy choice, not a bug.

- **Per-tunnel restart button on the HARD alert.** Replacing "🔁 Перезапуск
  awg-manager" (restarts the whole daemon) with the per-tunnel `tunnel_restart`
  used by smart-reply is tempting, but `tunnel_restart` runs through
  `guardStaleTunnelPanelAction`, which on a Tunnels-Panel cache-miss **edits the
  message into a panel** — it would destroy the alert message. Not worth the
  regression risk; the awg-manager restart label is honest. Left as-is.

- **DNS `fail_threshold: 2` + RKN majority** and **hydraroute "installed but not
  running" when the mechanism probe errors** — both debounced by the 3-fail FSM
  and judged acceptable; no change.

## Round 2 — improvements driven by the live router picture

Acted on the operator's "improve everything you proposed". All TDD, working tree.

- **#1 — multiple-defaultRoute warning.** `🩺 router-doctor` now flags the
  ambiguous "two tunnels both `defaultRoute:true`" config and names the
  authoritative one from `routeTag` (`internal/agent/actions/router_doctor.go`).
- **#2 — pingCheck nudge.** When a tunnel HARD/realert fires on a stale handshake
  AND its pingCheck is disabled, the advice now suggests enabling pingCheck (the
  active probe that resolves the idle-vs-broken ambiguity) — only when disabled,
  so it isn't noise (`internal/backend/alerts/format.go`).
- **#3a — DB-aware `/readyz`.** Replaced the bare `PingContext` with a real table
  read (`db.HealthCheck`) so SQLite-corruption-class failures (connection alive,
  reads dead) flip `/readyz` to 503 instead of silently passing. Fixed
  `docs/external-uptime-probe.md` to probe `/readyz` (not `/healthz`, which is
  liveness-only) and corrected the cron script's broken `body == "ok"` keyword.
- **#3c — dead-man digest.** New opt-in `internal/backend/digest` poller posts a
  daily "🟢 Монитор жив — N/M роутеров онлайн" to the primary chat; its absence
  signals a dead backend. `digest.{enabled,hour_msk,online_window_sec}` in
  backend.yaml, default off.
- **#3b — external probe (partial).** Probe doc fixed + quick-install added;
  full wizard auto-provisioning (SSH to a third host type) documented as
  deferred with rationale.
- **#4 — external_reach per-target transparency.** Reachable-but-4xx targets now
  land in a `targets_degraded` bucket and render as "Доступны, но вернули отказ:
  Instagram (403) — это не сбой связи, сервис отверг бота" instead of being
  hidden in "Работают".

## Verification

- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./... -count=1` — **29 packages OK, 0 failures** (incl. new `digest`)
- `git diff --check` — clean

## Live cross-check against the operator's router (2026-06-18)

Reached the home Keenetic's awg-manager directly at `http://192.168.0.1:2222`
(read endpoints need only `X-Requested-With: XMLHttpRequest`; no auth). The
backend on the Pi (`192.168.0.87`) was unreachable/off. Router clock matched
the workstation exactly, so handshake ages are exact.

Live state — Keenetic Ultra (KN-1811), fw 5.1 Beta 4, HydraRoute 3.11.0:

- **awg10** "amnezia_for_awg-nktelecom": running, `defaultRoute:true`, nwg0 /
  Wireguard0, AWG (awg2.0, nativewg, MTU 1280), handshake age ~8s, **pingCheck
  disabled**.
- **awg12** "NetherlandsAmsterdamH17": running, `defaultRoute:true`, nwg3 /
  Wireguard3, same backend, handshake age ~75s, **pingCheck disabled**.
- pingCheck **globally disabled**; static routes empty; many HR-Neo fall-through
  DNS policy rules (OpenAI/Anthropic/ChatGPT/Discord/Amnezia geosites) → all
  credited to the default tunnel awg10.

Verdicts:

- **FP-1 confirmed as the operator's reported "внешние сервисы" false alert.**
  external_reach binds to the default-route tunnel (awg10) and probes
  YouTube/Telegram/**Instagram**; Instagram answers the bot UA with 403, which
  the old logic scored as unreachable. The fix resolves it directly.
- **Idle-handshake HARD FP is NOT currently active**: both default-route tunnels
  hold fresh handshakes (<180s) despite pingCheck off — they carry steady
  traffic. This validates leaving the handshake logic unchanged; tightening it
  would only have risked false negatives. (Caveat: with pingCheck off, a
  genuinely idle period on a route-bearing tunnel could still trip it — enabling
  pingCheck or `PersistentKeepalive` is the operator-side mitigation.)
- **HydraRoute healthy** (installed + running) → no hydraroute FP.

## Not done (needs NDMC / SSH)

awg-manager does not expose the Keenetic DNS upstream list, so the **DNS**
half of the operator's reported pain could not be verified end-to-end from
here. To check it, read the upstreams the agent auto-discovers via
`ndmc show running-config` (DNS endpoints + any per-tunnel binding) and confirm
none false-fail under the `fail_threshold: 2` / RKN-majority rules. The agent
already skips endpoints whose ndms interface is absent from the live tunnel map,
so a down-tunnel-bound resolver is suppressed, not failed.
