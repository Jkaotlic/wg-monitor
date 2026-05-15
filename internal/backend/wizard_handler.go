package backend

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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

type wizardPutAgentReq struct {
	SSHHost             string `json:"ssh_host"`
	SSHPort             int64  `json:"ssh_port"`
	SSHUser             string `json:"ssh_user"`
	Arch                string `json:"arch"`
	LastDeployedVersion string `json:"last_deployed_version"`
}

// wizardPutAgentHandler upserts deploy metadata into an existing users row.
// Route path is /v1/wizard/agents/{nickname} — Go 1.22+ ServeMux pattern.
func wizardPutAgentHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
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
		var req wizardPutAgentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "bad json: "+err.Error())
			return
		}
		if req.SSHHost == "" || req.SSHPort == 0 || req.SSHUser == "" || req.Arch == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON,
				"ssh_host, ssh_port, ssh_user, arch are required")
			return
		}
		err := d.DB.Users().UpdateDeployInfo(nickname, db.DeployInfo{
			SSHHost:             req.SSHHost,
			SSHPort:             req.SSHPort,
			SSHUser:             req.SSHUser,
			Arch:                req.Arch,
			LastDeployedVersion: req.LastDeployedVersion,
		})
		if err != nil {
			if errors.Is(err, db.ErrUserNotFound) {
				writeJSONError(w, http.StatusNotFound, "user_not_found",
					"nickname not registered — run actionAddRouter first")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// wizardDeployReq is the body POSTed to /v1/wizard/agents/{nickname}/deploy.
// target_version must be a published release tag — the agent passes it
// straight to GitHub Releases, so a typo here surfaces as a 404 download on
// the router rather than getting validated server-side.
type wizardDeployReq struct {
	TargetVersion string `json:"target_version"`
}

type wizardDeployResp struct {
	CmdID string `json:"cmd_id"`
}

// wizardDeployHandler enqueues a self_update command for an agent through
// the existing /v1/cmd long-poll channel. Returns 202 with the command ID
// the wizard then polls via /v1/wizard/cmd/{id}?nickname=. Does not block
// on the agent's response — the wizard is expected to follow up with the
// cmd-result endpoint, then poll /v1/wizard/agents for the heartbeat-side
// version flip.
func wizardDeployHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if d.CommandSink == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "command sink not configured")
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
		var req wizardDeployReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "bad json: "+err.Error())
			return
		}
		if req.TargetVersion == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "target_version required")
			return
		}

		u, err := d.DB.Users().GetByNickname(nickname)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if u == nil {
			writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
			return
		}

		id, err := newCmdID()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "id gen: "+err.Error())
			return
		}
		cmd := wire.Command{
			ID:       id,
			Action:   "self_update",
			Args:     map[string]any{"version": req.TargetVersion},
			IssuedAt: time.Now().UTC(),
		}
		if err := d.CommandSink.Enqueue(u.ID, cmd); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "enqueue: "+err.Error())
			return
		}
		if d.Logger != nil {
			d.Logger.Info("wizard deploy enqueued",
				"nickname", nickname, "user_id", u.ID, "cmd_id", id, "target_version", req.TargetVersion)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(wizardDeployResp{CmdID: id})
	}
}

// wizardCmdResultHandler returns the agent's CommandResult for a previously
// enqueued command. ?nickname= names the agent; ?wait_sec= bounds how long
// to long-poll AwaitResult (clamped to [0, 60], default 30). 404 on
// timeout so the caller can retry without re-issuing the command.
func wizardCmdResultHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if d.CommandSink == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "command sink not configured")
			return
		}
		cmdID := r.PathValue("cmd_id")
		if cmdID == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "cmd_id required")
			return
		}
		nickname := r.URL.Query().Get("nickname")
		if nickname == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "nickname query param required")
			return
		}
		wait := 30
		if w := r.URL.Query().Get("wait_sec"); w != "" {
			if n, err := strconv.Atoi(w); err == nil {
				wait = n
			}
		}
		if wait < 0 {
			wait = 0
		}
		if wait > 60 {
			wait = 60
		}

		u, err := d.DB.Users().GetByNickname(nickname)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if u == nil {
			writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
			return
		}

		res, ok := d.CommandSink.AwaitResult(r.Context(), u.ID, cmdID, time.Duration(wait)*time.Second)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "result_not_ready", "no result yet — poll again")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(res)
	}
}

// newCmdID returns a hex-encoded 16-byte random ID suitable for wire.Command.ID.
// Not a real UUID — the wire layer only requires non-empty uniqueness, and a
// raw hex string is friendlier to grep through journalctl than the dashed form.
func newCmdID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
