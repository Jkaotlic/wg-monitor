# wg-monitor notification audit session checkpoint

Date: 2026-06-17

Scope:
- Full notification audit pass across Telegram alerts, dashboard/status cards, backend notifier flows, command result notifications, maintenance/update/deploy paths, route/tunnel/diagnostic/pingcheck/opkg/firmware/security/access-control surfaces.
- Confirmed high/medium fixes were handled with focused regression tests first, then minimal fixes, then focused verification.
- Low-risk UX/operational notes were recorded in `docs/operations/2026-06-17-notification-audit.md`.

Completed fixes now on `main`:
- `9c48f4e7 fix: route notification followups to router chats`
- `4b4e7447 fix: persist hard alerts before telegram send`
- `92f8e455 docs: record notification audit notes`

Verified before this checkpoint:
- `go test ./... -count=1`
- `go vet ./...`
- `git diff --check`
- `govulncheck ./...`

Release intent:
- Cut the next release candidate from current `main`.
- Previous local RC baseline was `v0.13.0-rc132`; the intended next tag for this checkpoint is `v0.13.0-rc133` unless remote tags show it already exists.

Operational notes:
- No deploy was requested as part of this checkpoint.
- Existing unrelated/untracked artifacts were intentionally left untouched.
