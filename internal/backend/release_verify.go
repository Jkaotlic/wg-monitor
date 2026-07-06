package backend

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
	"github.com/Jkaotlic/wg-monitor/internal/releasesig"
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

func fetchReleaseVerifyAsset(ctx context.Context, assetURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, assetURL)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
