# DNS-independent repair download — design

**Date:** 2026-07-08
**Status:** approved (operator)

## Problem

`Repair → reinstall` (and provision-install) drives the router to download the
agent binary from the backend's public URL, e.g.
`https://wgmonitor.anexaev.crazedns.ru/v1/releases/download/<ver>/<asset>`, via a
shell `fetch()` in the bootstrap script (`awgm-relay.py`
`build_deferred_bootstrap_script`).

A router whose DNS is broken cannot resolve that host and the download dies:

```
__WG_STEP__ downloading
curl: (6) Could not resolve host: wgmonitor.anexaev.crazedns.ru
```

This is a chicken-and-egg: repair is exactly the tool you reach for when a
router is broken, yet it needs working router DNS to run. Confirmed live on
`snekhaev` (agent dark — it can't resolve the backend to call home either).

## Goal

Let the download bypass the router's resolver by pinning the backend's public
IP, while keeping TLS validated against the real hostname (SNI + cert
unchanged). Signature (sha256) verification after download is unchanged, so a
wrong IP serving a wrong binary is still rejected — defense in depth intact.

## Why a static config IP (not auto-resolve)

The repo already has an auto-resolve convention (`repo_resolve_ip`,
`wizardRepoResolveIPForHost` → `net.LookupHost`). But the live backend runs on
rpie4 **inside** the operator's tunnel, where the public host resolves via
split-horizon DNS to a private tunnel address (`172.16.6.1`) — useless to the
fleet. So the backend cannot resolve its own fleet-facing IP correctly.
Decision: a static, operator-set `public_ip` in `backend.yaml`
(`128.0.142.207`). Empty → current behaviour (no `--resolve`), fully backward
compatible.

## Design

### 1. Config
`internal/backend/config.go`: add `PublicIP string \`yaml:"public_ip"\``,
trimmed on load like `PublicBaseURL`. IPv4 only (operator confirmed; no IPv6).

### 2. Deps + wiring
`internal/backend/handler.go` `Deps`: add `PublicIP string`.
`cmd/backend/main.go`: wire `cfg.PublicIP` → `Deps.PublicIP`.

### 3. Relay job field
`internal/backend/agent_deploy_router.go` `awgmInstallJob`: add
`DownloadResolveIP string \`json:"download_resolve_ip,omitempty"\``.
Both build sites in `provision_handler.go` (install ~L354, repair ~L604) set
`DownloadResolveIP: d.PublicIP`.

### 4. cmd/deploy (wizard-deferred) — shared builder
`run_deferred_bootstrap` and `run_install_bootstrap` both call
`build_deferred_bootstrap_script`, so the python change (below) covers both
once the cfg carries `download_resolve_ip`. `cmd/deploy/awgm_deferred.go` sets
the key from the deploy path's existing resolve-IP source when available;
absent → omitted (unchanged behaviour). Best-effort, not the primary target.

### 5. Python `awgm-relay.py`
In `build_deferred_bootstrap_script`, when `cfg["download_resolve_ip"]` is set:
- Parse host + port from `download_url` (`urlparse`; default 443 https / 80 http).
- Inject `RESOLVE_HOST` / `RESOLVE_IP` / `RESOLVE_PORT` vars (sh_quote'd; IP is
  trusted operator config, not user input).
- Rewrite `fetch()`:
  - **curl present:** `curl --resolve "$RESOLVE_HOST:$RESOLVE_PORT:$RESOLVE_IP" -fsSL "$url" -o "$dst"`.
  - **busybox wget (no --resolve):** append `"$RESOLVE_IP $RESOLVE_HOST"` to
    `/etc/hosts` for the download, run wget, then remove that exact line;
    preserve wget's rc and propagate a non-zero exit. If `/etc/hosts` is
    read-only (some Keenetic rootfs), the append fails silently and wget runs
    plain — best-effort.
- When unset: `fetch()` is byte-for-byte the current plain version.

TLS: `--resolve` and the hosts entry both keep SNI/hostname = the domain, so
the cert still validates. No `curl -k`, no IP-in-URL.

## Testing

- **Go:** `provision_handler_test.go` — install + repair jobs carry
  `DownloadResolveIP` from `Deps.PublicIP`; empty config → empty field.
  `config_test.go` — `public_ip` loads + trims.
- **Python** (`test_install_mode.py` / `relay_test.go` harness): generated
  script contains `curl --resolve <host>:443:<ip>` and the wget `/etc/hosts`
  add+cleanup when `download_resolve_ip` set; contains neither (plain fetch)
  when unset.

## Out of scope

- IPv6 pinning.
- Changing the wizard/self-update auto-resolve mechanism.
- Fixing snekhaev's own DNS (operational, router-side).

## Rollout

Backend-only change (agent binary untouched). Requires rebuilding/redeploying
the backend on rpie4 and setting `public_ip: 128.0.142.207` in its
`backend.yaml`. No fleet agent redeploy.
