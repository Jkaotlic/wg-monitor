package backend

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/releaseorigin"
	"github.com/anex/wg-monitor/internal/releasesig"
)

// verifyChecksumsSignature is a package var wrapping
// releasesig.VerifyChecksumsSignature — the same call cmd/deploy/github.go's
// fetchExpectedSha uses — so verifiedReleaseChecksums never has its own,
// separate signature-verification logic to drift out of sync with the rest
// of the fleet's update paths.
//
// It exists as a var (rather than calling releasesig.VerifyChecksumsSignature
// directly) solely so tests can substitute a locally generated signing key:
// the production public key baked into internal/releasesig has no matching
// private key in this repo (the signing seed is a GitHub Actions secret), so
// an accept-path test cannot produce a signature the real key would verify.
// Production code paths never reassign this var.
var verifyChecksumsSignature = releasesig.VerifyChecksumsSignature

// Bounded reads for the two release assets this helper fetches. These mirror
// cmd/deploy/github.go's maxChecksumsBytes / maxChecksumsSigBytes (that
// package is `package main`, so its constants aren't importable here).
const (
	maxVerifiedChecksumsBytes    = 1 << 20 // checksums.txt: 1 MiB, same cap fetchReleaseChecksums uses
	maxVerifiedChecksumsSigBytes = 8 << 10 // checksums.txt.sig: ed25519 sigs are 64 bytes; generous cap
)

// releaseFetchTransport is the shared HTTP transport for outbound fetches
// from GitHub's release-assets CDN: checksums.txt and its detached signature
// below, and — via release_proxy.go, which reuses this same var — the
// proxied binary/archive downloads. It replaces Go's DefaultTransport, whose
// 10s TLSHandshakeTimeout and lack of an explicit dial timeout were, paired
// with zero retries, enough for a single stalled handshake over a flaky home
// uplink to abort an entire provisioning run ("net/http: TLS handshake
// timeout"). The values below are tuned to tolerate that home->CDN path
// while still failing well short of the caller's own context deadline.
//
// Built once and shared package-wide, never recreated per call: per the
// net/http docs, a Transport caches connections for reuse and is meant to be
// long-lived, not constructed per request.
// releaseFetchDialer is the IPv4-pinned dialer releaseFetchTransport uses; see
// the DialContext comment below for why tcp4.
var releaseFetchDialer = &net.Dialer{
	Timeout:   10 * time.Second,
	KeepAlive: 30 * time.Second,
}

var releaseFetchTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	// DialContext pins IPv4 ("tcp4") for GitHub's CDN. The failure this whole
	// transport exists to fix — "net/http: TLS handshake timeout" reaching
	// release-assets.githubusercontent.com — is the classic IPv6-blackhole
	// signature: the dual-stack host has a working AAAA route, so Go's
	// Happy-Eyeballs completes the TCP connect over IPv6, but the TLS handshake
	// over that v6 path stalls to death. GitHub's CDN is fully reachable over
	// IPv4 and every host this backend runs on has working IPv4, so pinning
	// tcp4 sidesteps the blackhole rather than paying the handshake-timeout tax
	// on every attempt. (Retries + the tuned timeouts below cover the other
	// cause, transient throttling, which IPv4-pinning alone would not.)
	DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		return releaseFetchDialer.DialContext(ctx, "tcp4", addr)
	},
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   20 * time.Second,
	ResponseHeaderTimeout: 20 * time.Second,
}

// releaseFetchMaxAttempts bounds fetchReleaseVerifyAsset's retry loop: the
// initial attempt plus at most two retries.
const releaseFetchMaxAttempts = 3

// releaseFetchRetryBackoff is the fixed, deterministic (no jitter) delay
// fetchReleaseVerifyAsset waits before retry attempts 2 and 3 — index 0 is
// the wait before attempt 2, index 1 before attempt 3. It's a package var
// rather than a const solely so tests can shrink it; production code never
// reassigns it.
var releaseFetchRetryBackoff = []time.Duration{500 * time.Millisecond, 1 * time.Second}

// verifiedReleaseChecksums downloads "<base>/<version>/checksums.txt" and its
// detached "<base>/<version>/checksums.txt.sig", verifies the signature with
// the same releasesig call cmd/deploy/github.go uses, and only then parses
// the checksums via the existing parseReleaseChecksums (release_checksums.go)
// — the parser is reused, not duplicated. It returns a non-nil error, and
// never a partial map, if either asset can't be fetched or the signature is
// missing/malformed/invalid.
//
// base is expected to already be a validated release origin (for example the
// return value of releaseorigin.ValidateRepoBase / DefaultGitHubReleaseBase /
// DefaultBackendMirrorBase). This helper does not itself constrain base to
// https/public hosts — callers validate that upstream — so it stays testable
// against plain-http loopback httptest servers.
func verifiedReleaseChecksums(ctx context.Context, base, version string) (map[string]string, error) {
	v, err := releaseorigin.ValidateReleaseTag(strings.TrimSpace(version))
	if err != nil {
		return nil, fmt.Errorf("invalid release tag: %w", err)
	}
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return nil, fmt.Errorf("release base is required")
	}
	checksumsURL := b + "/" + url.PathEscape(v) + "/checksums.txt"

	body, err := fetchReleaseVerifyAsset(ctx, checksumsURL, maxVerifiedChecksumsBytes)
	if err != nil {
		return nil, fmt.Errorf("fetch checksums: %w", err)
	}
	sig, err := fetchReleaseVerifyAsset(ctx, checksumsURL+".sig", maxVerifiedChecksumsSigBytes)
	if err != nil {
		return nil, fmt.Errorf("fetch checksums signature: %w", err)
	}
	if err := verifyChecksumsSignature(body, sig); err != nil {
		return nil, fmt.Errorf("checksums signature: %w", err)
	}

	sums := parseReleaseChecksums(string(body))
	if len(sums) == 0 {
		return nil, fmt.Errorf("checksums.txt for %s parsed to zero entries", v)
	}
	return sums, nil
}

// fetchReleaseVerifyAsset fetches assetURL, retrying up to
// releaseFetchMaxAttempts times (releaseFetchRetryBackoff between attempts)
// on transport-level errors (TLS handshake timeout, dial timeout, connection
// reset, ...) and on HTTP 5xx/429 responses. It does not retry context
// cancellation/deadline — that is returned immediately — nor other terminal
// HTTP statuses such as 404/401/403, which are returned immediately as the
// same "HTTP %d for %s" error a non-retrying fetch would produce. After the
// final attempt, the last error seen is returned.
func fetchReleaseVerifyAsset(ctx context.Context, assetURL string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second, Transport: releaseFetchTransport}

	var lastErr error
	for attempt := 1; attempt <= releaseFetchMaxAttempts; attempt++ {
		if attempt > 1 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(releaseFetchRetryBackoff[attempt-2]):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
			resp.Body.Close()
			return body, err
		}

		// Drain (bounded) so the connection can be reused, then decide
		// whether this status is worth spending another attempt on.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, limit))
		resp.Body.Close()
		lastErr = fmt.Errorf("HTTP %d for %s", resp.StatusCode, assetURL)
		if resp.StatusCode < http.StatusInternalServerError && resp.StatusCode != http.StatusTooManyRequests {
			return nil, lastErr // terminal: 404/401/403/etc — not worth retrying
		}
	}
	return nil, lastErr
}
