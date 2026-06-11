# 2026-06-11 audit hardening session

Source audit: `docs/audit-2026-06-11.md`.

## Fixed in this batch

- Release assets are now protected by an Ed25519 signature over `checksums.txt`.
  - Public key is embedded in `internal/releasesig`.
  - GitHub Actions signs `dist/checksums.txt` into `dist/checksums.txt.sig`.
  - Agent self-update, backend remote update, and deploy downloader require the signature for releases at or after `v0.13.0-rc128`.
  - `v0.13.0-rc127` and older releases remain unsigned-compatible for rollback.
- Backend release proxy now allows `checksums.txt.sig`.
- AWG Manager Python relay no longer disables TLS verification unconditionally.
  - It uses normal TLS verification by default.
  - `AWGM_INSECURE_TLS=1` / relay config remains an explicit break-glass path.
- Restore script validates required restored token files before stopping `wg-monitor-backend`.
- Agent `update_backend_url` now validates HTTPS public URLs, rejects private/local literal IPs, runs a `/healthz` check, and only then rewrites config/restarts.
- Agent command loop deduplicates repeated `cmd.ID` values and reposts the cached result instead of re-executing.
- Agent poll backoff now uses full jitter.
- Dashboard session cookies are time-bound and reject expired values.
- Backend rate limiter now applies a global bucket to missing `uid` instead of bypassing.
- `BE-08` timestamp storage concern is covered by a regression test; current storage already preserves an RFC3339-like value.
- CI now includes `govulncheck`, a high-severity `gosec` gate with an explicit historical baseline, and a `grype` high-severity scan.

## Still intentionally not fully closed

- Full AWG Manager TLS fingerprint pinning is not implemented. The unconditional Python TLS bypass is closed; the Go direct path still has the explicit `AWGM_INSECURE_TLS=1` break-glass behavior.
- Runtime SQLite `VACUUM` lock hardening (`BE-01`) is not completed in this batch.
- Deploy process locking (`DEP-03`) is not completed in this batch.
- The gosec baseline still has noisy historical rule classes excluded in CI: `G101,G115,G702,G703,G704`. This lets the gate run now while future work removes exclusions one class at a time.

## Verification

- `go test ./... -count=1`
- `go vet ./...`
- `govulncheck ./...`
- Local `gosec` high gate over `go list` package directories: 182 files, 0 issues with the same historical baseline.
- GitHub secret `WG_MONITOR_RELEASE_SIGNING_SEED_B64` exists.
- Local `grype` scan could not be completed because this host could not download the Grype vulnerability DB from `grype.anchore.io`.
