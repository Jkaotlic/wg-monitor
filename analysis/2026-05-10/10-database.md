# Database audit — wg-monitor (SQLite via modernc.org/sqlite)

Date: 2026-05-10
Scope: `internal/backend/db/*`, `internal/backend/retention/*`, `internal/backend/state/*`,
`cmd/backend/main.go`. Empirical verification done with a temporary `audit_test.go`
(now removed) running `EXPLAIN QUERY PLAN` against a freshly-migrated DB.

Severity scale: **CRITICAL** (data loss / corruption / prod-blocking) ·
**HIGH** (latency/concurrency/correctness under load) · **MEDIUM** (perf, hygiene) ·
**LOW** (style, clarity, minor).

---

## Empirical baseline (good news first)

Verified on a fresh DB:

- `journal_mode=wal` — correct, applied per-conn via DSN.
- `foreign_keys=1` — verified across 8 pooled connections; `ON DELETE CASCADE`
  from `users → events` confirmed working in a delete-test.
- `busy_timeout=5000` — applied per-conn.
- Migrations re-apply cleanly (`TestOpenIdempotent` exists and passes); every
  CREATE uses `IF NOT EXISTS`.
- Two real ALTER TABLE migrations (`incident_state.acked`, `users.kind`) are
  guarded by `pragma_table_info` probes — idempotent, safe.

---

## Findings

### DB-01 — `incident_state` queries are full-scan (HIGH)

`internal/backend/db/state.go:165` (`StaleHards`), `state.go:139` (`AllActiveHard`),
`internal/backend/callbacks/router.go:716` (per-user active incidents),
`state.go:118` (`BumpLastAlertAt`).

EXPLAIN QUERY PLAN confirms `SCAN incident_state` for these. The PK is
`(user_id, check_name)` — useful for `Get/Save`, useless for
`WHERE current_status='hard'` and the realert poller that runs every
`RealertTickSec` (default 5 min).

Recommendation: add a partial index
```sql
CREATE INDEX IF NOT EXISTS idx_incident_state_hard
  ON incident_state(last_alert_at)
  WHERE current_status = 'hard' AND acked = 0;
```
Sized at <fleet HARD-incidents> rows, irrelevant to write cost. Speeds up
realert/StaleHards from O(rows) to O(hard rows).

### DB-02 — `events` prune is full-scan (HIGH)

`internal/backend/db/events.go:138` (`PruneBefore`).
`EXPLAIN QUERY PLAN DELETE FROM events WHERE ts < ?` → `SCAN events`.
Existing index `(user_id, ts DESC)` doesn't help a global ts predicate.

For a fleet of N agents posting every minute over 30 days the events table
hits multi-million rows; the daily prune blocks WAL-checkpoints meanwhile.

Recommendation: add `CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts)`.
It costs ~1 extra B-tree per insert, but prune becomes index-range delete
and the 24h prune ticker stops monopolising the writer.

### DB-03 — `users.telegram_thread_id` lookup is full-scan (HIGH)

`internal/backend/db/users.go:176` (`GetByThreadID`). EXPLAIN: `SCAN users`.
This is hit on **every Telegram update** that lands in a forum topic
(callbacks router maps `message_thread_id → user`). Linear in fleet size on
every TG callback / smart-reply.

Recommendation:
```sql
CREATE INDEX IF NOT EXISTS idx_users_thread_id
  ON users(telegram_thread_id) WHERE telegram_thread_id IS NOT NULL;
```

### DB-04 — `idx_events_user_ts` doesn't cover `(user_id, check_name, ts)` (MEDIUM)

`events.go:53` (`LatestEvent`), `events.go:96` (`LatestEventsByPrefixSince`),
`events.go:157` (`ListSince`). EXPLAIN for
`WHERE user_id=? AND check_name=? ORDER BY ts DESC LIMIT 1` shows
`SEARCH events USING INDEX idx_events_user_ts (user_id=?)` — only the
user_id prefix is used; per-user events are then scanned in memory for
the `check_name` filter and re-sorted.

