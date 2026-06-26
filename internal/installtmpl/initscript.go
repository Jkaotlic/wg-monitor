// Package installtmpl holds install-time templates shared between the deploy
// wizard (cmd/deploy) and the backend's dashboard "Deploy to router" relay.
package installtmpl

import (
	_ "embed"
	"strings"
)

//go:embed S99wg-monitor
var initScript string

// InitScript returns the S99wg-monitor Entware init script with the trailing
// newline trimmed, ready to splice into a relay bootstrap job.
func InitScript() string {
	return strings.TrimRight(initScript, "\n")
}
