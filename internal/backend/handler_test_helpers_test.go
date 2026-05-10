// internal/backend/handler_test_helpers_test.go
//
// Shared test plumbing for handler_test.go and friends. Lives in package
// backend so it can be reused across *_test.go files without exporting any
// production symbols.
package backend

import (
	"testing"
	"time"
)

// waitForRelay polls fn() until it returns want or the deadline elapses.
// Used to wait on async relay/dispatch goroutines that the handler kicks off
// after returning 200 OK to the agent. Returns the last observed value so the
// caller can assert exact equality (e.g. "want 1, got 0" → handler never
// dispatched at all vs "want 1, got 2" → fired twice).
//
// Polls every 10ms — same cadence the open-coded loops used. The fn callback
// must be cheap and concurrency-safe (typically reads an atomic counter or
// holds a mutex briefly).
func waitForRelay(t *testing.T, fn func() int, want int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := fn()
	for time.Now().Before(deadline) {
		last = fn()
		if last == want {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last
}
