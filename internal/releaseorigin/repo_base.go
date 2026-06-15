package releaseorigin

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

const (
	DefaultGitHubReleaseBase = "https://github.com/Jkaotlic/wg-monitor/releases/download"
	DefaultBackendMirrorBase = "https://wgmonitor.example.com/v1/releases/download"
)

var releaseTagRE = regexp.MustCompile(`^v[0-9][0-9A-Za-z._-]*$`)

// ValidateReleaseTag normalizes a release tag used as a download URL path
// segment. It intentionally accepts the project's v-prefixed semver/RC tags
// while rejecting separators, query delimiters, and unversioned strings.
func ValidateReleaseTag(raw string) (string, error) {
	tag := strings.TrimSpace(raw)
	if !releaseTagRE.MatchString(tag) {
		return "", fmt.Errorf("target_version %q is not a valid release tag", raw)
	}
	return tag, nil
}

// ValidateRepoBase normalizes repo_base and requires an exact match against a
// compile-time allowlist. Callers may pass a test-only allowlist, but
// production should use only the canonical GitHub origin and backend mirror.
func ValidateRepoBase(raw string, allowedBases []string) (string, error) {
	base, err := normalizeRepoBase(raw)
	if err != nil {
		return "", err
	}
	for _, allowed := range allowedBases {
		normalized, err := normalizeRepoBase(allowed)
		if err != nil {
			continue
		}
		if base == normalized {
			return base, nil
		}
	}
	return "", fmt.Errorf("repo_base %q is not an allowed release origin", raw)
}

func normalizeRepoBase(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return "", fmt.Errorf("repo_base is required")
	}
	u, err := url.Parse(base)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("repo_base %q is not an absolute URL", raw)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("repo_base %q must use https", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("repo_base %q must not include credentials, query, or fragment", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return "", fmt.Errorf("repo_base %q must not use localhost", raw)
	}
	if ip := net.ParseIP(host); ip != nil && privateOrLoopbackIP(ip) {
		return "", fmt.Errorf("repo_base %q must not use a private or loopback IP", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func privateOrLoopbackIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
