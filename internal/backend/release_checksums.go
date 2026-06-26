package backend

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/releaseorigin"
)

// releaseChecksumsFetcher is a package var so handler tests can stub the
// network fetch. It returns a map of release asset filename -> sha256.
var releaseChecksumsFetcher = fetchReleaseChecksums

// parseReleaseChecksums parses GitHub's "checksums.txt" ("<sha256>␠␠<file>"
// lines) into asset->lowercase-sha. Malformed lines are skipped.
func parseReleaseChecksums(content string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		out[fields[1]] = strings.ToLower(fields[0])
	}
	return out
}

// fetchReleaseChecksums downloads and parses checksums.txt for a release tag
// from the same upstream the release proxy mirrors.
func fetchReleaseChecksums(ctx context.Context, version string) (map[string]string, error) {
	v, err := releaseorigin.ValidateReleaseTag(strings.TrimSpace(version))
	if err != nil {
		return nil, fmt.Errorf("invalid release tag: %w", err)
	}
	u := strings.TrimRight(releaseDownloadBase, "/") + "/" + url.PathEscape(v) + "/checksums.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch checksums: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	sums := parseReleaseChecksums(string(body))
	if len(sums) == 0 {
		return nil, fmt.Errorf("checksums.txt for %s parsed to zero entries", v)
	}
	return sums, nil
}
