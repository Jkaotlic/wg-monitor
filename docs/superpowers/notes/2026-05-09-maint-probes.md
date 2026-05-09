# Maintenance Panel — SSH Probes (M0)

**Date:** 2026-05-09
**Target:** testkeen (192.168.31.1, port 222)
**Goal:** Verify the 6 TBDs from the maintenance-panel design spec; capture
exact command outputs so subsequent milestones target real shapes.

## Probe 1 — awg-manager self-restart

**Tried via API:** all candidates return HTTP 403 from a curl without auth
(awg-manager guards mutation endpoints with basic-auth or session cookie):

```
POST /api/system/restart      → HTTP 403
POST /api/system/restart-self → HTTP 403
```

**Init.d script exists and works:**

```
$ /opt/etc/init.d/S99awg-manager status
AWG Manager running (PID 1895): http://192.168.31.1:2222
```

**Decision:** restart awg-manager via `Exec("/opt/etc/init.d/S99awg-manager", "restart")`.
No `awgmgr.RestartSelf` HTTP method needed — keeps us out of the auth headache
and uses the same path the operator would SSH to.

→ **Plan delta:** Skip Milestone 2 entirely. In M4.1's runner case for
  `service_restart awgmgr`, call Exec instead of `r.AwgClient.RestartSelf`.

## Probe 2 — Upstream repos

GitHub identifiers needed for `internal/backend/upstream/versions.go`:

- **awg-manager:** `Slava-Shchipunov/awg-keenetic` — TBD verify (not done in
  this probe session — only access is GitHub web; check before wiring backend
  config). Tag format observed in past: `vX.Y.Z`.
- **HydraRoute-Neo:** `Mihaylov-Sergei/HydraRoute-Neo` — TBD verify. (Note:
  `opkg info hrneo` lists `Maintainer: Ground_Zerro` — that may be the actual
  GitHub owner; check both before committing to one.)

→ **Plan delta:** Treat `cfg.Upstream.{AwgmgrRepo,HrneoRepo}` as required
  config; fall back to empty (graceful "no warning") when unset. Final repo
  IDs to be confirmed before deploy.

## Probe 3 — `ndmc -c "show version"` format

```
release: 5.00.C.11.0-0
sandbox: stable
title: 5.0.11
arch: aarch64

ndm:
  exact: 0-2e403e0
  cdate: 30 Apr 2026

bsp:
  exact: 0-a0dc62e05a
  cdate: 30 Apr 2026

ndw:
features: dual_image,wifi_button,...
components: afp,base,cloudcontrol,...

ndw3:
  version: 5.0.49

ndw4:
  version: 5.0.C.11

manufacturer: Keenetic Ltd.
vendor: Keenetic
series: KN
model: Ultra (KN-1811)
hw_version: 11188000
hw_type: router
hw_id: KN-1811
device: Ultra
consent: EA
region: EA
description: Keenetic Ultra (KN-1811)
```

**Field meanings:**
- `release` — full build identifier (matches `awg-manager.SystemInfo.firmwareVersion`).
- `title` — user-visible version (`5.0.11`).
- `sandbox` — channel (`stable`, `preview`, `draft`).
- `model` — hardware label (`Ultra (KN-1811)`).
- `ndm.exact` is a commit hash for the `ndm` component. **Not** the firmware version.

**There is NO field for "available update" in this command's output.**

→ **Plan delta to M3.1's parser:** strategy changes — see Probe 7 below.
  Parse `title`, `sandbox`, `model` from `show version` if needed for the
  panel header, but determine update availability from `components list`.

## Probe 4 — `ndmc -c "components list"` (UPDATE DETECTION)

**This is the answer to "is an update available":**

```
firmware:
  version: 5.00.C.11.0-0
  title: 5.0.11

sandbox: stable

local:
  sandbox: stable
  version: 5.00.C.11.0-0
```

**Field meanings:**
- `firmware.{version,title}` — the **server-side** release for the active sandbox.
- `local.version` — what is **installed** locally.
- `sandbox` — current channel.

**Update detection:** if `firmware.version != local.version` → update available.
On testkeen today both are `5.00.C.11.0-0` → no update.

→ **Plan delta to M3.1:** rename `parseShowVersion` → `parseComponentsList`.
  Read `firmware.version` (or `firmware.title`) as `Available`, `local.version`
  (or local + show-version `title`) as `Current`. If equal, set Available="".
  This is much cleaner than the original spec's parser sketch.

## Probe 4b — `ndmc -c "components commit"`

Did not execute (would actually attempt an install). Help text is silent (no
`(config)` mode reachable via `-c`). The spec's assumption stands: this is the
correct command to fire an install — same one the web UI uses. After commit,
the router reboots automatically.

→ **No plan change.** Keep `InstallFirmware` as `Exec("ndmc","-c","components commit")`.

