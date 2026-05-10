# Performance audit — wg-monitor (Go)

Date: 2026-05-10
Scope: backend (long-running on VPS), agent (long-running on Keenetic
mips/arm64, 256MB RAM). Sources: `internal/backend/**`, `internal/agent/**`,
`cmd/{agent,backend}/main.go`.

Severity scale:
- **HIGH** — real load, leak, DoS exposure, or amplifies on every tick / RPC.
- **MEDIUM** — measurably suboptimal; safe to fix when touching the area.
- **LOW** — micro / cosmetic; flag only.

---

## PERF-01 — Agent backend `*http.Client` Timeout=10s rips long-poll mid-flight (HIGH)

`cmd/agent/main.go:50`, `internal/agent/client.go:23-30`, `internal/agent/client.go:62-92`,
`internal/agent/cmdloop/loop.go:37-48`.

`agent.NewClient(..., 10*time.Second)` builds **one** `*http.Client{Timeout: 10s}`
that is then used by both:
- `SendReport` — fine, 10s is plenty for a JSON POST.
- `PollCommand(waitSec=30)` — broken. `http.Client.Timeout` covers the entire
  request including read of the body, so the long-poll connection is closed
  by the agent at ~10s while the backend is still holding the request for up
  to 30s (`maxCmdWait=60s`, `defaultCmdWait=30s` in `internal/backend/handler.go:21-23`).

Effects on every Keenetic agent, every cycle:
1. Long-poll disconnects at 10s instead of returning at 30s with `204 No Content`.
2. Agent log shows `cmdloop poll failed` (`context deadline exceeded`),
   `attempt` increments, exponential backoff up to 60s kicks in.
3. Backend receives 3× the long-poll handshake load it was designed for.
4. TCP/TLS reconnect every ~10s wastes battery on mobile (4G) routers and
   warm-cache CPU on the underpowered MIPS/ARM CPU.
5. The 10s window is also the cap on commands the operator can ever receive
   — anything queued more than 10s before the next reconnect is delayed.

The backend's TG client has a separate `LongPollHTTP: &http.Client{Timeout: 90s}`
(see `cmd/backend/main.go:57` and the contract comment in
`internal/backend/tg/client.go:25-28`). The agent needs the same split.

Fix: in `internal/agent/client.go` introduce two clients (or unset the
top-level Timeout on the long-poll client and rely on `NewRequestWithContext`
+ `waitSec + 5s grace`), e.g. add a `httpLong` field with `Timeout: time.Duration(waitSec+10) * time.Second` and use it from `PollCommand`.

---

## PERF-02 — `mscLoc()` calls `time.LoadLocation("Europe/Moscow")` on every alert (MEDIUM)

`internal/backend/alerts/format.go:546-552`, called from lines 86 and 156
(`FormatHard`, `FormatRealert`). `time.LoadLocation` opens the tzdata file on
every invocation — historically expensive on the hot alert path on Linux,
which is exactly where this is called per HARD/STILL-DOWN/Smart-reply.

Fix: load once at package init / once via `sync.Once`:
```go
var mscOnce sync.Once
var msc *time.Location
func mscLoc() *time.Location {
    mscOnce.Do(func() {
        msc, _ = time.LoadLocation("Europe/Moscow")
        if msc == nil { msc = time.FixedZone("МСК", 3*3600) }
    })
    return msc
}
```

---

## PERF-03 — `heartbeat.Watcher.scan` is N+1 over user count (MEDIUM)

`internal/backend/heartbeat/watcher.go:115-161`. Each `ScanEvery` tick:
```
GetAll() // 1 query
for u := range users { LatestPerUser(u.ID) }   // N queries
```
With 10 users that's 11 queries per scan. The scan default is short (likely
1 min), and `LatestPerUser` itself is `SELECT MAX(ts) FROM events WHERE user_id=?`
— see DB-01 in `10-database.md` for index coverage on `(user_id, ts)`. Even
with the index, going through driver / cgo-free SQLite per call has fixed
overhead.

