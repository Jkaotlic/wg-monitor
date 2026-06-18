# Editing a router from the dashboard

Date: 2026-06-18

When a router's domain or deploy settings change, you no longer need the deploy
CLI / `awgm-url-patch` to fix the fleet record — the operator dashboard can edit
an enrolled agent in place.

## Edit settings

Open an agent (click its row), then **Edit settings** (pencil icon in the drawer
header, or the button in the **Resurrect / recovery** section). The modal edits:

- **AWG Manager URL** — the router domain used by the drawer's *Open AWG Manager*
  link. Must be an absolute `http(s)` URL.
- **AWG auth**, **SSH host / port / user**, **deploy mode**, **router arch**,
  **ring**, **expected MAC**.

`PUT /v1/dashboard/agents/{nickname}` merges with the current row, so **blank
fields keep their existing value** — you can change only the domain without
wiping SSH or ring. System-managed fields (versions, pending-deploy markers) are
never touched.

## Resurrect / recovery

After editing, bring the agent back with the safe, already-allowlisted actions in
the same drawer section:

- **Wake / Force recheck** — asks the agent for a fresh report.
- **Restart AWG** — restarts AWG Manager on the router.

## On-router config (config.yaml on the router)

The drawer's **On-router config** section edits a safe whitelist of the agent's
own `config.yaml` *on the router* and restarts the agent to apply it — no SSH.

1. **Load config** (`agent_config_get`) — the agent returns the safe subset; the
   section shows the current interval / AWG-manager URL.
2. **Edit & restart** (`update_agent_config`) — opens a modal prefilled from the
   loaded values. Editable keys:
   - `agent.interval_sec` (10..86400)
   - `awg_manager.base_url` / `awg_manager.login`
   - `external_reach.enabled` / `external_reach.fail_threshold` (1..20)
   - `maintenance.allow_router_reboot` / `allow_firmware_install`

   Apply writes a **partial patch** (only the listed keys; comments and other keys
   are preserved via the yaml-node patcher), then restarts the agent.

This works only while the agent can still reach the backend (it's a long-poll
command). A fully dark agent must be fixed via SSH or the wizard. The agent
re-reads `config.yaml` on restart, so changes take effect on the next boot.

## What stays off the dashboard

Re-pointing an agent's **backend URL** (the `update_backend_url` command, which
makes the agent rewrite its config and self-restart) is **intentionally not**
exposed in the browser dashboard: a leaked dashboard session could redirect the
whole fleet to a hostile backend. That operation stays gated behind the wizard
token / deploy CLI (`actionMigrateBackendURL`). See
`TestDashboardCommandDispatchRejectsHiddenBackendURLUpdate`.
