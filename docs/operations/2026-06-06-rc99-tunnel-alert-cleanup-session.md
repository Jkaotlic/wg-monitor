# 2026-06-06 rc99 tunnel alert cleanup session

## Scope

This session fixed false hard alerts for reserve DNSLK/default route rules, legacy tunnel deletion failures, and import/delete regressions seen after NativeWG replacement work.

## Root causes

- Hard alert false positives were caused by disabled/stale AWG/HR-Neo route rules still being counted as active fallback coverage.
- Legacy `awgNN` tunnel deletion could fail with AWG Manager `409 tunnel_referenced`, leaving broken config files behind.
- Import success is still not proof of tunnel health; the UI/status path must keep checking after import and report a later status transition.

## Code changes released

- DNS/static-route alert logic now ignores disabled rules.
- HR-Neo/default fallthrough credit only applies when the referenced default tunnel is enabled.
- DNS alert severity downgrades when DNS endpoints fail but neighboring tunnels are alive.
- UI delete now passes `force_legacy_cleanup` for legacy `awgNN` tunnels.
- Agent forced cleanup now handles `409 tunnel_referenced` by removing legacy files, restarting AWG Manager, and verifying the tunnel disappeared.

## Release and deploy

- Final release: `v0.13.0-rc99`.
- Commits pushed to `main`:
  - `b6494472 fix: ignore disabled routes in tunnel alerts`
  - `04be53a9 fix: ignore disabled default tunnel fallthrough`
  - `5b3f619f fix: force cleanup referenced legacy tunnels`
- Tags pushed: `v0.13.0-rc97`, `v0.13.0-rc98`, `v0.13.0-rc99`.
- Backend on the Raspberry Pi was updated to `v0.13.0-rc99`; health returned `{"status":"ok","version":"v0.13.0-rc99"}`.
- Online static agents updated to `v0.13.0-rc99`: `alyaba`, `testkeen`, `de4ddy`, `del`, `gachimikhail`, `puzirek`.
- Mobile/offline agents were queued for deferred update:
  - `bronya`: `93727cfc2a460a9735b5b70a525e88c4`
  - `caredns-oldcar`: `82a65cf17f9cc9b6ee6cf9862c727635`
  - `router4car4`: `a82d24d1454acd71d8b14a3bdf35c610`

## Verification

- `go test ./...` passed with local `.gocache`.
- GitHub prerelease `v0.13.0-rc99` was created with assets.
- `testkeen` legacy `awg10` deletion was verified: AWG Manager returned `409 tunnel_referenced`, forced cleanup completed, and only `awg11`/`awg12` remained running.
- `testkeen router_doctor` was OK after cleanup: `tunnels: 2/2 running`.
- `alyaba` remained a real router/tunnel outage after update, not a rollout problem: `0/4 running`, no handshakes.

## Follow-up

- Investigate `alyaba` separately as a live tunnel/router outage.
- Keep import UX asynchronous: after config import, show "checking" and update the message later with the real tunnel status instead of treating import success as health success.
- Deferred mobile updates should complete when those agents next report.
