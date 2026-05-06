package callbacks

import (
	"regexp"
	"strings"
)

var validTunnelNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

func isValidTunnelName(s string) bool { return validTunnelNameRe.MatchString(s) }

func sanitizeTunnelName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