Fix: one query — `SELECT user_id, MAX(ts) FROM events GROUP BY user_id` — and
join with the user list in Go. Or `SELECT u.id, MAX(e.ts) FROM users u LEFT JOIN events e ON e.user_id=u.id GROUP BY u.id`.

---

## PERF-04 — `dispatcher.collectNeighbors` and `realert.neighborSummaries` re-`json.Unmarshal` Details on every alert (MEDIUM)

`internal/backend/alerts/dispatcher.go:128-155`,
`internal/backend/realert/poller.go:75-108`,
`internal/backend/callbacks/router.go:687-711` and `:586-620`.

For every HARD / STILL-DOWN / Smart-reply / Tunnels-Panel render, we
`LatestEventsByPrefix(userID, "tunnel_")` and then `json.Unmarshal(...)` each
row's `details_json` into a brand-new `map[string]any`. For users with N
tunnels, that's N unmarshals per render.

This is bounded (≤ ~10 tunnels typical) but not free — each `map[string]any`
allocation pressures GC and the unmarshal is the most expensive single op
on the alert path.

Lower-impact fix:
- The agent already sends a flat shape; the backend could store a typed
  projection in a new column (`tunnel_name`, `interface`, `handshake_age_sec`,
  `ping_check_status`) at insert time, eliminating the unmarshal on read.
- Or at least share one decoded struct across the four read sites — currently
  each has its own field-extraction code (`strOrEmpty`, `intOrZero`).

If you keep the JSON-blob design, use `json.Decoder.UseNumber()` is irrelevant
here — but consider `easyjson` or a small custom decoder for the 7 keys we
actually read.

---

## PERF-05 — `cmd.Queue.Dequeue`/`AwaitResult` use `time.After` inside per-call goroutine (MEDIUM)

`internal/backend/cmd/queue.go:130-140`, `:182-192`. `time.After(holdTimeout)`
returns a `*time.Timer` that runs to completion even when the `stop` channel
closes. Per long-poll that's a 30-60s zombie timer holding a small
allocation + a runtime timer-heap entry.

At fleet steady state (~10 agents, each long-polling continuously), that's
10 zombie timers at any moment plus brief overlap on reconnect — not a leak,
but pure waste.

Fix:
```go
t := time.NewTimer(holdTimeout)
defer t.Stop()
go func() {
    select {
    case <-ctx.Done():
    case <-t.C:
    case <-stop:
        return
    }
    q.signal.Broadcast()
}()
```

---

## PERF-06 — `cmd.Queue.signal.Broadcast()` thundering-herd across all per-user waiters (MEDIUM)

`internal/backend/cmd/queue.go:115` (Enqueue), `:173` (RecordResult),
`:139, :191` (the timeout watcher goroutines).

`sync.Cond.Broadcast()` wakes **every** Dequeue / AwaitResult waiter,
including N-1 users whose queues didn't change. With 10 agents long-polling,
every Enqueue or RecordResult costs 10 wakeups + 10 mutex re-acquires + 10
condition re-checks. Today that's negligible (~1 cmd/min); at fleet 100 it
would be O(N) per Enqueue.

Fix: per-user `chan struct{}` for "wake one waiter", or per-user `*sync.Cond`.
Low priority until fleet > 50.

---

## PERF-07 — `checks.OK`/`Fail` clone the details map on every probe (LOW)

`internal/agent/checks/checks.go:34-59`. Each probe builds a `details` map,
then `OK`/`Fail` allocates a new map and copies every entry — for no reason
the caller can observe (caller never re-uses its map after the call). For the
multi-tunnel case (TunnelsCheck → ~10 tunnels per cycle, ~1/min) that's 10
extra map allocations per minute on a 256 MB router.

Fix: drop the clone; pass through the caller's map. If a caller really wants
to reuse the source map, the contract becomes "OK/Fail takes ownership".

---

## PERF-08 — Agent has no shared `*http.Transport` across DNS / external_reach / awgmgr / backend (LOW)

`cmd/agent/main.go` builds: `http.Client{Timeout: 5s}` for DNS,
`http.Client{Timeout: 6s}` for external_reach (with optional iface-bound
Transport), `awgmgr.New` builds its own, `agent.NewClient` builds its own.
None set `Transport` explicitly, so each falls back to
`http.DefaultTransport` — which actually IS shared across them (good news).

