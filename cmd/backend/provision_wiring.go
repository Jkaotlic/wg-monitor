// cmd/backend/provision_wiring.go
//
// Wires the router-provisioning engine's LastSeenFunc collaborator to the
// backend's own report state. Kept in its own small file (rather than inline
// in main) so it can be unit-tested directly (provision_wiring_test.go)
// without spinning up main()'s whole dependency graph.
package main

import (
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// provisionLastSeen returns a provision.LastSeenFunc backed by the exact same
// column POST /v1/report updates: reportHandler's own
// `UPDATE users SET last_seen_at = ...` (internal/backend/handler.go), read
// back here via the ordinary Users().GetByNickname query. The provisioning
// engine's verify_online step (internal/backend/provision/verify.go) polls
// this after an install/repoint to confirm the (re)started agent actually
// phoned home on the new config — wiring it to any other snapshot (e.g. a
// heartbeat-watcher scan cache) would let verify_online lag behind or miss
// the real signal entirely, since that cache only refreshes on its own scan
// cadence rather than on every report.
//
// found is false both for a nickname that has never enrolled at all and for
// one that enrolled but has never sent a report yet — VerifyOnline (the only
// caller) treats both identically: keep polling until budget runs out.
func provisionLastSeen(database *db.DB) provision.LastSeenFunc {
	return func(nick string) (time.Time, bool) {
		u, err := database.Users().GetByNickname(nick)
		if err != nil || u == nil || u.LastSeenAt == nil {
			return time.Time{}, false
		}
		return u.LastSeenAt.UTC(), true
	}
}