Recommendation: replace with `CREATE INDEX idx_events_user_check_ts
ON events(user_id, check_name, ts DESC)`. Drop the old one — it's a
strict prefix superset of one needed by `LatestPerUser`'s `MAX(ts) WHERE
user_id=?` (covering index hit confirmed; the new index serves that too).

### DB-05 — Multi-write paths run without transactions (HIGH)

Zero `Begin()` / `BeginTx` calls in the entire codebase (verified by grep).
Several hot paths perform 2+ writes that should be atomic:

- `internal/backend/handler.go:115-145` — per `/v1/report` POST: `UpdateLastSeen` →
  N× (`Events.Insert` + `State.Save`). A crash after `Events.Insert` but
  before `State.Save` leaves an event with no FSM advance, manufacturing
  fake flap counters on next report.
- `internal/backend/alerts/dispatcher.go:47-94` (Hard branch): `IncSoftFlap` +
  `State.Save`; or `SendMessage` + `State.Save`. The TG-call-then-save
  ordering is intentional (you'd rather send twice than not at all), but
  the SoftFlap pair (`IncSoftFlap` + `State.Save`) should be atomic.
- `internal/backend/realert/poller.go:144-156` — `tg.SendMessage` then
  `BumpLastAlertAt`. A crash between the two re-pages on next tick;
  acceptable for the use-case.

Recommendation: introduce `db.WithTx(func(tx *sql.Tx) error)` helper and
wrap the report ingest loop. SQLite serialises writers anyway, so this
costs no concurrency, but you gain atomicity on crash and (bonus) you can
combine N inserts inside one fsync window — a measurable speedup at
fleet scale.

### DB-06 — `synchronous=FULL` is the default; `NORMAL` is recommended with WAL (MEDIUM)

`db.go:21`. EXPLAIN of `PRAGMA synchronous` on every pooled conn returns
`2 (FULL)`. SQLite docs explicitly recommend `NORMAL` with WAL: "WAL mode
is safe from corruption with synchronous=NORMAL", and the throughput
delta on small writes is 5-10×.

Recommendation: extend DSN with `&_pragma=synchronous(normal)`.

### DB-07 — `VACUUM` blocks the entire database (MEDIUM)

`internal/backend/retention/policy.go:87-94`. `VACUUM` takes an exclusive
lock; while it runs, every reader (TG callback router, /v1/cmd long-poll,
heartbeat scan) blocks behind `busy_timeout=5000ms`. On a multi-GB DB
this is more than 5s and you'll see `SQLITE_BUSY` errors in logs.

Recommendation: prefer `PRAGMA incremental_vacuum` after enabling
`auto_vacuum=INCREMENTAL` at create-time; or schedule `VACUUM` only
after `wal_checkpoint(TRUNCATE)` and gate it behind an off-hours window
config knob. Document the user-visible stall.

### DB-08 — `daily_soft_flaps` lacks pruning / index (LOW)

`migrations.sql:40-47`. PK is `(user_id, check_name, date)`. There is no
retention policy that prunes old `date` rows — `daily_soft_flaps` grows
unbounded (1 row × N users × M checks per day).

Recommendation: prune in `retention.Policy` with the same `EventsDays`
cutoff (`DELETE FROM daily_soft_flaps WHERE date < ?`); add
`CREATE INDEX idx_daily_soft_flaps_date ON daily_soft_flaps(date)` to
keep the prune cheap.

### DB-09 — Heartbeat scan is N+1 (MEDIUM)

`internal/backend/heartbeat/watcher.go:115-137`. For every user in
`GetAll()` (no LIMIT) it issues `LatestPerUser` (1 SELECT each). Runs
every `ScanIntervalSec` (default ~30 s).

Recommendation: replace with a single SELECT:
```sql
SELECT user_id, MAX(ts) FROM events GROUP BY user_id
```
Joined to the user list in Go. With a fleet of 50, that's 51 queries → 1.

### DB-10 — `dispatchFleetHealth` & `realert` re-resolve users in a loop (LOW)

`callbacks/router.go:807` calls `GetAll()` to build a map (already
batched). Good. `realert/poller.go:118` calls `Users().GetByID(sh.UserID)`
inside the StaleHards loop — N+1 again.

Recommendation: pre-fetch `Users().GetAll()` once per tick and look up
locally; on a fleet of 50 with 5 hard incidents this is negligible, but
the realert tick is your only operator-visible "the bot is awake" path,
so making it cheap is good hygiene.

### DB-11 — No prepared-statement reuse (LOW)

Zero `db.Prepare` calls in the codebase. modernc.org/sqlite caches
parsed statements per-connection internally, so the cost of re-parsing
SQL strings on every `Exec/QueryRow` is small but non-zero. With
`go test -bench` you can measure it; under typical fleet load this is
< 1% of overhead.

Recommendation: not worth fixing now. Note for future profiling.

### DB-12 — `ListSince` / `LatestEventsByPrefix` have no LIMIT (LOW)

`events.go:84-136` and `:157-185`. A pathological caller (long-running
incident with a thousand tunnels) could materialise unbounded slices.
Currently bounded by use-case (`tunnel_*` neighbours, single-check ts
list); not exploitable, but worth a comment / explicit cap if
`LatestEventsByPrefixSince` is ever used by a non-trusted call site.

### DB-13 — `events.details_json` stores `null` literally (LOW)

`alerts/dispatcher.go:140`: `if r.DetailsJSON != "" && r.DetailsJSON != "null"` —
a workaround for `json.Marshal(nil) == "null"` written into the column.
Cosmetic but indicates the schema lets a trivially invalid value in.

Recommendation: in `EventsRepo.Insert`, normalise `"null"` (and `""`?)
to NULL via `sql.NullString`, so consumers don't carry the workaround.

### DB-14 — `incident_state` orphan rows on agent rename (MEDIUM)

Documented in `docs/superpowers/specs/2026-04-28-wg-monitor-stage-2-design.md:495`.
When a check is renamed/removed in agent code, its `incident_state` row
stays "current_status='hard'" forever — surfaces in Fleet-Health.

Recommendation: add a periodic cleanup that drops `incident_state` rows
whose `(user_id, check_name)` has had no `events` for > 7 days. Safe
because Recovery sets `HardSince=nil` and the FSM re-creates rows on
demand.

### DB-15 — `parseEventTS` carries 5 fallback formats (LOW)

`events.go:189-208`. Indicates historical drift — different code paths
have written timestamps in different formats. Currently tolerated; if
ever a 6th format slips in, `LatestEvent` silently fails for those
rows.

Recommendation: write a one-shot migration that normalises all
`events.ts` to RFC3339Nano UTC, then collapse `parseEventTS` to one
format.

---

## Summary table

| ID | Severity | File:line |
|----|----------|-----------|
| DB-01 | HIGH   | state.go:139,165 ; router.go:716 |
| DB-02 | HIGH   | events.go:138 |
| DB-03 | HIGH   | users.go:176 |
| DB-04 | MEDIUM | events.go:53,96,157 ; migrations.sql:23 |
| DB-05 | HIGH   | handler.go:115-145 ; dispatcher.go:47-94 |
| DB-06 | MEDIUM | db.go:21 |
| DB-07 | MEDIUM | retention/policy.go:87-94 |
| DB-08 | LOW    | migrations.sql:40-47 ; retention/policy.go |
| DB-09 | MEDIUM | heartbeat/watcher.go:115-137 |
| DB-10 | LOW    | realert/poller.go:118 |
| DB-11 | LOW    | (codebase-wide) |
| DB-12 | LOW    | events.go:84,157 |
| DB-13 | LOW    | alerts/dispatcher.go:140 |
| DB-14 | MEDIUM | (no implementation; doc-only) |
| DB-15 | LOW    | events.go:189-208 |

---

## Quick-win batch (one PR)

Add to `migrations.sql`:
```sql
CREATE INDEX IF NOT EXISTS idx_events_user_check_ts
  ON events(user_id, check_name, ts DESC);
DROP INDEX IF EXISTS idx_events_user_ts;
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS idx_users_thread_id
  ON users(telegram_thread_id) WHERE telegram_thread_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_incident_state_hard
  ON incident_state(last_alert_at)
  WHERE current_status = 'hard' AND acked = 0;
CREATE INDEX IF NOT EXISTS idx_daily_soft_flaps_date
  ON daily_soft_flaps(date);
```

Add to `db.go:21` DSN:
`&_pragma=synchronous(normal)`

That single PR knocks out DB-01 / DB-02 / DB-03 / DB-04 / DB-06 / DB-08
without touching application code. DB-05 / DB-07 / DB-09 / DB-14 are
follow-ups requiring code or workflow changes.
