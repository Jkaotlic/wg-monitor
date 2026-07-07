package backend

import (
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/internal/backend/provision"
)

// This file holds the revive/repoint support the provisioning engine's
// repair-repoint path (dashboardHandleRepairRepoint, provision_handler.go)
// depends on: the /bin/sh script that rewrites backend.url and restarts the
// agent (buildReviveScript), the shell-quoting helper it uses
// (shellSingleQuote), and the URL the script should target
// (reviveNewBackendURL). Running that script over the awg-manager terminal —
// including self-provisioning the embedded Python relay when no wizard
// install exists on disk — is now the engine's own job: internal/backend/
// provision/relay.go (DefaultRelay/resolveRelayScript). The synchronous
// versions that used to live in this file (runAWGMRelayJob, resolveRelayScript,
// runRelayProcess) and the standalone POST .../revive route
// (dashboardReviveAgentHandler) were removed once the dashboard switched to
// that async engine (Task 14).

const defaultAWGMRelayPath = "/usr/local/lib/wg-monitor/awgm-relay.py"

// awgmReviveJob is the subset of the relay's job config the revive/repoint
// path needs. The relay's default mode runs bootstrap_script via the
// awg-manager terminal and returns its output + rc (see awgm-relay.py
// run_bootstrap). Assembled by dashboardHandleRepairRepoint
// (provision_handler.go) and handed to the provisioning engine.
type awgmReviveJob struct {
	BaseURL          string `json:"base_url"`
	APIKey           string `json:"api_key,omitempty"`
	Login            string `json:"login,omitempty"`
	Password         string `json:"password,omitempty"`
	TerminalUser     string `json:"terminal_user"`
	TerminalPassword string `json:"terminal_password"`
	BootstrapScript  string `json:"bootstrap_script"`
	Mode             string `json:"mode"`
}

// buildReviveScript returns a /bin/sh script that rewrites backend.url in the
// router's config.yaml to newBackendURL (section-aware awk, comment- and
// token-preserving, with a backup) and restarts the agent. Run on the router via
// the awg-manager terminal, so the agent does not need to be reachable.
//
// This is the script the NEW repair-repoint flow runs (dashboardHandleRepairRepoint,
// provision_handler.go), whose job checklist is terminal_connected ->
// backend_url_rewritten -> service_restarted -> verify_online:
// terminal_connected is emitted by the Python relay itself (Task 7) once it
// connects, and verify_online is resolved by the engine's own post-relay poll
// (runner.go), but nothing else ever emits the middle two — so this script
// must echo them itself, in order, or the dashboard's checklist stalls
// forever on a step nothing ever completes. Marker names are the exact
// provision.Step* constants (steps.go), not hand-typed literals, so this
// cannot silently drift from what ParseStepLine/the engine actually expects.
// Emitted via `echo` (matching Task 7's relay convention) rather than some
// other mechanism: a PTY echoes back the command line it just ran, and
// runner.go's onLine relies on that echoed line starting with "echo" (the
// builtin), not the marker itself, so ParseStepLine's strict-prefix match
// only ever fires on the real output line, never the reflected input.
func buildReviveScript(newBackendURL string) string {
	return `set -e
CFG=/opt/etc/wg-monitor/config.yaml
NEW=` + shellSingleQuote(newBackendURL) + `
[ -f "$CFG" ] || { echo "config.yaml missing on router"; exit 1; }
cp -p "$CFG" "$CFG.bak-revive-$(date +%Y%m%d%H%M%S)" 2>/dev/null || true
awk -v new="$NEW" '
/^[^ \t#]/ { inb = ($0 ~ /^backend:[ \t]*$/) ? 1 : 0 }
inb && !done && $0 ~ /^[ \t]+url:/ { match($0, /^[ \t]+/); printf "%surl: %s\n", substr($0, 1, RLENGTH), new; done=1; next }
{ print }
' "$CFG" > "$CFG.tmp" && mv "$CFG.tmp" "$CFG"
echo ` + provision.StepMarker + ` ` + provision.StepBackendURLRewrite + `
chmod 600 "$CFG"
echo "backend.url -> $NEW"
/opt/etc/init.d/S99wg-monitor restart
echo ` + provision.StepMarker + ` ` + provision.StepServiceRestarted + `
echo "agent restarted"
`
}

// shellSingleQuote wraps s in single quotes, escaping embedded single quotes,
// so it is safe to splice into a /bin/sh script.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// reviveNewBackendURL resolves the backend URL to write: the operator override
// when given, else the backend's own public base URL. Must be a public https URL.
func reviveNewBackendURL(override, publicBaseURL string) (string, error) {
	raw := strings.TrimSpace(override)
	if raw == "" {
		raw = strings.TrimSpace(publicBaseURL)
	}
	if raw == "" {
		return "", fmt.Errorf("no backend URL: pass new_backend_url or configure PublicBaseURL")
	}
	normalized, err := sanitizeWizardBackendURL(raw)
	if err != nil {
		return "", err
	}
	return normalized, nil
}
