# Dashboard frontend audit + cron-install fix + edit/resurrect — design

Date: 2026-06-18
Status: approved-by-directive (autonomous `/goal` session; user asked to "do it A→Z, don't pause")

## Goal

Three things, in one pass:

1. **Frontend audit + beautify** — make the operator dashboard
   (`internal/backend/dashboard_static/`) nicer to use and to look at.
2. **Fix the two cron scripts that don't install cron** — `OpkgCronManager`
   and `EntwareCleanManager` write a managed script + a root-crontab block and
   then `S10cron start`, but they assume the Entware `cron` package
   (`crond` + `/opt/etc/init.d/S10cron`) already exists. On a fresh router it
   doesn't, so the schedule silently never runs. They must ensure cron is
   installed first.
3. **New (mid-session ask):** when a router's domain / settings change, let the
   operator edit them from the frontend and "resurrect" the agent — without SSH.

## Findings (audit)

Frontend (`index.html` 252L, `app.js` 1005L, `app.css` 1009L), vanilla JS SPA
served from Go `embed`, Tabler-lite CSS:

- **No edit-agent path.** Agent metadata (awgm_url = router domain, ssh_host,
  arch, ring, deploy_mode, expected_mac, telegram topic) is set only at
  enrollment. The drawer literally tells the operator to use the CLI
  (`awgm-url-patch` / deploy sync). This is the gap the new ask targets.
- **No auto-refresh / freshness indicator.** A live fleet console requires manual
  Refresh clicks; no "updated Ns ago".
- **No keyboard affordances.** No Esc-to-close, no `/`-to-search.
- **Redundant sidebar.** Nav is just "Fleet" + a duplicate "Refresh".
- **Cron drawer run-time is hardcoded** (04:30 / 05:15) and ignores the
  already-installed schedule.
- Minor: loading/empty states are plain; focus states thin.

Backend primitives that already exist and that the new feature can reuse with
**no agent redeploy**:

- `wizardPutAgentHandler` (`PUT /v1/wizard/agents/{nickname}`) updates deploy
  metadata via `UsersRepo.UpdateDeployInfo`. The dashboard has no equivalent.
- Agent action `update_backend_url`: health-checks the new URL, rewrites
  `config.yaml` `backend.url`, then `S99wg-monitor restart` — i.e. it **is** the
  "resurrect" primitive (re-point + self-restart). Already in the wizard command
  allowlist + the deploy CLI's `actionMigrateBackendURL`, but **not** in
  `dashboardCommandAllowlist`.
- Agent actions `force_recheck` (wake) and `service_restart name=awgmgr` already
  reachable from the dashboard.

`UpdateDeployInfo` semantics matter: kind/thread/ssh_host/ssh_port/ssh_user/arch
are coalesced (empty = keep), but ring/deploy_mode/awgm_url/awgm_auth/
expected_mac/pending*/last_deploy are **overwritten** (empty → NULL). So a naive
edit form would wipe fields. The dashboard edit handler must **merge with the
current row** first (same approach as `dashboardDeployInfoFromEnrollmentReq`).

## Design

### Part A — ensure cron is installed (agent-side)

New shared helper `internal/agent/actions/cron_install.go`:

```
ensureCronInstalled(ctx, exec ExecFunc) (installedNow bool, err error)
```

- Detect via exec (injectable for tests):
  `sh -c "test -x /opt/etc/init.d/S10cron"` → rc 0 ⇒ present, return (false,nil).
- If absent: `opkg install cron`; on failure retry once after `opkg update`.
- Return a typed error on persistent failure so the UI surfaces it.

Call it inside both `Install` methods **after** the `/opt` free-space check and
**before** writing the script / editing crontab (crontab + crond must exist
first). Existing `S10cron start` stays. No wire-type change.

Tests: extend the existing exact-match `Exec` fakes with the detection command
(present path); add new cases for absent→`opkg install cron`, and
install-failure→`opkg update`→retry.

### Part C — edit + resurrect (backend)

- **`dashboardPutAgentHandler`** → register `PUT /v1/dashboard/agents/{nickname}`.
  Body = subset of deploy fields. Handler loads the current row, fills blanks
  from it (merge-safe), validates arch + awgm_url, calls `UpdateDeployInfo`.
  404 if unknown nickname. Reuses the wizard PUT request shape.
- **Allowlist:** add `"update_backend_url": true` to `dashboardCommandAllowlist`
  (`sanitizeWizardCommandArgs` already validates the `url` arg: https-only,
  public host). `force_recheck` is already allowlisted.

No new agent action ⇒ works against the **already-deployed** fleet.

### Part B — frontend

- **Edit agent**: drawer "Edit settings" button opens a modal (reuse/extend the
  add-agent form in edit mode, no token). PUTs to the new endpoint, then refresh.
- **Resurrect group** in the drawer: "Re-point backend & restart"
  (`update_backend_url`, with a typed URL + confirm), plus existing Wake/recheck
  and Restart AWG, grouped under a clear "Resurrect / recovery" heading.
- **Auto-refresh**: toggle (default on, 20 s) + "updated Ns ago" in the topbar.
  Pauses while a modal/drawer is open so it doesn't yank the UI.
- **Keyboard**: Esc closes the top-most overlay; `/` focuses search.
- **Polish**: prefill cron run-time from installed schedule; cleaner
  loading/empty/focus states; tidy sidebar; keep the existing light Tabler look.

## Out of scope

- New agent-side "restart agent service" action (would need a fleet redeploy;
  `update_backend_url` already restarts).
- Changing the backend's own public domain config.
- Wire-type / DB-schema changes.

## Verification

- `go build ./...` and `go test ./internal/agent/actions/... ./internal/backend/...`.
- Manual UI sanity of the edited static (served locally) where feasible.
- Cron-install fix ships in the next agent release (agent-side code).
