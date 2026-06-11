# 2026-06-11 production gate fixes

## Scope

- Closed the release download origin gap for agent and backend self-update paths.
- Preserved the enrollment `PublicBaseURL` fix so generated install payloads do not leak private LAN hosts.
- Added a regression test proving AWG Manager fractional-second timestamps decode successfully.
- Added `govulncheck ./...` to CI.

## Security boundary

`repo_base` is now accepted only when it exactly matches one of the compiled release origins:

- `https://github.com/Jkaotlic/wg-monitor/releases/download`
- `https://wgmonitor.anexaev.crazedns.ru/v1/releases/download`

Arbitrary HTTP origins, non-allowlisted HTTPS origins, localhost, and literal private IP origins are rejected before any download starts.

## Verification

- `go test ./internal/releaseorigin ./cmd/backend ./internal/agent/actions ./internal/agent/awgmgr ./internal/backend -count=1`
- `git diff --check`
- `go test ./... -count=1`
- `go vet ./...`
- `govulncheck ./...`

`govulncheck` reported 0 vulnerabilities affecting called code.
