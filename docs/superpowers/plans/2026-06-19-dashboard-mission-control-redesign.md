# Dashboard «Mission Control» Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repaint and restructure the operator dashboard into a dark "mission-control" console — clearer at a glance, less cluttered, prettier — without changing any backend, API, or behavior.

**Architecture:** Front-end only. Three embedded static files (`internal/backend/dashboard_static/index.html`, `app.js`, `app.css`) are reworked. A dark design-token system drives all styling; the agent-detail drawer becomes tabbed; a fleet-health strip and counted filter pills are added; the fleet table is compacted with a trimmed action strip + overflow. All fetch/state/command logic in `app.js` is preserved.

**Tech Stack:** Vanilla JS (IIFE, no framework, no build step), single hand-written CSS file, Tabler-lite vendor CSS (kept), Go `embed.FS` (`dashboard_static.go`, unchanged). Verification: `go build ./...`, `node --check`, and a Playwright/browser pass.

## Global Constraints

- Front-end only: **no changes to any `.go` file**, no new endpoints, no new `summary` fields, no payload changes. (Verbatim from spec non-goals.)
- Stays vanilla JS + single CSS file, served via `go:embed` — no framework, no build step.
- Dark theme only — no light/dark toggle.
- Preserve 1:1: all endpoints + `state` model; 20s auto-refresh (`AUTO_REFRESH_MS = 20000`); overlay-pauses-refresh (`overlayOpen()`); freshness ticker; keyboard (`Esc` closes top overlay, `/` focuses search); `focus-visible` rings on every interactive control; `escapeHTML`/`escapeAttr` on all interpolated values (no new XSS surface).
- Security boundary unchanged: dashboard has NO backend re-point; revive stays AWG-Manager-terminal based.
- Every action reachable today must remain reachable after the redesign (enumerated in Task 6).
- The delegated click handler contract stays: actions are dispatched from `document.body` click via `data-command` / `data-maint` / `data-deploy` / `data-update-latest` / `data-edit-agent` / `data-agent-config` / `data-revive` / `data-row-agent` attributes. Renaming any of these requires updating the handler in lockstep.
- Element ids the JS binds to (the `els` map in `app.js:18-102`) must keep matching `index.html`. If an id moves, update both in the same task.

---

## File Structure

- `internal/backend/dashboard_static/app.css` — full rewrite around dark tokens. Section order: tokens → base/reset → layout shell → sidebar → topbar → health strip → toolbar/pills → table → drawer/tabs → modals/forms → result drawer → toast → utilities → responsive → focus.
- `internal/backend/dashboard_static/index.html` — restructure topbar actions, add fleet-health strip, convert toolbar segments to counted pills, convert the agent drawer into a tab container; modal markup reused with refreshed classes.
- `internal/backend/dashboard_static/app.js` — add `renderHealthStrip()`, `filterCounts()`; refactor `renderSelectedDrawer()` into a header + per-tab renderers + `state.drawerTab`; trim `agentRow()` action strip and add an overflow menu; everything else unchanged.

No Go files change. `dashboard_static.go` already embeds `dashboard_static/*` and `dashboard_static/vendor/*`, so new/edited files inside that directory are picked up with no edit.

---

### Task 1: Dark design tokens + base/reset

**Files:**
- Modify: `internal/backend/dashboard_static/app.css:1-52` (tokens + base) — and establish the new token set the rest of the rewrite consumes.

**Interfaces:**
- Produces: CSS custom properties consumed by every later task — `--bg`, `--surface`, `--surface-2`, `--surface-hover`, `--line`, `--line-strong`, `--text`, `--muted`, `--faint`, `--accent`, `--green`, `--amber`, `--red`, `--info`, the `*-soft` background variants, `--radius`, `--radius-lg`, `--shadow`, `--mono`.

- [ ] **Step 1: Replace the `:root` block and base styles with the dark token set**

Replace `app.css:1-52` (`:root` through the `.app-shell` opener) tokens with:

