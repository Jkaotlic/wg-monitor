package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// shrinkReleaseFetchRetryBackoffForTest swaps the package-level
// releaseFetchRetryBackoff (release_verify.go) for near-zero delays so these
// tests don't have to sit through the real 500ms/1s production schedule.
// Production code never reassigns the var; only tests do, and only for the
// duration of the calling test.
func shrinkReleaseFetchRetryBackoffForTest(t *testing.T) {
	t.Helper()
	old := releaseFetchRetryBackoff
	releaseFetchRetryBackoff = []time.Duration{time.Millisecond, 2 * time.Millisecond}
	t.Cleanup(func() { releaseFetchRetryBackoff = old })
}

// TestReleaseFetchTransportPinsIPv4 proves the shared transport's DialContext
// is pinned to tcp4. With the pin, dialing an IPv6 literal is rejected at
// address resolution ("no suitable address found") and never attempts the
// connection; were the pin absent (plain "tcp"), that same address would be
// dialed and fail later with connection-refused — so asserting the
// address-family rejection specifically is what proves the pin is in effect.
func TestReleaseFetchTransportPinsIPv4(t *testing.T) {
	conn, err := releaseFetchTransport.DialContext(context.Background(), "tcp", "[::1]:9")
	if err == nil {
		conn.Close()
		t.Fatal("tcp4-pinned DialContext connected to an IPv6 literal; IPv4 pin not in effect")
	}
	if !strings.Contains(err.Error(), "no suitable address") {
		t.Fatalf("expected an IPv4 address-family rejection, got: %v", err)
	}
}

// TestFetchReleaseVerifyAsset_RetriesTransientServerErrorThenSucceeds covers
// the case the live provisioning failure actually needs fixed: a first
// attempt that fails (here, HTTP 503; a stalled TLS handshake would surface
// the same way, as a failed attempt) followed by a good response.
func TestFetchReleaseVerifyAsset_RetriesTransientServerErrorThenSucceeds(t *testing.T) {
	shrinkReleaseFetchRetryBackoffForTest(t)

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("checksums-body"))
	}))
	defer srv.Close()

	body, err := fetchReleaseVerifyAsset(context.Background(), srv.URL+"/checksums.txt", 1<<20)
	if err != nil {
		t.Fatalf("fetchReleaseVerifyAsset: %v", err)
	}
	if string(body) != "checksums-body" {
		t.Fatalf("body = %q, want %q", body, "checksums-body")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (1 failed attempt + 1 success)", got)
	}
}

// TestFetchReleaseVerifyAsset_RetriesTransportErrorThenSucceeds simulates a
// genuine transport-level failure (as opposed to a well-formed non-2xx HTTP
// response) by hijacking and closing the connection before writing anything,
// which is what a stalled/reset connection looks like from the client's
// side. client.Do must return a non-nil error for this, exercising the same
// retry branch a real TLS handshake timeout would.
func TestFetchReleaseVerifyAsset_RetriesTransportErrorThenSucceeds(t *testing.T) {
	shrinkReleaseFetchRetryBackoffForTest(t)

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				panic("test server ResponseWriter does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				panic("hijack: " + err.Error())
			}
			_ = conn.Close() // drop the connection with no response: a transport-level failure
			return
		}
		_, _ = w.Write([]byte("checksums-body"))
	}))
	defer srv.Close()

	body, err := fetchReleaseVerifyAsset(context.Background(), srv.URL+"/checksums.txt", 1<<20)
	if err != nil {
		t.Fatalf("fetchReleaseVerifyAsset: %v", err)
	}
	if string(body) != "checksums-body" {
		t.Fatalf("body = %q, want %q", body, "checksums-body")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (1 failed transport-level attempt + 1 success)", got)
	}
}

// TestFetchReleaseVerifyAsset_DoesNotRetryNotFound pins the terminal-status
// fast path: 404 (like 401/403) is never worth a retry, so exactly one
// request should go out and the existing "HTTP %d for %s" error should come
// straight back.
func TestFetchReleaseVerifyAsset_DoesNotRetryNotFound(t *testing.T) {
	shrinkReleaseFetchRetryBackoffForTest(t)

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchReleaseVerifyAsset(context.Background(), srv.URL+"/checksums.txt", 1<<20)
	if err == nil {
		t.Fatal("expected a non-nil error for HTTP 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want it to mention HTTP 404", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want exactly 1 (404 must not be retried)", got)
	}
}

// TestFetchReleaseVerifyAsset_ContextCancellationReturnsImmediately pins that
// a caller-cancelled context short-circuits the loop instead of spending the
// retry budget/backoff on a request that can never succeed.
func TestFetchReleaseVerifyAsset_ContextCancellationReturnsImmediately(t *testing.T) {
	shrinkReleaseFetchRetryBackoffForTest(t)

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := fetchReleaseVerifyAsset(ctx, srv.URL+"/checksums.txt", 1<<20)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a non-nil error for a pre-cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", err)
	}
	// The shrunk backoff schedule totals 3ms; a real retry attempt would also
	// cost at least one more round trip. 250ms is generous headroom for a
	// "no retry was attempted" assertion without being timing-flaky.
	if elapsed > 250*time.Millisecond {
		t.Fatalf("took %s, want a near-immediate return with no retry spent", elapsed)
	}
}

// TestFetchReleaseVerifyAsset_RetryCapExceeded pins the cap itself: a
// persistently failing upstream gets exactly releaseFetchMaxAttempts
// requests, no more, and a non-nil error.
func TestFetchReleaseVerifyAsset_RetryCapExceeded(t *testing.T) {
	shrinkReleaseFetchRetryBackoffForTest(t)

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := fetchReleaseVerifyAsset(context.Background(), srv.URL+"/checksums.txt", 1<<20)
	if err == nil {
		t.Fatal("expected a non-nil error after exhausting the retry cap, got nil")
	}
	if got := requests.Load(); got != releaseFetchMaxAttempts {
		t.Fatalf("requests = %d, want exactly %d (the retry cap)", got, releaseFetchMaxAttempts)
	}
}
