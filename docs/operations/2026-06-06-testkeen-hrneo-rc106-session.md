# 2026-06-06 testkeen HR-Neo false-positive fix

## Problem

`testkeen` reported green tunnel connectivity even when HR-Neo policy routes
for blocked services were effectively tied to `nwg1/awg10`, while `awg10`
was not usable (`status=starting`). The Telegram `check_via_tunnel` command
fell back to another running default-route tunnel (`nwg5`) and produced a
false OK.

The route panel was also confusing because disabled NDMS rules were included
in the summary as total rules, which made NDMS look active even when the active
routing path was HR-Neo.

## Root Causes

- DNS route counts ignored `routes[0].tunnelId` when `routes[0].interface`
  was empty, so `tunnel_awg10` could look unused and get suppressed.
- `check_via_tunnel` treated only explicit HR-Neo `routes[]` as authoritative.
- Live HR-Neo rules such as `hr:YOUTUBE` used `routes:null` plus
  `hrRouteMode=policy` / `hrPolicyName=HydraRoute`, so they followed the
  policy default route.
- The policy default route and the usable connectivity fallback diverged:
  route status credited policy rules to first enabled defaultRoute (`nwg1`),
  while connectivity probes used first running defaultRoute (`nwg5`).

## Fixes

- `tallyRouteCounts` now falls back from `interface` to `tunnelId`.
- `check_via_tunnel` now fail-closes when a matching HR-Neo rule points to an
  unavailable tunnel instead of testing another tunnel.
- HR-Neo matching now recognizes route names, `hr:*`, `geosite:*`, and
  `geoip:*` aliases for the built-in connectivity probes.
- HR-Neo policy/fall-through rules now use the policy default route for the
  match decision; if that route is unavailable, the command returns an error.
- Route snapshot text now reports active rules separately from disabled rules.

## Releases

- `v0.13.0-rc104`: initial route count/fail-closed fixes.
- `v0.13.0-rc105`: added `geosite`/alias matching.
- `v0.13.0-rc106`: aligned HR-Neo policy default-route handling with route
  status and deployed as the final live fix.

## Verification

- `go test ./...` passed before each final release step.
- GitHub CI and Release workflows passed for `v0.13.0-rc106`.
- Backend health:
  `https://wgmonitor.anexaev.crazedns.ru/healthz` returned
  `{"status":"ok","version":"v0.13.0-rc106"}`.
- Final component table showed backend and online/static agents on
  `v0.13.0-rc106`; `router4car4` and `caredns-oldcar` remain pending until
  next wake; `bronya` still needs metadata repair.
- Live `check_via_tunnel` for `testkeen` returned:
  `HR-Neo правило "YOUTUBE" для YouTube идёт через nwg1, но этот туннель сейчас не запущен/не пригоден; fallback на другой туннель отключён, чтобы не показать ложный OK`.