```css
:root {
  --bg: #0b0f17;
  --surface: #141a26;
  --surface-2: #1b2333;
  --surface-hover: #202a3d;
  --line: #222c3d;
  --line-strong: #2f3c52;
  --text: #e6edf7;
  --muted: #8a99b3;
  --faint: #5c6b85;
  --accent: #3b82f6;
  --accent-soft: rgba(59, 130, 246, 0.16);
  --green: #34d399;
  --green-soft: rgba(52, 211, 153, 0.14);
  --amber: #fbbf24;
  --amber-soft: rgba(251, 191, 36, 0.14);
  --red: #f87171;
  --red-soft: rgba(248, 113, 113, 0.14);
  --info: #38bdf8;
  --info-soft: rgba(56, 189, 248, 0.14);
  --radius: 10px;
  --radius-lg: 14px;
  --shadow: 0 18px 50px rgba(0, 0, 0, 0.45);
  --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

* { box-sizing: border-box; }

body {
  min-width: 320px;
  margin: 0;
  background: radial-gradient(1200px 600px at 70% -10%, rgba(59, 130, 246, 0.10), transparent 60%), var(--bg);
  color: var(--text);
  font-family: "Segoe UI", system-ui, -apple-system, BlinkMacSystemFont, sans-serif;
  font-size: 14px;
  line-height: 1.45;
}
```

- [ ] **Step 2: Verify embed still builds**

Run: `go build ./...`
Expected: builds clean (no output, exit 0). Confirms the embedded file is still valid and present.

- [ ] **Step 3: Verify the CSS file has no stray light-theme `--bg-soft`/`--bg-ink` references left that other tasks depend on**

Run: `grep -n "var(--bg-soft)\|var(--bg-ink)\|var(--panel)\|var(--blue)\|var(--cyan)" internal/backend/dashboard_static/app.css`
Expected: prints the remaining usages (later tasks will migrate them). This is a worklist, not a failure — note the count so later tasks can confirm it reaches zero.

- [ ] **Step 4: Commit**

```bash
git add internal/backend/dashboard_static/app.css
git commit -m "feat(dashboard): dark mission-control design tokens + base"
```

---

### Task 2: Sidebar + topbar restyle

**Files:**
- Modify: `internal/backend/dashboard_static/index.html:13-47` (sidebar + topbar markup)
- Modify: `internal/backend/dashboard_static/app.css` (sidebar, topbar, button, input sections)

**Interfaces:**
- Consumes: tokens from Task 1.
- Produces: restyled `.sidebar`, `.brand`, `.nav-item`, `.sidebar-status`, `.topbar`, `.topbar-actions`, `.btn`/`.btn-primary`/`.btn-secondary`/`.btn-ghost`/`.btn-warn`, `input`, `select`, `.icon-btn`. No id changes (all ids in `els` preserved).

- [ ] **Step 1: Restyle sidebar + topbar in CSS**

Migrate the `.sidebar`, `.brand*`, `.nav-item*`, `.sidebar-status`, `.workspace`, `.topbar*`, `.btn*`, `input`, `select`, `.icon-btn` rules (`app.css:54-328` region) onto dark tokens:
- `.sidebar` background `var(--surface)`, right border `1px solid var(--line)`.
- `.nav-item` default `color: var(--muted)`; `.active`/`:hover` → `background: var(--surface-hover); color: var(--text)`.
- `.brand-mark` `background: var(--surface-2); color: var(--accent); border-color: var(--line-strong)`.
- Buttons: base `.btn` → `background: var(--surface-2); color: var(--text); border: 1px solid var(--line)`. `.btn-primary` → `background: var(--accent); border-color: var(--accent); color: #fff`. `.btn-secondary` → `background: var(--info-soft); border-color: rgba(56,189,248,0.4); color: var(--info)`. `.btn-ghost` → `background: transparent; color: var(--muted)`. `.btn-warn` → `background: var(--amber-soft); border-color: rgba(251,191,36,0.45); color: var(--amber)`. Keep the existing `:hover` lift and `[data-state]` variants but recolor onto `*-soft` backgrounds.
- `input, select` → `background: var(--surface-2); border: 1px solid var(--line); color: var(--text)`; `:focus` → `border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft)`.
- `.icon-btn` → `background: var(--surface-2); color: var(--text)`.

