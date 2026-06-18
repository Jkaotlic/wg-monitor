# wg-monitor notification-improvements session checkpoint

Date: 2026-06-18 (second session of the day, on top of the production-readiness audit)

## Scope

Operator asked: "проверь все уведомления, найди ложные срабатывания, сделай их
понятными для инженера с понятными кнопками, проверь от и до, сверяйся с конфигом
роутера". Done as a focused false-positive hunt + clarity/self-monitoring pass,
TDD throughout, cross-checked against the **live router** (awg-manager reachable
from the operator PC at `http://192.168.31.1:2222`).

Full findings + rationale: `docs/operations/2026-06-18-notification-false-positive-audit.md`.

## Live router picture (cross-check, 2026-06-18)

- Keenetic Ultra KN-1811, fw 5.1 Beta 4, HydraRoute 3.11.0 running.
- Two AWG tunnels, **both `defaultRoute:true`**: `awg10` (nwg0, ~1.7 MB) and
  `awg12` (nwg3, ~10.5 GB). Authoritative default per `settings.download.routeTag`
  = **awg12**.
- **pingCheck globally disabled**; static routes empty; many HR-Neo fall-through
  DNS policy rules → credited to the live default.
- Pi backend (`192.168.31.87`) was unreachable during the probe.

## Shipped (commit `f5a878d9`, pushed to origin/main)

False positives:
- external_reach: 4xx (bot-rejection, e.g. Instagram 403) no longer counts as an
  outage — only 5xx / transport errors do.
- periodic checks use the authoritative default (`routeTag`) not the first
  `defaultRoute=true`: external_reach was probing awg10, real egress is awg12;
  HR-Neo blast-radius was miscredited. Shared helper
  `awgmgr.Settings.ActiveDefaultTunnelID()`.

UX / clarity:
- STILL-DOWN realert reminders now carry the original HARD alert's action buttons.
- router-doctor warns on >1 `defaultRoute=true` and names the live one.
- tunnel advice nudges enabling pingCheck when it's disabled + handshake stale.
- external_reach shows reachable-but-4xx targets separately ("Доступны, но
  вернули отказ: Instagram (403)").

Self-monitoring:
- `/readyz` does a real table read (`db.HealthCheck`) → catches SQLite-corruption
  the bare ping missed; `docs/external-uptime-probe.md` now points at `/readyz`.
- new opt-in dead-man digest (`internal/backend/digest`): daily "🟢 Монитор жив —
  N/M онлайн" to the primary chat. `digest.{enabled,hour_msk,online_window_sec}`,
  default off.

Deliberately NOT changed:
- handshake≥180s "suspicion" — intentional early-warning, pinned by tests.
- per-tunnel restart on HARD alert — `tunnel_restart` panel-guard would rewrite
  the alert into a panel (regression risk).

## Verified

- `go build ./...`, `go vet ./...` — clean
- `go test ./... -count=1` — **29 packages OK, 0 failures** (new `digest` pkg)
- `git diff --check` — clean

## Push

- `git push origin main`: `23470cfd..f5a878d9` (this session's feature commit
  `f5a878d9` plus the previously-deferred 2026-06-18 audit/SEC commits that were
  sitting unpushed), then `f5a878d9..4b37040c` (this checkpoint doc).

## Repo cleanup (same session)

The working tree had accumulated clutter; tidied it:

- **Removed from disk** (regenerable build junk): `dist-rc107..111/` + their
  `-flat` variants (compiled release binaries for all platforms) and
  `.playwright-mcp/` (MCP tool logs/screenshots).
- **`.gitignore` extended**: `/dist-rc*/`, `.playwright-mcp/`, `.tmp/`
  (commit `b81ea2ee`).
- **14 internal operational notes kept local-only** (operator decision): no
  hard secrets, but internal infra topology → listed explicitly in `.gitignore`
  so they stay on disk but are NOT published to the public repo
  (commit `95f96458`). New shareable docs still get committed.
- `deploy.exe` (operator's wizard binary) and `dist/` left untouched (already
  ignored). `git status` is now clean.

Final HEAD pushed: `95f96458` (+ this doc update). origin/main == local HEAD.

## Deferred / next

- Deploy the updated agent + backend to the fleet (not done this session).
- `#3b` wizard auto-provisioning of the external uptime probe (needs a third SSH
  target type) — documented as future work in `docs/external-uptime-probe.md`.
- Optional: enable `digest.enabled: true` in backend.yaml after deploy; set up a
  real external probe against `/readyz`.
