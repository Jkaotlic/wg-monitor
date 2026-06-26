package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/installtmpl"
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
}

// dashboardDeployRouterHandler performs a first-time agent install on a router
// from the dashboard: it re-mints the enrollment token, resolves the target
// version + release checksums, and drives the relay's bootstrap_install mode
// with operator-supplied, never-stored credentials.
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

		version := strings.TrimSpace(req.Version)
		if version == "" {
			version, err = lookupDashboardLatestVersion(r.Context())
			if err != nil {
				writeJSONError(w, http.StatusBadGateway, "latest_version_failed", err.Error())
				return
			}
		}
		sums, err := releaseChecksumsFetcher(r.Context(), version)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "checksums_failed", err.Error())
			return
		}

		backendURL := strings.TrimRight(strings.TrimSpace(d.PublicBaseURL), "/")
		if backendURL == "" {
			writeJSONError(w, http.StatusInternalServerError, "no_public_base_url",
				"backend PublicBaseURL is not configured")
			return
		}

		// Re-mint the enrollment token: the DB only keeps the hash, and a fresh
		// config.yaml needs the raw token. Idempotent (UpsertEnrollment upserts).
		enrollment, _, err := createAgentEnrollment(d.DB, nickname, user.Kind, int64Value(user.TelegramThreadID))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}

		relayPath := d.AWGMRelayPath
		if relayPath == "" {
			relayPath = defaultAWGMRelayPath
		}
		job := awgmInstallJob{
			BaseURL:          awgmURL,
			APIKey:           strings.TrimSpace(req.AWGMAPIKey),
			Login:            strings.TrimSpace(req.AWGMLogin),
			Password:         req.AWGMPassword,
			TerminalUser:     "root",
			TerminalPassword: req.RootPassword,
			Mode:             "bootstrap_install",
			Nickname:         nickname,
			TargetVersion:    version,
			BackendURL:       backendURL,
			RawToken:         enrollment.RawToken,
			ReleaseBase:      backendURL + "/v1/releases/download",
			InitScript:       installtmpl.InitScript(),
			Checksums:        sums,
		}
		output, runErr := runAWGMInstallJob(r.Context(), relayPath, job)
		if runErr != nil {
			if d.Logger != nil {
				d.Logger.Warn("dashboard deploy-router failed", "nickname", nickname, "version", version, "err", runErr)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(struct {
				OK      bool   `json:"ok"`
				Error   string `json:"error"`
				Output  string `json:"output"`
				Version string `json:"version"`
			}{OK: false, Error: runErr.Error(), Output: output, Version: version})
			return
		}

		// Record the successful install (merge-safe: preserve existing fields).
		info := db.DeployInfo{
			Kind:                user.Kind,
			ThreadID:            int64Value(user.TelegramThreadID),
			SSHHost:             stringValue(user.SSHHost),
			SSHPort:             int64Value(user.SSHPort),
			SSHUser:             stringValue(user.SSHUser),
			Arch:                stringValue(user.Arch),
			Ring:                stringValue(user.Ring),
			DeployMode:          "awgm",
			AWGMURL:             awgmURL,
			AWGMAuth:            stringValue(user.AWGMAuth),
			ExpectedMAC:         stringValue(user.ExpectedMAC),
			LastDeployedVersion: version,
			LastDeploy:          time.Now().UTC().Format(time.RFC3339),
		}
		if err := d.DB.Users().UpdateDeployInfo(nickname, info); err != nil && d.Logger != nil {
			d.Logger.Warn("deploy-router: UpdateDeployInfo failed", "nickname", nickname, "err", err)
		}
		if d.Logger != nil {
			d.Logger.Info("dashboard deploy-router ok", "nickname", nickname, "version", version)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			OK      bool   `json:"ok"`
			Output  string `json:"output"`
			Version string `json:"version"`
		}{OK: true, Output: output, Version: version})
	}
}
