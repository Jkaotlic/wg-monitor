package provision

import (
	"context"
	"fmt"
	"time"
)

// LastSeenFunc returns nick's most recent report time. The backend receives
// agent reports directly, so it — not the relay, not the router — is the
// source of truth for "is this agent actually running". found mirrors the
// usual map-lookup idiom: false means nick has never reported at all, in
// which case the returned time.Time is meaningless and must not be
// inspected. Production wires a closure over the real report store (e.g.
// db.Users().GetByNickname(nick).LastSeenAt, per Task 9's wiring); tests
// inject a stub.
type LastSeenFunc func(nick string) (time.Time, bool)

// VerifyOnline is the last step of every provision/repair job (see the
// installSequence/repointSequence step catalogs in steps.go, which both end
// in StepVerifyOnline). A script exiting rc==0 only proves the router-side
// relay run completed cleanly — it says nothing about whether the
// (re)started agent is actually phoning home on the (possibly just
// rewritten) backend URL. So the engine polls lastSeen — the backend's own
// report state — until it observes a report timestamped strictly after
// `since` (the job's start time, so a stale report left over from before
// this run can never be mistaken for fresh confirmation), or `budget`
// elapses, whichever comes first.
//
// since is deliberately a caller-supplied instant rather than "now at call
// time": VerifyOnline typically runs after an install/repoint sequence that
// already took real time, so a report that arrived mid-sequence but before
// this call started polling must still count as stale, not fresh.
//
// budget/poll are caller-supplied rather than hardcoded here so this stays
// a pure polling primitive; the engine (Task 6) owns the concrete
// constants. The provisioning-rework spec's grounded default is a 120s
// budget against a 3s poll cadence, sized off the live fleet's measured
// ~68s median report interval (120s ≈ 2 report intervals, enough slack for
// a fresh agent's first phone-home without making a hung/offline agent
// block the job for long).
//
// now and sleep are injected — production wires time.Now/time.Sleep, tests
// wire a virtual clock — so a full budget timeout never makes a test
// actually sleep in real time. ctx is checked every iteration so a caller's
// timeout or shutdown (e.g. the job's own server-owned context) can abort
// the poll promptly instead of riding out the full budget.
//
// detail is only populated on success, e.g. "first report 4s ago" (for
// display in the job's verify_online Step.Detail); a failed verify —
// budget exhausted or ctx done — returns "" since there is nothing yet to
// report. ok false is the signal the engine uses to map this step to the
// agent_offline hint.
func VerifyOnline(ctx context.Context, nick string, since time.Time, budget, poll time.Duration,
	lastSeen LastSeenFunc, now func() time.Time, sleep func(time.Duration)) (detail string, ok bool) {
	start := now()
	for {
		n := now()
		if seen, found := lastSeen(nick); found && seen.After(since) {
			return fmt.Sprintf("first report %ds ago", int(n.Sub(seen).Seconds())), true
		}
		if ctx.Err() != nil {
			return "", false
		}
		if n.Sub(start) >= budget {
			return "", false
		}
		sleep(poll)
	}
}
