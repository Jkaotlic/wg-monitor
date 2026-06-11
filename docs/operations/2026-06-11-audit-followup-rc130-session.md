# 2026-06-11 audit follow-up hardening session

Scope:
- DEP-03: deploy wizard PID lock is now guarded by a non-blocking OS file lock on Windows and Unix. A second wizard process gets `ErrAnotherWizardRunning`; stale PID files can still be overwritten only after the lock is acquired.
- BE-01: retention `VACUUM` now runs through a short-timeout maintenance SQLite connection instead of the backend primary single-connection pool.
- Gosec baseline: removed `G115` from the CI exclude list after bounding or documenting the remaining integer conversions.

Verification target before release:
- `go test ./... -count=1`
- `go vet ./...`
- `govulncheck ./...`
- `gosec -severity high -exclude=G101,G702,G703,G704 ./...`
- `git diff --check`

Known remaining audit follow-ups:
- `G101`, `G702`, `G703`, and `G704` are still intentionally excluded from the blocking gosec gate and should be reduced in separate, more semantic passes.
- AWG Manager TLS pinning remains backlog if the service is always reached through KeenDNS/Keenetic; it is not a current blocker for the standard deployment shape.
