// cmd/backend/test_helpers_test.go
//
// Local copy of waitForRelay (mirrors internal/backend/handler_test_helpers_test.go)
// — package main can't import test-only helpers from another package, so we
// duplicate the few-line helper instead of exporting it.
package main

import (
	"testing"
	"time"
)

// waitForRelay polls fn() until it returns want or the deadline elapses.
// Returns the last observed value so the caller can assert exact equality.
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
