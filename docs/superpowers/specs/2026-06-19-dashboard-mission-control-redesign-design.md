# Dashboard «Mission Control» redesign — design

Date: 2026-06-19
Status: approved (design), pending implementation plan
Scope: operator dashboard only (`internal/backend/dashboard_static/*`)

## Goal

Make the operator dashboard more convenient, prettier, and clearer ("удобнее,
красивее, нагляднее"). Operator-confirmed direction:

- **Visual style:** dark "mission-control" / NOC console.
- **Depth:** full UX + visual rework (not just a repaint).
- **Primary device:** desktop (keep a sane responsive fallback, but desktop wins).

This is a front-end-only change. No Go code, no API, no data shape changes.

## Non-goals (YAGNI)

- No JS framework, no build step — stays vanilla JS + a single CSS file, served
  via `go:embed` exactly as today.
- No light/dark theme toggle — dark only (operator chose dark).
- No new backend endpoints, no new summary fields, no SPA router.
- No change to command/deploy/enrollment/revive semantics or security boundary
  (dashboard still has NO backend re-point; revive stays terminal-based).

## Current state (baseline)

- Files: `index.html` (434), `app.js` (1397), `app.css` (1090), Tabler-lite
  vendor CSS. Served from `dashboard_static.go` via `embed.FS`.
- Light theme, 3-column grid: dark sidebar · fleet table · agent-detail drawer.
- KPI cards (Agents/Online/Alerts/Deploys), search + status segments, a dense
  **7-column table** (`min-width: 1280px`, horizontal scroll), click-row →
  agent-detail drawer that stacks **8 sections** (state, Telegram, AWG Manager,
  recovery, on-router config, checks, OPKG cron, Entware cleanup).
- Modals: deploy, add-agent, edit-agent, agent-config, revive. Dark
  command-result drawer (bottom-right). 20s auto-refresh + freshness line.

### Problems this redesign fixes

1. Flat light theme, weak visual hierarchy — hard to scan at a glance.
2. 7-column table is cramped and scrolls horizontally; status is a small badge.
3. Agent drawer is an 8-section wall — high cognitive load, lots of scrolling.
4. No at-a-glance fleet health — totals are 4 separate numbers, no proportion.
5. Action buttons are tiny and noisy (7 mini-buttons per row).

## Approach

Chosen: **A — dark restyle + targeted IA fixes.** Rebuild the look on a dark
design-token system and fix the three biggest information-architecture problems
(health strip, compact table, tabbed drawer). Same data flow, same endpoints,
vanilla JS. Rejected: B (full re-architecture / SPA — too much risk for the
payoff) and C (palette swap only — under-delivers vs. the "full UX" ask).

## Design

### 1. Design tokens (dark)

A single `:root` token set drives everything. Indicative values (final values
tuned during implementation for contrast/AA):

- Background layers: `--bg #0b0f17` → `--surface #141a26` → `--surface-2 #1b2333`
  → `--surface-hover`.
- Lines: `--line #222c3d`, `--line-strong #2f3c52`.
- Text: `--text #e6edf7`, `--muted #8a99b3`, `--faint #5c6b85`.
- Status: green `#34d399`, amber `#fbbf24`, red `#f87171`, info-cyan `#38bdf8`,
  accent-blue `#3b82f6`. Each with a low-alpha "soft" background variant for
  chips/buttons on dark.
- Mono font (`ui-monospace, SFMono-Regular, Menlo, monospace`) for technical
  values: IPs, versions, URLs, MACs, command ids.
- Spacing scale, radius scale, dark-appropriate shadows (subtle, low-alpha).

The result drawer is already dark; it gets aligned to these tokens so the whole
console is one coherent palette.

### 2. Layout (3 zones kept, refined)

- **Sidebar (slim, dark):** brand, nav (`Fleet` active + room to grow),
  auth-status pinned to the bottom.
- **Topbar:** title/eyebrow on the left; actions on the right with clear
  hierarchy — one primary (`Refresh`), the rest ghost/icon. `Auto` toggle and
  the freshness indicator live here. `Backend update` shows as a prominent warn
  chip only when an update is available (unchanged logic).
- **Fleet health strip (the "нагляднее" centerpiece):** the human summary
  sentence + 4 refined stat tiles (status-accented) + a **segmented proportion
  bar** showing online / sleeping / alert / offline as relative widths — fleet
  health readable in under a second.
- **Toolbar:** search + status filters as **pills with counts**
  (e.g. `Alerts 1`, `Online 10`). Counts come from the same totals/agent list.
- **Fleet table:** compact and readable. Status rendered as a colored chip with
  a dot + a short reason line. Keep the left status rail. Inline actions trimmed
  to the 2–3 most-used (`Diagnostics`, `Recheck`/`Wake`, `Update`) plus a `⋯`
  overflow; the rest of the actions live in the drawer. Comfortable row height,
  no forced horizontal scroll on desktop widths.

### 3. Agent detail drawer → tabs

The 8-section stack becomes a **tabbed** panel. Header keeps nickname, status
chip, key meta, and the edit button. Tabs:

- **Overview** — state + active incidents + Telegram (group/topic) + "Open AWG
  Manager".
- **Maintenance** — checks (Diagnostics / Recheck / Routes / Tunnels /
  PingCheck / Direct / Via-tunnel), Restart AWG, OPKG cron, Entware cleanup.
- **Config** — on-router config (Load / Edit & restart).
- **Recovery** — Revive (AWG Manager), Edit settings, Force recheck.

Tab state is kept per render so an auto-refresh re-render doesn't reset the
operator to the first tab. Every action available today stays reachable.

### 4. Components restyled for dark

Badges, buttons (with all states — `queued` / `waiting` / `ok` / `error` —
legible on dark), modals (dark panels + overlay), inputs/selects, segmented
control, toast, result drawer. State colors use the soft-background variants so
they read clearly without glare.

### 5. Preserved 1:1 (behavioral contract)

- All endpoints, request/response handling, and `state` model in `app.js`.
- 20s auto-refresh, overlay-pauses-refresh, freshness ticker.
- Keyboard: `Esc` closes top overlay, `/` focuses search.
- `focus-visible` rings on every interactive control.
- Same HTML element ids the JS binds to (or JS updated in lockstep when ids
  move). Escaping helpers (`escapeHTML`/`escapeAttr`) stay in place — no new XSS
  surface.

## Components / responsibilities

- `app.css` — full rewrite around dark tokens. Bulk of the work. Organized:
  tokens → base/reset → layout → topbar/health → toolbar/table → drawer/tabs →
  modals → result drawer → toast → responsive.
- `index.html` — restructure topbar, add health strip, convert drawer to a tab
  container, refine toolbar; modal markup mostly reused with new classes.
- `app.js` — refactor `renderSelectedDrawer` into per-tab renderers + a tab
  switcher; add health-strip render (proportion bar + counts on filter pills);
  trim `agentRow` action strip + add overflow menu. Data/fetch logic unchanged.

## Data flow

Unchanged. `GET /v1/dashboard/summary` → `state.summary` →
`render()` → table + health strip + (if selected) tabbed drawer. Commands,
deploy, enrollment, edit, config, revive all hit the same endpoints with the
same payloads.

## Error handling

Unchanged paths: 401 → redirect to `/dashboard/login`; per-button error states;
result drawer shows command errors / timeouts; `renderError` on summary failure.
Redesign only restyles these states for dark — no new error logic.

## Testing / verification

- No Go logic changes; existing Go tests must still pass (`go test ./...` for the
  backend package; the embed still includes the three files + vendor).
- Manual verification with Playwright against a running backend (or a static
  fixture of `summary`): load dashboard, confirm health strip + counts,
  open/close every modal, switch every drawer tab, run a command and watch the
  result drawer, toggle auto-refresh, exercise `/` and `Esc`, check focus rings,
  and sanity-check a narrow viewport doesn't break.
- Visual check that all four status colors and all button states are legible on
  the dark background.

## Risks

- Largest risk is `app.js` drawer refactor (tabs) silently dropping an action or
  breaking an element id the handlers rely on. Mitigation: keep the delegated
  `document.body` click handler + `data-*` attribute contract intact; enumerate
  every existing action and confirm it appears in exactly one tab.
- Contrast/AA on dark — tune token values, verify status colors.