- [ ] **Step 2: Tighten topbar action hierarchy in HTML**

In `index.html:40-46`, keep all five buttons and their ids (`backendUpdateBtn`, `addAgentBtn`, `autoRefreshBtn`, `refreshBtn`, `logoutBtn`) but reorder for hierarchy: `addAgentBtn` (secondary), `autoRefreshBtn` (ghost), `logoutBtn` (ghost icon-leaning), `backendUpdateBtn` (warn, stays `hidden`), `refreshBtn` (primary, last/right). Do not rename ids or `data-*`.

- [ ] **Step 3: JS syntax + build check**

Run: `node --check internal/backend/dashboard_static/app.js && go build ./...`
Expected: no output, exit 0. (No JS changed in this task; this confirms HTML edits didn't require JS changes and the embed still builds.)

- [ ] **Step 4: Commit**

```bash
git add internal/backend/dashboard_static/index.html internal/backend/dashboard_static/app.css
git commit -m "feat(dashboard): dark sidebar + topbar, clearer action hierarchy"
```

---

### Task 3: Fleet-health strip

**Files:**
- Modify: `internal/backend/dashboard_static/index.html:49-70` (replace `.kpi-grid` section with the health strip)
- Modify: `internal/backend/dashboard_static/app.css` (add `.health-strip`, `.health-summary`, `.stat-tile`, `.health-bar` rules; migrate old `.kpi-*`)
- Modify: `internal/backend/dashboard_static/app.js` (add `renderHealthStrip()`, call it from `render()`)

**Interfaces:**
- Consumes: tokens (Task 1); `state.summary.totals` (`agents/online/sleeping/offline/alerts/pending_deploys`) and `summarySentence()` (existing, `app.js:286-293`).
- Produces: `renderHealthStrip(summary)` updating the strip; element ids `kpiAgents`, `kpiOnline`, `kpiAlerts`, `kpiDeploys`, `kpiVersion`, `kpiOnlineText`, `kpiAlertsText`, `kpiDeploysText`, `summaryText`, `backendUpdateBtn`/`backendUpdateText` all preserved so existing `render()` lines keep working; new ids `healthBar` (proportion bar container).

- [ ] **Step 1: Replace KPI markup with the health strip in HTML**

Replace `index.html:49-70` (the `<section class="kpi-grid">`) with a `<section class="health-strip">` that contains: the existing `summaryText`/freshness moved into a `.health-summary` block (keep ids `summaryText`, `freshness`, `freshnessDot`), a `<div class="health-bar" id="healthBar"></div>` proportion bar, and four `.stat-tile` cards reusing the SAME ids (`kpiAgents`+`kpiVersion`, `kpiOnline`+`kpiOnlineText`, `kpiAlerts`+`kpiAlertsText`, `kpiDeploys`+`kpiDeploysText`) with status accent classes (`.stat-tile.good/.bad/.warn`). Remove the freshness `<p>` from the topbar if it was duplicated; it now lives in the strip.

- [ ] **Step 2: Add `renderHealthStrip()` and the proportion bar in JS**

Add to `app.js` (near `render`):

```js
function renderHealthStrip(summary) {
  const t = summary.totals || {};
  const segs = [
    { n: t.online || 0, cls: "seg-online", label: "online" },
    { n: t.sleeping || 0, cls: "seg-sleeping", label: "sleeping" },
    { n: t.alerts || 0, cls: "seg-alert", label: "alert" },
    { n: t.offline || 0, cls: "seg-offline", label: "offline" }
  ].filter((s) => s.n > 0);
  const total = segs.reduce((sum, s) => sum + s.n, 0) || 1;
  const bar = document.getElementById("healthBar");
  if (bar) {
    bar.innerHTML = segs.map((s) =>
      `<span class="health-seg ${s.cls}" style="width:${(s.n / total) * 100}%" title="${escapeAttr(s.n + " " + s.label)}"></span>`
    ).join("") || `<span class="health-seg seg-offline" style="width:100%"></span>`;
  }
}
```

Call `renderHealthStrip(summary)` inside `render()` right after the existing KPI text assignments (after `app.js:251`).

- [ ] **Step 3: Style the strip + bar in CSS**

Add rules: `.health-strip` grid (summary | tiles); `.stat-tile` → `background: var(--surface); border: 1px solid var(--line); border-radius: var(--radius-lg)`; status accent via left/top border (`.good` green, `.bad` red, `.warn` amber); big number `.stat-tile strong` mono-free, `font-size: 32px`. `.health-bar` → flex, `height: 10px; border-radius: 999px; overflow: hidden; background: var(--surface-2)`. `.health-seg.seg-online{background:var(--green)} .seg-sleeping{background:var(--amber)} .seg-alert{background:var(--red)} .seg-offline{background:var(--faint)}`.

- [ ] **Step 4: Verify**

Run: `node --check internal/backend/dashboard_static/app.js && go build ./...`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/dashboard_static/index.html internal/backend/dashboard_static/app.css internal/backend/dashboard_static/app.js
git commit -m "feat(dashboard): fleet-health strip with proportion bar"
```

---

### Task 4: Counted filter pills

**Files:**
- Modify: `internal/backend/dashboard_static/index.html:72-84` (toolbar/segments)
- Modify: `internal/backend/dashboard_static/app.css` (`.segmented`/`.segment` → pills)
- Modify: `internal/backend/dashboard_static/app.js` (add `filterCounts()`, update counts in `render()`)

**Interfaces:**
- Consumes: `state.summary.agents`, `state.filter`.
- Produces: `filterCounts(summary)` → `{all, alert, online, sleeping, offline}`; per-segment count `<span>` ids `cntAll`, `cntAlert`, `cntOnline`, `cntSleeping`, `cntOffline`. The existing `.segment` click handler (`app.js:1306-1313`) and `data-filter` values stay unchanged.

- [ ] **Step 1: Add count spans to each segment in HTML**

In `index.html:77-83`, inside each `<button class="segment" data-filter="...">` append `<span class="pill-count" id="cnt<Name>">0</span>` (e.g. `cntAll`, `cntAlert`, `cntOnline`, `cntSleeping`, `cntOffline`). Keep `data-filter` and the `active` class on `all`.

- [ ] **Step 2: Add `filterCounts()` and wire it in JS**

Add:

```js
function filterCounts(summary) {
  const agents = summary.agents || [];
  const by = (s) => agents.filter((a) => a.status === s).length;
  return { all: agents.length, alert: by("alert"), online: by("online"), sleeping: by("sleeping"), offline: by("offline") };
}
```

In `render()` after `renderHealthStrip(...)`, set each count's `textContent`:

```js
const counts = filterCounts(summary);
[["cntAll","all"],["cntAlert","alert"],["cntOnline","online"],["cntSleeping","sleeping"],["cntOffline","offline"]]
  .forEach(([id, key]) => { const el = document.getElementById(id); if (el) el.textContent = counts[key]; });
```

- [ ] **Step 3: Style pills in CSS**

Migrate `.segmented`/`.segment` onto tokens: container `background: var(--surface); border: 1px solid var(--line)`; `.segment` → `color: var(--muted)`; `.segment.active` → `background: var(--accent-soft); color: var(--text)`. `.pill-count` → small rounded `background: var(--surface-2); color: var(--muted); padding: 0 6px; margin-left: 6px; font-size: 11px`; on `.segment.active .pill-count` use `color: var(--text)`.

- [ ] **Step 4: Verify**

Run: `node --check internal/backend/dashboard_static/app.js && go build ./...`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/dashboard_static/index.html internal/backend/dashboard_static/app.css internal/backend/dashboard_static/app.js
git commit -m "feat(dashboard): status filter pills with live counts"
```

---

### Task 5: Compact table + trimmed action strip + overflow

**Files:**
- Modify: `internal/backend/dashboard_static/app.css` (table section `app.css:424-622`)
- Modify: `internal/backend/dashboard_static/app.js:309-346` (`agentRow`)
- Modify: `internal/backend/dashboard_static/index.html:86-105` (table head if columns change)

**Interfaces:**
- Consumes: `buttonState()`, `latestDeployButton()`, `statusBadge()`, `statusText()`, `recheckShortLabel()`, `recheckTitle()` (all existing).
- Produces: a slimmer `agentRow(agent)` whose inline actions are `Diagnostics`, `Recheck/Wake`, `Update` + a `⋯` overflow button toggling a `.row-overflow` menu holding the remaining quick actions (`route_status`, `tunnels_status`, `awgmgr` restart, custom `Version`). Overflow uses existing `data-command`/`data-maint`/`data-deploy` so the body click handler already dispatches them. New: `data-overflow="<nickname>"` toggles the menu.

- [ ] **Step 1: Restyle the table for dark + comfortable density in CSS**

Migrate `.table-wrap`, `.fleet-table`, `th/td`, hover, `.agent-cell::before` status rail, `.badge*`, `.cell-note`, `.mini-btn*` onto tokens. `.table-wrap` → `background: var(--surface); border: 1px solid var(--line)`. `th` → `color: var(--muted); background: var(--surface-2)`. `tbody tr:hover` → `background: var(--surface-hover)`. Status rail colors use `--green/--amber/--red`. Badges use `*-soft` backgrounds (`.badge-success`→green-soft/green text, `.badge-danger`→red-soft, `.badge-warning`→amber-soft, `.badge-info`→info-soft, `.badge-muted`→`var(--surface-2)`/muted). Reduce `min-width` on `.fleet-table` from `1280px` to fit desktop without horizontal scroll (target ≤ `1040px`); narrow per-column widths accordingly. Use mono font for `.agent-meta` IP/iface text.

- [ ] **Step 2: Add the overflow menu CSS**

```css
.row-actions { display: flex; gap: 6px; align-items: center; }
.row-overflow { position: relative; }
.row-overflow-menu { position: absolute; right: 0; top: calc(100% + 4px); z-index: 5; display: none; flex-direction: column; gap: 4px; min-width: 160px; padding: 6px; background: var(--surface-2); border: 1px solid var(--line); border-radius: var(--radius); box-shadow: var(--shadow); }
.row-overflow.open .row-overflow-menu { display: flex; }
```

- [ ] **Step 2b: Migrate the action `[data-state]` button-state colors onto dark soft backgrounds**

In `app.css:274-301`, recolor `[data-state="queued"]` → info-soft, `="waiting"` → amber-soft, `="ok"` → green-soft, `="error"` → red-soft (background + matching text), so command progress is legible on dark.

- [ ] **Step 3: Rewrite `agentRow` action column in JS**

Replace the `.action-strip` block in `agentRow` (`app.js:333-343`) with a trimmed primary set + overflow:

```js
<td>
  <div class="row-actions">
    <button class="mini-btn" type="button" title="Run diagnostics" data-state="${buttonState(agent.nickname, "diag_now")}" data-command="diag_now" data-agent="${escapeAttr(agent.nickname)}">Diag</button>
    <button class="mini-btn" type="button" title="${escapeAttr(recheckTitle(agent))}" data-state="${buttonState(agent.nickname, "force_recheck")}" data-command="force_recheck" data-agent="${escapeAttr(agent.nickname)}">${escapeHTML(recheckShortLabel(agent))}</button>
    ${latestDeployButton(agent, "mini-btn primary", "Update")}
    <div class="row-overflow">
      <button class="mini-btn" type="button" title="More actions" data-overflow="${escapeAttr(agent.nickname)}">⋯</button>
      <div class="row-overflow-menu">
        <button class="mini-btn" type="button" data-state="${buttonState(agent.nickname, "route_status")}" data-command="route_status" data-agent="${escapeAttr(agent.nickname)}">Routes</button>
        <button class="mini-btn" type="button" data-state="${buttonState(agent.nickname, "tunnels_status")}" data-command="tunnels_status" data-agent="${escapeAttr(agent.nickname)}">Tunnels</button>
        <button class="mini-btn danger" type="button" data-state="${buttonState(agent.nickname, "awgmgr")}" data-maint="awgmgr" data-agent="${escapeAttr(agent.nickname)}">Restart AWG</button>
        <button class="mini-btn" type="button" data-deploy="${escapeAttr(agent.nickname)}">Version</button>
      </div>
    </div>
  </div>
</td>
```

- [ ] **Step 4: Wire overflow toggle into the existing body click handler**

In the `document.body` click handler (`app.js:1314-1336`), before the row-select branch, add: if `button.dataset.overflow`, toggle `.open` on the button's `.row-overflow` parent and `return` (also close any other open `.row-overflow.open`). Ensure clicking elsewhere closes open menus (add a top-level branch: if the click is outside `.row-overflow`, remove `.open` from all). Keep `event.target.closest("[data-row-agent]")` row-select working when the click is not on a button.

- [ ] **Step 5: Verify**

Run: `node --check internal/backend/dashboard_static/app.js && go build ./...`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/dashboard_static/index.html internal/backend/dashboard_static/app.css internal/backend/dashboard_static/app.js
git commit -m "feat(dashboard): compact dark fleet table + action overflow"
```

---

### Task 6: Tabbed agent drawer

**Files:**
- Modify: `internal/backend/dashboard_static/app.js:411-544` (`renderSelectedDrawer`)
- Modify: `internal/backend/dashboard_static/app.css` (drawer + new `.drawer-tabs` rules)
- Modify: `internal/backend/dashboard_static/app.js:1-16` (add `drawerTab` to `state`)

**Interfaces:**
- Consumes: all existing drawer helpers — `statusLongText`, `drawerKV`, `drawerCommandButton`, `latestDeployButton`, `groupLabel`, `topicLabel`, `formatLastSeen`, `recheckLongLabel`, `shortURL`, `opkgCronBadge`, `entwareCleanBadge`, `cronScheduleToTime`, `formatKB`, and the `state.opkgCron/entwareClean/versions/agentConfig` maps.
- Produces: `state.drawerTab` (default `"overview"`); `renderSelectedDrawer()` renders a header + a `.drawer-tabs` nav + one active tab body. Tab switch is handled in the body click handler via `data-drawer-tab`.

**Action inventory — every action must land in exactly one tab (parity check):**
- Overview: status text, incidents, Telegram group/topic, Open AWG Manager link, `version_audit` (Versions), Edit (icon).
- Maintenance: `diag_now`, `force_recheck`, `route_status`, `tunnels_status`, `pingcheck_status`, `check_direct`, `check_via_tunnel`, `awgmgr` (Restart AWG), `latestDeployButton`, custom `data-deploy` (Version), OPKG cron (`opkg_cron_status/install/logs/remove` + `opkgCronSchedule` input), Entware cleanup (`entware_clean_status/install/run/logs/remove` + `entwareCleanSchedule` input).
- Config: `agent_config_get` (Load config) + `data-agent-config` (Edit & restart).
- Recovery: `data-revive` (Revive), `data-edit-agent` (Edit settings), `force_recheck` (Force recheck).

- [ ] **Step 1: Add `drawerTab` to state**

In the `state` object (`app.js:2-16`) add `drawerTab: "overview"`.

- [ ] **Step 2: Refactor `renderSelectedDrawer` into header + tabs**

Rewrite `renderSelectedDrawer` so it builds: (a) the empty state (unchanged copy), (b) a header (`eyebrow`, `<h2>` nickname, status note, edit icon — same markup as `app.js:427-434`), (c) a `<nav class="drawer-tabs">` with four buttons `data-drawer-tab="overview|maintenance|config|recovery"` (the active one gets class `active` from `state.drawerTab`), and (d) the body of only the active tab. Move the existing section markup into four helper functions `drawerTabOverview(selected)`, `drawerTabMaintenance(selected)`, `drawerTabConfig(selected)`, `drawerTabRecovery(selected)` using the action inventory above — reuse the existing `<section class="drawer-section">` blocks verbatim, just distributed across tabs. Select which body to render with a switch on `state.drawerTab`.

- [ ] **Step 3: Handle tab switching in the body click handler**

In the `document.body` click handler, add a branch: if `button.dataset.drawerTab`, set `state.drawerTab = button.dataset.drawerTab; renderSelectedDrawer(); return;`. Because re-render reads `state.drawerTab`, the 20s auto-refresh re-render keeps the operator on their current tab.

- [ ] **Step 4: Reset tab on row select**

In the row-select branch (`app.js:1337-1341`), when `state.selected` changes to a new agent, set `state.drawerTab = "overview"` before `renderSelectedDrawer()` so a freshly opened agent starts on Overview. (Do not reset if the same row is re-clicked.)

- [ ] **Step 5: Style the tabs in CSS**

Migrate `.agent-drawer`, `.drawer-*`, `.drawer-section`, `.drawer-kv`, `.action-btn*` onto dark tokens. Add:

```css
.drawer-tabs { display: flex; gap: 4px; margin: 4px 0 14px; padding: 4px; background: var(--surface-2); border-radius: var(--radius); }
.drawer-tabs button { flex: 1; min-height: 32px; border: 0; border-radius: 7px; background: transparent; color: var(--muted); font-weight: 800; cursor: pointer; }
.drawer-tabs button.active { background: var(--accent-soft); color: var(--text); }
```
`.agent-drawer` → `background: var(--surface)`; `.drawer-section` → `background: var(--surface-2); border: 1px solid var(--line)`; `.drawer-kv strong` → `color: var(--text)`.

- [ ] **Step 6: Verify**

Run: `node --check internal/backend/dashboard_static/app.js && go build ./...`
Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add internal/backend/dashboard_static/app.js internal/backend/dashboard_static/app.css
git commit -m "feat(dashboard): tabbed agent drawer (overview/maintenance/config/recovery)"
```

---

### Task 7: Modals, forms, result drawer, toast — dark finish

**Files:**
- Modify: `internal/backend/dashboard_static/app.css` (modal/form/result-drawer/toast sections `app.css:694-928` + `1011-1090`)

**Interfaces:**
- Consumes: tokens (Task 1). No JS or HTML changes; all modal ids/markup reused.

- [ ] **Step 1: Restyle overlays + forms in CSS**

Migrate `.modal`, `.modal-panel`, `.modal-head`, `.modal-actions`, `.form-field span`, `.form-section-title`, `.modal-note`, `.modal-note code`, `.form-error`, `.revive-advanced`, `.drawer-section-recovery`, `.action-btn.warn` onto tokens: overlay `background: rgba(2,6,16,0.66)`; `.modal-panel` → `background: var(--surface); border: 1px solid var(--line); box-shadow: var(--shadow)`; `.form-section-title` → `color: var(--muted)`; `.modal-note code` → `background: var(--surface-2)`; recovery section → `border: 1px solid rgba(251,191,36,0.3); background: var(--amber-soft)`.

- [ ] **Step 2: Align the result drawer + toast to the same palette**

The result drawer is already dark — retune to tokens: `.result-drawer` → `background: var(--surface); border: 1px solid var(--line)`; `.result-section` → `background: var(--surface-2); border-color: var(--line)`; `pre`/`.raw-output` → `background: #0a0e16`; `.result-grid span` → `color: var(--muted)`. `.toast` → `background: var(--surface-2); border: 1px solid var(--line-strong); color: var(--text)`.

- [ ] **Step 3: Confirm no light-theme variables remain**

Run: `grep -n "var(--bg-soft)\|var(--bg-ink)\|var(--panel)\|var(--blue)\|var(--cyan)\|var(--panel-ink)" internal/backend/dashboard_static/app.css`
Expected: no matches (exit 1). Every old token migrated.

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/dashboard_static/app.css
git commit -m "feat(dashboard): dark modals, forms, result drawer, toast"
```

---

### Task 8: Responsive + focus + full verification

**Files:**
- Modify: `internal/backend/dashboard_static/app.css` (responsive `@media` blocks `app.css:934-1009` + focus `1079-1090`)
- Verify only: all three files via a browser pass.

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Refit responsive rules for the new layout**

Update the `@media (max-width: 1180px)` and `(max-width: 780px)` blocks so the new `.health-strip`, `.drawer-tabs`, `.row-actions`, and pills collapse gracefully (drawer stacks below table on narrow; stat tiles 2-up then 1-up; topbar actions wrap; overflow menu stays on-screen). Desktop-first — these are fallbacks, not the priority.

- [ ] **Step 2: Confirm focus-visible covers new controls**

Extend the `:focus-visible` selector list (`app.css:1080-1089`) to include `.drawer-tabs button` and `[data-overflow]` (they already match `.mini-btn`/button if classed; add explicitly if not). Outline uses `var(--accent)`.

- [ ] **Step 3: JS syntax + build**

Run: `node --check internal/backend/dashboard_static/app.js && go build ./...`
Expected: exit 0.

- [ ] **Step 4: Run the backend Go tests for the dashboard package**

Run: `go test ./internal/backend/...`
Expected: PASS (no Go logic changed; this guards against an accidental Go edit / broken embed).

- [ ] **Step 5: Browser verification pass (Playwright MCP or manual)**

Against a running backend dashboard (operator's instance or a local run), verify:
- Loads in dark theme; health strip shows totals + proportion bar; filter pills show counts.
- Each status filter narrows the table; search filters; `/` focuses search; `Esc` closes overlays.
- Row overflow `⋯` opens/closes; clicking outside closes it.
- Open every modal (deploy, add-agent, edit-agent, agent-config, revive) and close via button + `Esc`.
- Select an agent → drawer opens on Overview; switch all four tabs; confirm every action from the Task 6 inventory is present and clickable.
- Queue a command (e.g. Diagnostics) → result drawer styled dark, button state transitions queued→waiting→ok/error are legible.
- Auto-refresh does not reset the active drawer tab or yank an open modal.
- All four status colors and all button states are readable on dark; focus rings visible when tabbing.
- Narrow the viewport once — nothing critical overflows or becomes unreachable.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/dashboard_static/app.css
git commit -m "feat(dashboard): responsive + focus polish for mission-control layout"
```

---

## Self-Review

**Spec coverage:**
- Dark tokens → Task 1. ✓
- Layout: sidebar/topbar → Task 2; health strip → Task 3; counted pills → Task 4; compact table + trimmed actions/overflow → Task 5. ✓
- Tabbed drawer (overview/maintenance/config/recovery) → Task 6 with explicit action-parity inventory. ✓
- Components restyled (badges/buttons/states/modals/inputs/segments/result-drawer/toast) → Tasks 2, 5 (button states), 7. ✓
- Preserved 1:1 (endpoints, auto-refresh, overlay-pause, freshness, keyboard, focus-visible, escaping) → Global Constraints + Tasks 5/6 keep the `data-*` + id contract; Task 8 verifies. ✓
- Mono font for technical values → Tasks 1 (`--mono`), 5 (agent-meta). ✓
- Responsive desktop-first + focus → Task 8. ✓
- No Go/API change → Global Constraints; verified by `go build`/`go test` each task. ✓

**Placeholder scan:** No "TBD/TODO/handle edge cases/similar to Task N". CSS styling steps name exact selectors + token values; JS steps give full function bodies; HTML steps name exact ids/`data-*` to preserve. Final pixel tuning is explicitly allowed but every selector + rule is specified. ✓

**Type/contract consistency:** `state.drawerTab` defined in Task 6 Step 1, used Steps 2–4. `renderHealthStrip`/`filterCounts` defined and called in their tasks. `data-overflow`/`data-drawer-tab` introduced in Tasks 5/6 and handled in the same task's click-handler step. Element ids reused from the existing `els` map are kept (Tasks 2–4 call this out). ✓

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-19-dashboard-mission-control-redesign.md`.