But the iface-bound external_reach client (cmd/agent/main.go:206) builds a
fresh `*http.Transport` with no `MaxIdleConnsPerHost` / `IdleConnTimeout`
override — every probe round can re-dial. For a 6s timeout budget on a slow
ARM CPU this is cheaper than full re-handshake.

Fix (low priority): tune the iface-bound transport with
`MaxIdleConnsPerHost: 2, IdleConnTimeout: 60*time.Second` so back-to-back
external_reach probes reuse a TLS session.

---

## PERF-09 — `LatestEventsByPrefix` correlated-subquery on every read (MEDIUM)

`internal/backend/db/events.go:96-136`. The query:
```sql
WHERE e1.ts = (SELECT MAX(e2.ts) FROM events e2
               WHERE e2.user_id = e1.user_id AND e2.check_name = e1.check_name)
```
is a correlated subquery. With `idx_events_user_ts(user_id, ts DESC)` and a
typical fleet size, SQLite evaluates this fast, but it scales as
`O(events_for_user)` per render. For Tunnels-Panel + Smart-Reply + dispatcher
neighbours on the same user this query runs 3× per panel open.

Fix: rewrite to window-function (SQLite ≥ 3.25):
```sql
WITH ranked AS (
  SELECT *, ROW_NUMBER() OVER (PARTITION BY check_name ORDER BY ts DESC) rn
  FROM events WHERE user_id=? AND check_name LIKE ? ESCAPE '\'
) SELECT ... FROM ranked WHERE rn = 1 [AND ts >= ?];
```
Or memoize per (userID, request-scoped) inside the callbacks Router for the
duration of one HTTP/TG call.

---

## PERF-10 — Auth middleware does sha256 + DB lookup on every agent report (LOW)

`internal/backend/auth.go:23-51` calls `db.UsersRepo.GetByToken(presented)` on
every `/v1/report` and `/v1/cmd` poll. `hashToken` does `sha256.Sum256` per
call (cheap), and the lookup uses `idx_users_token_hash`. Constant-time
compare is also done — fine.

This is fine at fleet 10. At fleet 1000+ on a 1-min agent cycle that's 16
lookups/sec — still fine. Flagged only because *every* agent RPC pays the
cost. A token→User in-memory LRU keyed by token-hash with 30s TTL would zero
this cost. Not worth doing now.

---

## PERF-11 — `ReadAll(LimitReader(..., 1<<20))` allocates fresh 1 MB-capable buffer on every awgmgr call (LOW)

`internal/agent/awgmgr/client.go:45, :70, :104`,
`internal/agent/awgmgr/routing.go:104`. Every awgmgr GET reads up to 1 MB into
a fresh slice. Real responses are ~1-10 KB. `io.ReadAll` grows in chunks so
the average is fine, but on the agent every cycle does 4-5 awgmgr calls.

Fix: `bytes.Buffer` with a sized pool (`sync.Pool`) shared across awgmgr.
Saves a few KB of GC pressure per cycle. Flag-only; not worth the complexity.

---

## PERF-12 — Reporter `runAll` lock-and-append costs little but synchronisation visible per check (LOW)

`internal/agent/reporter.go:143-177`. Each check goroutine takes `mu.Lock()`
to append its result. With ~5-10 checks per cycle this is fine; at >100
checks the goroutine spawn + lock convoy starts dominating. Today: low
priority; flag only if you ever increase check count materially.

---

## PERF-13 — `external_reach` and `connectivity` probes use HTTP GET, not HEAD (LOW)

`internal/agent/actions/connectivity.go:107-137`,
`internal/agent/checks/external_reach.go:67`. Comments call them "HEAD probes"
but the actual request method is `MethodGet`. For YouTube `/generate_204` and
favicon URLs the body is tiny; for `vk.com/` the body is ~100 KB which the
agent then immediately discards. With BindToDefault on a slow upstream this
adds visible bytes/second.

