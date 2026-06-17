# Notification audit, 2026-06-17

Scope: Telegram alerts, dashboard/status cards, backend notifier flows, command result notifications, maintenance/update/deploy notifications, route/tunnel/diagnostic/pingcheck/opkg/firmware/security/access-control notification paths.

## Fixed findings

- `internal/backend/realert`: still-down reminders now use the router's effective Telegram chat ID, not the global primary chat. This prevents reminders for extra-chat routers from being sent to the wrong group/topic.
- `internal/backend/callbacks`: maintenance action banners now treat every non-`ok` command result as an error. `timeout`, `locked`, and future non-ok statuses no longer render as green success.
- `internal/backend/callbacks`: command history now uses the router's effective Telegram chat ID with the router thread, avoiding primary-chat/thread mismatches.
- `internal/backend/alerts`: deferred self-update failure text no longer promises an automatic retry after backend has cleared `pending_version`. It tells the operator that pending was cleared and that repeating update is safe after checking the failure detail.
- `internal/backend/alerts`: HARD incident state is now persisted before Telegram topic creation/send. If Telegram topic creation or send fails, the backend still records the hard state and does not mark an alert as sent.

## Confirmed coverage

- Event sources: periodic agent reports, heartbeat watcher, command results, route/tunnel/maintenance/pingcheck/opkg/deploy callbacks, dashboard command dispatch, and fleet batch actions were mapped.
- Severity/status: HARD/Recovery FSM, dashboard `online/sleeping/offline/alert`, route `partial`, maintenance non-ok statuses, command `locked/timeout/err`, and heartbeat offline/sleeping flows were checked.
- False positives: route partial refresh, stale route/tunnel panels, duplicate command results, unissued/stale command results, cached agent result reposts, heartbeat mobile sleep grace, and resume grace are covered by tests or explicit code paths.
- False negatives: backend-down self-monitoring is documented in `docs/external-uptime-probe.md`; HARD topic/send failures are now persisted in incident state.
- Telegram routing: dispatcher, lifecycle notifier, realert, history, callbacks, ACL topic/chat checks, stale topic self-heal, and extra chat routing were checked.
- Dashboard surfaces: summary status counts, pending deploy conflict handling, command polling, destructive command rejection, hidden/unsafe backend URL rejection, and dashboard command rendering were checked.
- Command rendering: diag, pingcheck, route status/rebind/add/delete, tunnel import/toggle/restart/delete, opkg upgrade/feed repair, firmware/version audit, maintenance service restart, deploy/update result, and router doctor surfaces were checked.
- Required actions: high-risk operations remain behind explicit confirmations or config gates; error cards generally include retry/check/maintenance next actions.
- Rate limiting/dedup/suppression: incident ack/silence/mute, heartbeat renotify, realert cadence, duplicate/stale command result handling, pingcheck inflight, and mobile sleep one-shot behavior were checked.
- Tests/security/docs: full Go gate passed after each code batch; README and external uptime probe docs cover the operational notification model and backend silent-failure limitation.

## Low-risk notes

- `pkg/wire` still documents valid command result statuses as `ok/err/locked/timeout`; route rebind can legitimately emit `partial`. Backend accepts unknown statuses forward-compatibly and route notifier handles `partial`, so this is log-noise/taxonomy drift rather than a user-facing notification bug.
- Fleet batch rendering is binary (`ok`/`failed`/`skipped`/`queued`). Current fleet actions are `router_doctor` and `self_update`, which return `ok/err`; if future bulk actions can emit `partial`, add a separate `partial` bucket.
- Dashboard command result rendering shows non-`ok` statuses as error state, but it does not visually distinguish `partial` from other non-ok statuses. This is acceptable for current dashboard-allowed actions; add warning styling if dashboard exposes partial-emitting route actions later.
