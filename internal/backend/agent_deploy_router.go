package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// awgmInstallJob drives the relay's bootstrap_install mode: a first-time agent
// install over the AWG Manager terminal. Credentials are transient (0600 temp
// file, deleted after the run) — nothing here is persisted.
type awgmInstallJob struct {
	BaseURL          string            `json:"base_url"`
	APIKey           string            `json:"api_key,omitempty"`
	Login            string            `json:"login,omitempty"`
	Password         string            `json:"password,omitempty"`
	TerminalUser     string            `json:"terminal_user"`
	TerminalPassword string            `json:"terminal_password"`
	Mode             string            `json:"mode"`
	Nickname         string            `json:"nickname"`
	TargetVersion    string            `json:"target_version"`
	BackendURL       string            `json:"backend_url"`
	RawToken         string            `json:"raw_token"`
	ReleaseBase      string            `json:"release_base"`
	InitScript       string            `json:"init_script"`
	Checksums        map[string]string `json:"checksums"`
}

// runAWGMInstallJob runs the install relay. Package var so tests can stub it.
var runAWGMInstallJob = defaultRunAWGMInstallJob

func defaultRunAWGMInstallJob(ctx context.Context, relayPath string, job awgmInstallJob) (string, error) {
	body, err := json.Marshal(job)
	if err != nil {
		return "", err
	}
	return runRelayProcess(ctx, relayPath, body, 10*time.Minute)
}

type dashboardDeployRouterReq struct {
	RootPassword string `json:"root_password"`
	AWGMLogin    string `json:"awgm_login"`
	AWGMPassword string `json:"awgm_password"`
	AWGMAPIKey   string `json:"awgm_api_key"`
	Version      string `json:"version"`
	// AllowDowngrade overrides the anti-downgrade rejection the shared engine
	// core (runProvisionInstallCore) now applies to this route — the old
	// synchronous handler had no such check at all. Omitted/false preserves
	// the safer default (reject); this field exists so an operator who
	// legitimately wants to roll back via this route still can.
	AllowDowngrade bool `json:"allow_downgrade"`
}

// dashboardDeployRouterHandler performs a first-time agent install on a
// router from the dashboard. It is a thin adapter (Task 10) over the same
// bootstrap_install engine core dashboardHandleProvisionInstall uses
// (runProvisionInstallCore, provision_handler.go): signature-verified
// checksums, mint-under-lock, engine.Start — returning ONLY {job_id, steps},
// never the old raw {ok, output, version}. This closes the P0 vuln (unsigned
// checksums / raw token in the response / request-scoped context) that this
// still-registered route carried before.
//
// Unlike dashboardHandleProvisionInstall (which can register a brand-new
// nickname), this route always targets an already-enrolled one: awgm_url and
// awgm_auth come from the stored row, not the request body, and the
// Telegram topic is left untouched (UpdateTopic: false — this request shape
// has no telegram_group/thread_id fields to assign from).
func dashboardDeployRouterHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if d.DB == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "db_not_configured", "db not configured")
			return
		}
		if d.Provision.Store == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "provision_not_configured", "provisioning engine not configured")
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
		var req dashboardDeployRouterReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.RootPassword = strings.TrimSpace(req.RootPassword)
		if req.RootPassword == "" {
			writeJSONError(w, http.StatusBadRequest, "root_password_required",
				"router root password is required (used once for the terminal login, never stored)")
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
		awgmURL := strings.TrimSpace(stringValue(user.AWGMURL))
		if err := validateDashboardAWGMURL(awgmURL); err != nil || awgmURL == "" {
			writeJSONError(w, http.StatusBadRequest, "no_awgm_url",
				"agent has no AWG Manager URL — set awgm_url first (Edit settings)")
			return
		}

		runProvisionInstallCore(w, r, d, provisionInstallCoreParams{
			Nickname:       nickname,
			AgentKind:      user.Kind,
			ThreadID:       int64Value(user.TelegramThreadID),
			AWGMURL:        awgmURL,
			AWGMAuth:       stringValue(user.AWGMAuth),
			RootPassword:   req.RootPassword,
			AWGMLogin:      strings.TrimSpace(req.AWGMLogin),
			AWGMPassword:   req.AWGMPassword,
			AWGMAPIKey:     strings.TrimSpace(req.AWGMAPIKey),
			Version:        strings.TrimSpace(req.Version),
			AllowDowngrade: req.AllowDowngrade,
			Existing:       user,
			UpdateTopic:    false,
		})
	}
}