Fix: switch to `http.MethodHead`. If a server (Telegram web) doesn't honour
HEAD, fall back to GET + `Range: bytes=0-0`.

---

## PERF-14 — `upstream.Cache` unbounded growth and no negative-cache backoff (LOW)

`internal/backend/upstream/versions.go:38-101`. The cache stores one Entry
per known source name; sources are closed-set in `NewCache(sources)`, so
unbounded growth is structurally impossible. Good.

But: when GitHub returns 429 / 5xx the Entry is stored with `Err != nil` and
the same `FetchedAt`. Latest's TTL check applies equally to errors, so a
single 429 makes all readers wait the full TTL before retrying. For a
backend that intentionally caches against the 60-req/h anonymous cap this is
correct, but consider a shorter "negative TTL" so transient errors don't
suppress a working response for the full TTL.

---

## PERF-15 — Reporter cold-start: blocking config load + sync awgmgr probe + sync ndmc shell-out before first report (MEDIUM, agent cold-start)

`cmd/agent/main.go:111-166` (`buildDNSCheck`) does at startup:
1. `ndmc -c "show running-config"` (subprocess, ≤ 5s timeout) — synchronous.
2. `keenetic.FetchIfaceMap` HTTP call to awgmgr — synchronous.

Both block before `rep.Run(ctx)` is even called. On a Keenetic that just
booted, awg-manager and ndmc may not be ready — those calls each wait the
full 5s budget. Worst-case agent-startup ⇒ 10s before first report.

This pattern matters on Keenetic because the agent restarts on:
- Keenetic firmware update (every ~1-2 weeks).
- `S99wg-monitor restart` triggered by the operator.
- OOM-killer (256 MB RAM, low-memory routers).

Fix: do the discovery in a goroutine, send first report with no DNS
endpoints, then refresh on a slow re-discover ticker (every 10 min).
Reporter already accepts dynamic check sets if you change the slice
under a lock.

---

## PERF-16 — agent serial heavy operations under one mutex in `Reporter.runAll` (LOW)

`internal/agent/reporter.go:143-177`: all checks fan out via goroutines with
`perCheckTimeout = 10*time.Second`. Good. But **multiChecks** (TunnelsCheck)
fetch `TunnelsAll` then `PingCheckStatus` sequentially within one goroutine
(`internal/agent/checks/tunnels.go:38-50`). Two awgmgr calls back-to-back is
~2× round-trip — could be parallelised with errgroup, like `route_status.go`
already does.

Cold relevance only because `TunnelsCheck` is the longest-running per-cycle
work on the agent.

---

## PERF-17 — `fmt.Sprintf` / `strings.Builder` patterns in the alert formatter are fine (LOW)

`internal/backend/alerts/format.go` — uses `strings.Builder` and `fmt.Fprintf`
correctly; no string-concatenation in loops. Format/SmartReply also look OK.
No action.

---

## PERF-18 — `parseEventTS` retries 5 formats per row (LOW)

`internal/backend/db/events.go:189-208`. For every row in
`LatestEventsByPrefix` / `ListSince` we try 5 layouts. The producer side
(`Insert`) writes via Go's default time encoding (modernc.org/sqlite stores
as Go time string) so the format is stable per-DB. After the first
successful parse, we could cache the winning index per process and try it
first.

Negligible; flag only.

---

## Summary of priorities

| ID | Severity | Surface | Cost-to-fix |
|---|---|---|---|
| PERF-01 | HIGH | every Keenetic agent, continuous | trivial — split client |
| PERF-02 | MEDIUM | hot alert path, all backends | 5 lines |
| PERF-03 | MEDIUM | backend tick (1/min × users) | 1 query rewrite |
| PERF-04 | MEDIUM | every TG render | small refactor |
| PERF-15 | MEDIUM | agent cold-start (Keenetic boot) | move to goroutine |
| PERF-05/06/09 | MEDIUM | scaling cliffs, today benign | targeted change |
| rest | LOW | hygiene | when touching |

PERF-01 alone justifies a patch release: every agent in production right now
is reconnecting every 10s instead of every 30s, with the visible symptom
being "cmdloop poll failed" warnings in the agent journal.
