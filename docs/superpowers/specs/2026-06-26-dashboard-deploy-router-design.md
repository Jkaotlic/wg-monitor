# Dashboard "Deploy to router" — remote first-time agent install from the frontend — design

Date: 2026-06-26
Status: approved (brainstorm with user; "норм делаем")

## Goal

Let an operator deploy the agent onto a **brand-new router directly from the
dashboard**, entering the AWG Manager login/password (or api-key) and the router
root password in the UI. Today the dashboard's "Add agent" only mints an
enrollment token; the real install is an out-of-band wizard CLI step. This adds a
second, separate **"Deploy to router"** action that performs the full remote
install by reusing the existing Revive AWG-Manager relay.

## Background — why there is no credential input today (verified)

Clicking **Add agent → Create enrollment** does exactly this and nothing more:

- Frontend `createEnrollment()` ([app.js:846](../../../internal/backend/dashboard_static/app.js#L846))
  POSTs `nickname, kind, telegram_*, deploy_mode, awgm_url, awgm_auth, ssh_host/port/user,
  arch, ring, expected_mac` to `/v1/dashboard/enrollments`. **No password / api-key /
  login value is in the payload** — `awgm_auth` is only an enum label
  (`web`/`api-key`/`none`, [index.html:211](../../../internal/backend/dashboard_static/index.html#L211)).
- Backend `dashboardEnrollmentHandler` ([dashboard_handler.go:870](../../../internal/backend/dashboard_handler.go#L870))
  mints a 32-byte token (`createAgentEnrollment`), stores only its **SHA256 hash**
  (`UpsertEnrollment`, `ON CONFLICT(nickname) DO UPDATE`), stores non-secret deploy
  metadata (`UpdateDeployInfo`), and returns the **raw token once**.
- The actual install is the **wizard CLI** (`wg-monitor-deploy → [3] Routers`,
  [DEPLOY.md:70](../../../DEPLOY.md#L70)): it prompts for AWG Manager api-key OR
  web login/password + the Entware terminal root password, caches them in a local
  `secrets.env` (0600), opens the AWG Manager terminal websocket, and bootstraps
  the agent. The backend never sees these secrets.

So the missing credential fields are **partly unfinished UI** (the `awgm_auth`
dropdown promises an auth mode with nowhere to type it) and **partly a deliberate
security boundary** (secrets are intentionally kept off the backend; the backend
`users` table has **no** password/api-key columns — only `token_hash` + non-secret
metadata).

### The reusable building block: Revive relay

The dashboard already has one credential-using path — **Revive**
([agent_revive.go:161](../../../internal/backend/agent_revive.go#L161)). It takes a
per-click root password (+ optional AWG creds), runs the embedded Python relay
(`awgmrelay.Script`, self-provisioned to a 0600 temp file — **no wizard step on the
backend**), which authenticates to the public AWG Manager, opens a **root terminal
over WebSocket**, and runs an arbitrary bootstrap script. Creds live only in a
0600 temp JSON deleted after the run; the root password is **never stored**.

Revive's script ([buildReviveScript](../../../internal/backend/agent_revive.go#L137))
only rewrites `backend.url` and **hard-fails if `config.yaml` is missing** — it
assumes an already-installed agent. The relay itself is install-capable: it already
contains the full first-time bootstrap builder
[build_deferred_bootstrap_script](../../../internal/awgmrelay/awgm-relay.py#L401) +
[build_agent_config](../../../internal/awgmrelay/awgm-relay.py#L365), used today by
the wizard's `deferred_bootstrap` mode. **What's missing is a dashboard endpoint
that drives the relay with a full-install script instead of the revive script.**

## Decisions (from brainstorm)

1. **Scope:** full remote deploy from the dashboard (not just capture creds for the
   wizard).
2. **Credential model:** per-click, **never stored** — same transient model as
   Revive (0600 temp file, deleted; no new DB columns, no crypto/RBAC). Keeps the
   documented security boundary: a leaked dashboard session ≠ fleet-cred leak.
3. **Target version:** default **latest stable** (`lookupDashboardLatestVersion`),
   with an optional **override** field (mirrors the existing Custom-version deploy).
4. **Flow shape:** **two steps** — keep "Create enrollment" as-is (token-only, for
   the wizard path), add a **separate "Deploy to router"** action with creds (like
   Revive). Allows deploy and later re-deploy.
5. **Arch:** **auto-detected** by the relay via `GET /api/system/info` (goArch) — no
   arch field in the form. Falls back to stored `arch` metadata if the probe fails.
6. **Script generation:** stays in the **Python relay** (new `bootstrap_install`
   mode reusing `build_deferred_bootstrap_script`) — single source of truth, no
   duplicated shell template in Go.
7. **Token:** the deploy **re-mints** the enrollment token (the backend only keeps
   the hash; a fresh `config.yaml` needs a fresh raw token). Re-mint is idempotent
   (`UpsertEnrollment` upserts) and rotates the token safely on re-deploy.

## Design

### Frontend — new "Deploy to router" modal

`internal/backend/dashboard_static/index.html` + `app.js`, cloning the Revive
modal/handler:

- New drawer/row action **"Deploy to router"** (icon `ti-rocket`), placed next to
  Revive in the recovery/deploy group.
- New `deployRouterModal`:
  - **AWG Manager URL** — shown from the stored `awgm_url` (read-only); if empty,
    the same guard as Revive: "set awgm_url first (Edit settings)".
  - **AWG Manager auth** — api-key **OR** login + password (`deployAwgmLogin`,
    `deployAwgmPassword`, `deployAwgmApiKey`). Needed for the relay to reach the
    terminal; defaulted by the stored `awgm_auth` label.
  - **Router root password** — required (terminal login).
  - **Target version** — prefilled with latest stable, editable (override).
  - Note (same wording as Revive): creds used once via the AWG Manager terminal and
    never stored.
- `submitDeployRouter()` → `POST /v1/dashboard/agents/{nickname}/deploy-router`;
  render relay output in the result drawer (reuse `showReviveResult` shape, incl.
  the 502-with-`{output,error}` path).

### Backend — `dashboardDeployRouterHandler`

New file `internal/backend/agent_deploy_router.go` (mirror of `agent_revive.go`).

Request: `{ awgm_login, awgm_password, awgm_api_key, root_password (required),
version (optional) }`.

1. Load user by nickname; require a valid stored `awgm_url` (same guard as Revive).
2. **Re-mint** the enrollment token via `createAgentEnrollment` → fresh raw token
   (+ new hash persisted).
3. Resolve target version: override if provided, else `lookupDashboardLatestVersion`.
4. Fetch `checksums.txt` for the version via the backend's own release proxy
   (`/v1/releases/download/{version}/checksums.txt`,
   [release_proxy.go:72](../../../internal/backend/release_proxy.go#L72) already
   allows it) and parse into a `{asset: sha256}` map. New small helper, porting the
   `<sha>␠␠<file>` parse from
   [github.go:351 fetchExpectedSha](../../../cmd/deploy/github.go#L351).
5. Build an `awgmReviveJob`-style job with `Mode: "bootstrap_install"` carrying:
   transient auth (api-key/login/password) + `TerminalUser: root` + root password,
   plus install params: `nickname, version, backend_url (PublicBaseURL), raw_token,
   checksums map, release_base (backend "/v1/releases/download"), init_script
   (embedded S99)`. Run it via the existing `runAWGMRelayJob` (0600 temp JSON,
   deleted after).
6. On success: `UpdateDeployInfo` (deploy_mode=`awgm`, last_deploy, last_deployed_version,
   awgm_url/awgm_auth). The pending marker clears on the next heartbeat.
7. Log success/failure **without credentials** (same as Revive).

### Relay — new `bootstrap_install` mode

`internal/awgmrelay/awgm-relay.py`: add a `mode == "bootstrap_install"` branch in
`main()` (alongside `deferred_bootstrap` / `system_info`). It is a **wizard-free**
variant of `run_deferred_bootstrap`:

- `login_if_needed` → `GET /api/system/info` → `goArch` (arch auto-detect).
- Set `cfg["expected_sha"]` from the job's checksums map by asset
  `wg-monitor-agent-linux-<arch>` (the builder reads `cfg.get("expected_sha")` and
  raises if it is empty).
- `script = build_deferred_bootstrap_script(cfg, backend_url, raw_token, arch)`
  (reuses the existing builder + `build_agent_config`). The job must therefore carry
  the cfg keys that builder reads: `target_version`, `release_base`, `init_script`,
  `nickname` (plus `backend_url`, `raw_token` passed as args). These mirror the keys
  the wizard's `deferred_bootstrap` already populates.
- `ensure_terminal` → `ws_connect` → `login_terminal` (root) → `run_bootstrap` →
  `terminal/stop`. Return output + rc (same contract `run_bootstrap` already uses).
- **No** local-wizard enrollment calls, **no** two-phase token-commit, **no** sqlite
  writes — the backend already minted the token and holds the DB.

Backend job struct: add a **dedicated install-job struct** (e.g. `awgmInstallJob`)
rather than overloading `awgmReviveJob`, sharing the common auth/terminal fields via
a small embedded base. It carries the install fields (`mode=bootstrap_install`,
`nickname, target_version, raw_token, backend_url, release_base, init_script,
checksums`) plus the transient auth/root password. `runAWGMRelayJob` is generalized
to marshal either job (it already just writes the job JSON to the 0600 temp file).

### Embedded init script

The S99 init template lives at
[cmd/deploy/templates/S99wg-monitor](../../../cmd/deploy/templates/S99wg-monitor)
(read via `ReadStaticTemplate`). Embed the same file in the backend (`//go:embed`,
or promote it to a shared package imported by both `cmd/deploy` and
`internal/backend`) so the handler can pass `init_script` to the relay. The two
copies must stay byte-identical — prefer a shared package over a duplicate.

## Data flow (happy path)

operator fills modal → `POST /deploy-router` → backend re-mints token, resolves
version, fetches+parses checksums → relay (0600 job): AWGM login → system_info(arch)
→ pick sha → build install script (binary URL + config.yaml w/ token + S99 init) →
root terminal → run bootstrap → rc 0 → backend `UpdateDeployInfo` → agent boots,
sends heartbeat → pending clears, version confirmed in fleet view.

## Error handling / edge cases

- **Missing `awgm_url`** → 400 (same as Revive).
- **AWG auth / terminal login failure** → relay non-zero rc; handler returns 502
  with `{ok:false, output, error}`; UI shows the terminal output (as Revive does).
- **Checksum mismatch on the router** → install script `exit 14`; surfaced in output.
- **Existing different agent on the router** → `build_deferred_bootstrap_script`
  refuses to overwrite a `config.yaml` whose `agent.nickname` differs (`exit 11`) —
  protects against clobbering another agent.
- **No `/opt` (Entware)** → script `exit 10`.
- **system_info has no goArch / probe fails** → relay raises a hard error (same as `run_deferred_bootstrap`); no silent wrong-arch install. Handler returns a clear error to the caller.
- **python3 missing on backend host** → existing `defaultRunAWGMRelayJob` error.
- **Re-deploy of a live agent** rotates the token (new hash); the freshly written
  `config.yaml` carries the new token, so the agent re-authenticates cleanly.

## Security

- Transient-only, identical to Revive: creds in a 0600 temp file, deleted after the
  run; root password never persisted; gated by the same dashboard session/Bearer.
- No new secret columns; the documented dashboard security boundary
  (`feedback_dashboard_security_boundary.md`) is preserved — a browser-session
  compromise still cannot exfiltrate stored fleet credentials (there are none).
- Logs never include credentials.

## Out of scope

- Storing AWG/root creds on the backend; per-operator auth / RBAC / rate-limiting
  (rejected — would break the boundary and needs at-rest crypto the project lacks).
- Local AWG-Manager auth in the deployed agent config (parity with the wizard:
  `awg_manager.base_url: http://127.0.0.1:2222`, no login baked in).
- Wiring the `pull` / `ssh` / `legacy-ssh` deploy modes from the dashboard (the
  dropdown labels stay metadata-only).
- A single-click "enroll + install" merge (we chose the two-step flow).

## Verification

- `go build ./...` and `go test ./internal/backend/... ./internal/awgmrelay/...`.
- New backend unit test mirroring `agent_revive_test.go`: a fake `runAWGMRelayJob`
  asserts the job is built with the right install fields, version resolution, and
  no-creds logging; checksum-parse helper test; 400/502 paths.
- Relay `bootstrap_install` mode: a Python-level test (or a Go test that runs the
  embedded relay against a stub) covering arch selection + script assembly.
- Manual UI sanity of the new modal against the locally-served static.
- A real end-to-end install is validated on a live router after deploy (the relay
  terminal path cannot be unit-tested against a real AWG Manager).
