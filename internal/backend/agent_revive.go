package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Dashboard "Revive via AWG Manager". A router goes dark when its backend domain
// changes: the agent keeps dialing the old backend.url and never polls the
// current backend, so no agent-channel command can reach it. But the router's
// awg-manager is a separate daemon, reachable over its public DDNS, and the VPS
// already ships a Python relay (awgm-relay.py, installed by the wizard's
// deferred-awgm deploy) that drives the awg-manager terminal. So the backend can
// revive a dark router out-of-band: run a script through that terminal to rewrite
// backend.url and restart the agent.
//
// The router root password (terminal login) is NOT stored — the operator supplies
// it per-click. So a leaked dashboard session alone cannot revive anything; it's
// a step-up over the dashboard token.

const defaultAWGMRelayPath = "/usr/local/lib/wg-monitor/awgm-relay.py"

type dashboardReviveReq struct {
	RootPassword  string `json:"root_password"`
	AWGMLogin     string `json:"awgm_login"`
	AWGMPassword  string `json:"awgm_password"`
	AWGMAPIKey    string `json:"awgm_api_key"`
	NewBackendURL string `json:"new_backend_url"`
}

// awgmReviveJob is the subset of the relay's job config the revive path needs.
// The relay's default mode runs bootstrap_script via the awg-manager terminal
// and returns its output + rc (see awgm-relay.py run_bootstrap).
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

// runAWGMRelayJob writes the job to a 0600 temp file, runs the Python relay, and
// deletes the file (so the root password never persists). Package var so tests
// can swap in a fake without a real router/relay.
var runAWGMRelayJob = defaultRunAWGMRelayJob

func defaultRunAWGMRelayJob(ctx context.Context, relayPath string, job awgmReviveJob) (string, error) {
	if _, err := os.Stat(relayPath); err != nil {
		return "", fmt.Errorf("awg-manager relay not installed at %s — run a deferred-awgm deploy from the wizard once to install it", relayPath)
	}
	body, err := json.Marshal(job)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "wg-monitor-revive-*.json")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "python3", relayPath, tmp).CombinedOutput()
	return string(out), err
}

// buildReviveScript returns a /bin/sh script that rewrites backend.url in the
// router's config.yaml to newBackendURL (section-aware awk, comment- and
// token-preserving, with a backup) and restarts the agent. Run on the router via
// the awg-manager terminal, so the agent does not need to be reachable.
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
chmod 600 "$CFG"
echo "backend.url -> $NEW"
/opt/etc/init.d/S99wg-monitor restart
echo "agent restarted"
`
}

// shellSingleQuote wraps s in single quotes, escaping embedded single quotes,
// so it is safe to splice into a /bin/sh script.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func dashboardReviveAgentHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if d.DB == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "db_not_configured", "db not configured")
			return
		}
		nickname := r.PathValue("nickname")
		if nickname == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "nickname required")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req dashboardReviveReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.RootPassword = strings.TrimSpace(req.RootPassword)
		if req.RootPassword == "" {
			writeJSONError(w, http.StatusBadRequest, "root_password_required",
				"router root password is required (it is used once for the terminal login and never stored)")
			return
		}
		newURL, err := reviveNewBackendURL(req.NewBackendURL, d.PublicBaseURL)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_backend_url", err.Error())
			return
		}
		user, err := d.DB.Users().GetByNickname(nickname)
		if err != nil {
			if errors.Is(err, db.ErrUserNotFound) {
				writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if user == nil {
			writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
			return
		}
		awgmURL := strings.TrimSpace(stringValue(user.AWGMURL))
		if err := validateDashboardAWGMURL(awgmURL); err != nil || awgmURL == "" {
			writeJSONError(w, http.StatusBadRequest, "no_awgm_url",
				"agent has no AWG Manager URL — revive runs over the awg-manager terminal, set awgm_url first (Edit settings)")
			return
		}
		relayPath := d.AWGMRelayPath
		if relayPath == "" {
			relayPath = defaultAWGMRelayPath
		}
		job := awgmReviveJob{
			BaseURL:          awgmURL,
			APIKey:           strings.TrimSpace(req.AWGMAPIKey),
			Login:            strings.TrimSpace(req.AWGMLogin),
			Password:         req.AWGMPassword,
			TerminalUser:     "root",
			TerminalPassword: req.RootPassword,
			BootstrapScript:  buildReviveScript(newURL),
			Mode:             "",
		}
		output, runErr := runAWGMRelayJob(r.Context(), relayPath, job)
		if runErr != nil {
			if d.Logger != nil {
				d.Logger.Warn("dashboard revive failed", "nickname", nickname, "err", runErr)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(struct {
				OK     bool   `json:"ok"`
				Error  string `json:"error"`
				Output string `json:"output"`
				NewURL string `json:"new_backend_url"`
			}{OK: false, Error: runErr.Error(), Output: output, NewURL: newURL})
			return
		}
		if d.Logger != nil {
			d.Logger.Info("dashboard revive ok", "nickname", nickname, "new_backend_url", newURL)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			OK     bool   `json:"ok"`
			Output string `json:"output"`
			NewURL string `json:"new_backend_url"`
		}{OK: true, Output: output, NewURL: newURL})
	}
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
