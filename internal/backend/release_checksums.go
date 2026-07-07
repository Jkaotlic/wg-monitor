package backend

import "strings"

// parseReleaseChecksums parses GitHub's "checksums.txt" ("<sha256>␠␠<file>"
// lines) into asset->lowercase-sha. Malformed lines are skipped.
//
// Still used by verifiedReleaseChecksums (release_verify.go) to parse the
// checksums.txt body once its signature is verified. Its old, unsigned
// caller — fetchReleaseChecksums/releaseChecksumsFetcher, which downloaded
// checksums.txt straight off releaseDownloadBase with no signature check at
// all — was removed (Task 14) once its only remaining exerciser, a
// deploy-router regression-pin test proving the new route did NOT call it,
// went away with that route.
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