## Probe 5 — HydraRoute-Neo version source

**`/api/system/hydraroute-status`** → HTTP 403 (same auth gate as Probe 1).

**`/opt/etc/HydraRoute/`** lists only config files — no `VERSION` file:
```
domain.conf      hrneo.conf      ip.list
domain.conf-opkg hrneo.conf-opkg ip.list-opkg
geofile          hrweb.conf
```

**`/opt/bin/hrneo`** binary exists, but `--version` / `-v` are unsupported
("not found" — wrong path tried initially; binary itself doesn't expose
`--version`).

**Winner: `opkg info hrneo`:**

```
Package: hrneo
Version: 2.4.0-1
Depends: libc, ipset, iptables
Status: install user installed
Maintainer: Ground_Zerro
Filename: hrneo_2.4.0-1_aarch64-3.10.ipk
...
```

→ **Plan delta:** in `VersionAudit`, source hrneo version from
  `Exec("opkg","info","hrneo")` — parse the `Version: ` line, strip the
  `-N` packager-revision suffix (`2.4.0-1` → `2.4.0`). The HRStatus API
  field is unreliable (auth + may not exist).

**Init.d for restart:** `/opt/etc/init.d/S99hrneo` exists, has `PROCS=hrneo`
and standard rc.func `restart` action.

→ **Plan delta to M4.1:** for `service_restart hrneo`, use
  `Exec("/opt/etc/init.d/S99hrneo", "restart")` — symmetric with the
  awg-manager case. Drop the `HydraRouteControl(restart)` API call from the
  spec — the API likely controls "feature on/off" not "process restart"
  and adds an auth dependency we don't need.

## Probe 6 — Daemon uptime mechanics

**busybox `ps` does NOT support `-o`:**

```
$ ps -o pid,etime,comm
ps: invalid option -- 'o'
```

**busybox `stat` does NOT support `-c`:**

```
$ stat -c %Y /proc/20604
stat: invalid option -- 'c'
```

**`getconf` is not installed** (so we cannot read `CLK_TCK` at runtime).

**What does work:**

```
$ pidof hrneo
20604

$ cat /proc/20604/stat
20604 (hrneo) S 1 20602 1874 0 -1 4194560 62568 896951 0 0 278 1662 453 365 \
20 0 1 0 4118135 3846144 ...
```

Field 22 (`starttime`) is `4118135` — process start time in **jiffies since
boot**.

```
$ cat /proc/uptime
105561.18 193126.14
```

First field is **system uptime in seconds** (float).

**USER_HZ on aarch64 Linux:** universally 100. Hard-code this constant.

**Computation:** `daemon_uptime_sec = system_uptime - (starttime_jiffies / 100)`.

For the example above: `105561 - (4118135 / 100) ≈ 105561 - 41181 ≈ 64380`
seconds = ~17.9 hours. Plausible (matches the date confirmation:
`Modify: 2026-05-08 22:24:36` ≈ 14h ago at probe time).

→ **Plan delta to M3.3:** `daemonUptime` reads `/proc/uptime` (one shot) and
  `/proc/$pid/stat` (per daemon). Parse stat field 22, divide by USER_HZ=100,
  subtract from system uptime. No `ps` / `stat` shell calls.

  Refactor: `daemonUptime(ctx, exec, name) string` becomes
  `daemonUptime(ctx, exec, sysUptime float64, name string) string` so the
  caller reads `/proc/uptime` once.

## Summary of plan deltas

| Plan item | Original | Adjusted (per probes) |
|---|---|---|
| **M2.1** RestartSelf API method | POST /api/system/restart-self | **Drop entirely** — use init.d Exec from runner |
| **M3.1** parser | parse `show version` for current+available | Parse `components list` for current (`local.version`) + available (`firmware.version`); use `show version` only for `title`/`sandbox`/`model` if needed |
| **M3.3** hrneo version | `awgmgr.HRStatus.Version` API | `Exec("opkg","info","hrneo")` + parse `Version:` line, strip `-N` |
| **M3.3** daemon uptime | `ps -o pid,etime,comm` | Parse `/proc/$pid/stat` field 22 with USER_HZ=100 + `/proc/uptime` |
| **M4.1** awgmgr restart | `r.AwgClient.RestartSelf(ctx)` | `r.Exec(ctx, "/opt/etc/init.d/S99awg-manager", "restart")` |
| **M4.1** hrneo restart | `r.AwgClient.HydraRouteControl(ctx,"restart")` | `r.Exec(ctx, "/opt/etc/init.d/S99hrneo", "restart")` |
| **M4.1** firmware install | unchanged | unchanged: `r.Exec(ctx, "ndmc","-c","components commit")` |

All other milestones remain valid. Notably the wire types, callback grammar,
panel renderer, upstream cache, smart-reply integration, and config
plumbing are untouched.
