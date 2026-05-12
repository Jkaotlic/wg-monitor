package backend

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// WizardAuthMiddleware gates /v1/wizard/* endpoints with a constant-time
// compare against the loaded wizard token. Empty `expected` is a bug —
// callers must check cfg.Wizard.Token != "" BEFORE wiring this middleware
// (the route registration in NewMux enforces that).
func WizardAuthMiddleware(expected string, logger *slog.Logger) func(http.Handler) http.Handler {
	logReject := func(r *http.Request, reason string) {
		if logger == nil {
			return
		}
		logger.Warn("wizard auth: rejected",
			"reason", reason, "remote", r.RemoteAddr, "path", r.URL.Path,
		)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(hdr, prefix) {
				logReject(r, "missing-bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			presented := strings.TrimPrefix(hdr, prefix)
			if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
				logReject(r, "token-mismatch")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// wizardAgent is the JSON shape returned to the wizard. NULL DB fields are
// emitted as empty/zero values (omitempty would hide them — we want explicit
// nulls visible so the wizard knows "not yet pushed").
type wizardAgent struct {
	Nickname            string `json:"nickname"`
	Kind                string `json:"kind"`
	ThreadID            int64  `json:"thread_id"`
	SSHHost             string `json:"ssh_host"`
	SSHPort             int64  `json:"ssh_port"`
	SSHUser             string `json:"ssh_user"`
	Arch                string `json:"arch"`
	LastDeployedVersion string `json:"last_deployed_version"`
	HasTopic            bool   `json:"has_topic"`
}

type wizardAgentList struct {
	Agents []wizardAgent `json:"agents"`
}

// wizardListAgentsHandler returns the full fleet as the wizard sees it.
// Read-only; safe to call as often as the wizard wants.
func wizardListAgentsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		users, err := d.DB.Users().GetAll()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		out := wizardAgentList{Agents: make([]wizardAgent, 0, len(users))}
		for _, u := range users {
			a := wizardAgent{
				Nickname: u.Nickname,
				Kind:     u.Kind,
				HasTopic: u.TelegramThreadID != nil,
			}
			if u.TelegramThreadID != nil {
				a.ThreadID = *u.TelegramThreadID
			}
			if u.SSHHost != nil {
				a.SSHHost = *u.SSHHost
			}
			if u.SSHPort != nil {
				a.SSHPort = *u.SSHPort
			}
			if u.SSHUser != nil {
				a.SSHUser = *u.SSHUser
			}
			if u.Arch != nil {
				a.Arch = *u.Arch
			}
			if u.LastDeployedVersion != nil {
				a.LastDeployedVersion = *u.LastDeployedVersion
			}
			out.Agents = append(out.Agents, a)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(out)
	}
}
