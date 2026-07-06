package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/provision"
	"github.com/anex/wg-monitor/internal/installtmpl"
)

// Positive, fixed budgets handed to provision.StartReq. provision.Deps.Start
// never defends against a zero/negative budget (a non-positive
// context.WithTimeout expires instantly, which would misreport a healthy run
// as "timed out at step X" — see runner.go's own doc + this plan's B6 review
// cross-task requirement). These match the provisioning-rework design spec's
// grounded defaults: a 10-minute install budget and a 120s verify-online
// budget (sized off the live fleet's ~68s median report interval).
const (
	provisionInstallBudget = 10 * time.Minute
	provisionVerifyBudget  = 120 * time.Second
)

// defaultProvisionTerminalUser is the only account the awg-manager terminal
// relay ever logs into — mirrors agent_deploy_router.go/agent_revive.go's
// existing "root" convention.
const defaultProvisionTerminalUser = "root"

// verifiedChecksumsFetcher wraps verifiedReleaseChecksums (release_verify.go,
// Task 1) so handler tests can stub the network fetch — same pattern as
// releaseChecksumsFetcher (release_checksums.go) for the older, unsigned
// path. Production code never reassigns this; every provision/reinstall path
// MUST go through the signature-verified helper, never the older unsigned
// fetchReleaseChecksums.
var verifiedChecksumsFetcher = verifiedReleaseChecksums

// dashboardProvisionReq is the body for POST /v1/dashboard/provision. Kind
// selects register (mint token + persist metadata, no relay) vs provision
// (install now: validate, verify checksums, mint, start the async job).
type dashboardProvisionReq struct {
	Kind           string `json:"kind"` // "register" | "provision"
	Nickname       string `json:"nickname"`
	AgentKind      string `json:"agent_kind"`
	TelegramGroup  int64  `json:"telegram_group"`
	ThreadID       int64  `json:"thread_id"`
	AWGMURL        string `json:"awgm_url"`
	AWGMAuth       string `json:"awgm_auth"`
	RootPassword   string `json:"root_password"`
	AWGMLogin      string `json:"awgm_login"`
	AWGMPassword   string `json:"awgm_password"`
	AWGMAPIKey     string `json:"awgm_api_key"`
	Version        string `json:"version"`
	AllowDowngrade bool   `json:"allow_downgrade"`
}

// dashboardRegisterResp is returned by the register path only: the raw
// enrollment token is shown to the operator exactly once, same as today's
// wizard/dashboard enrollment endpoints. JobID/Steps are best-effort (present
// only when the provisioning engine is wired) — an instant, already-"success"
// job so the dashboard can render register-only agents through the same
// polling code path as an install, without ever calling engine.Start.
type dashboardRegisterResp struct {
	OK         bool             `json:"ok"`
	Kind       string           `json:"kind"`
	Nickname   string           `json:"nickname"`
	RawToken   string           `json:"raw_token"`
	BackendURL string           `json:"backend_url"`
	JobID      string           `json:"job_id,omitempty"`
	Steps      []provision.Step `json:"steps,omitempty"`
}

// dashboardJobStartResp is the ONLY shape a provision-install or repair
// response ever carries: a job id plus its seeded step checklist. Never the
// raw relay transcript, never a token — the client polls
// GET /v1/dashboard/provision/{job_id} for progress.
type dashboardJobStartResp struct {
	JobID string           `json:"job_id"`
	Steps []provision.Step `json:"steps"`
}

// dashboardRepairReq is the body for POST /v1/dashboard/agents/{nickname}/repair.
type dashboardRepairReq struct {
	Mode           string `json:"mode"` // "repoint" | "reinstall"
	RootPassword   string `json:"root_password"`
	NewBackendURL  string `json:"new_backend_url"`
	Version        string `json:"version"`
	AWGMLogin      string `json:"awgm_login"`
	AWGMPassword   string `json:"awgm_password"`
	AWGMAPIKey     string `json:"awgm_api_key"`
	AllowDowngrade bool   `json:"allow_downgrade"`
}

// dashboardProvisionHandler serves POST /v1/dashboard/provision. The body's
// `kind` selects register (inline mint+persist, no engine involvement at
// all) or provision (validate, verify checksums, mint, start the async
// install job).
func dashboardProvisionHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if d.DB == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "db_not_configured", "db not configured")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req dashboardProvisionReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.Nickname = strings.TrimSpace(req.Nickname)

		switch strings.TrimSpace(req.Kind) {
		case string(provision.KindRegister):
			dashboardHandleRegister(w, r, d, req)
		case string(provision.KindProvision):
			dashboardHandleProvisionInstall(w, r, d, req)
		default:
			writeJSONError(w, http.StatusBadRequest, "invalid_provision_kind", `kind must be "register" or "provision"`)
		}
	}
}

// dashboardHandleRegister mints an enrollment token and persists the router's
// metadata — no relay call, no engine.Start (Template(KindRegister) is
// empty; register does not go through the job/progress machinery at all).
// The token is returned inline, exactly like today's wizard/dashboard
// enrollment endpoints.
func dashboardHandleRegister(w http.ResponseWriter, r *http.Request, d Deps, req dashboardProvisionReq) {
	existing, err := lookupExistingUser(d.DB, req.Nickname)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}

	enrollment, userID, err := createAgentEnrollment(d.DB, req.Nickname, req.AgentKind, req.ThreadID)
	if err != nil {
		writeProvisionEnrollmentError(w, err)
		return
	}
	if err := d.DB.Users().UpdateTelegramTopic(userID, req.TelegramGroup, req.ThreadID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}
	if err := d.DB.Users().UpdateDeployInfo(enrollment.Nickname, provisionDeployInfo(existing, req)); err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}

	resp := dashboardRegisterResp{
		OK:         true,
		Kind:       string(provision.KindRegister),
		Nickname:   enrollment.Nickname,
		RawToken:   enrollment.RawToken,
		BackendURL: wizardEnrollmentBackendURL(r, d.PublicBaseURL),
	}
	// Best-effort: an instant, already-terminal job so the dashboard can
	// render a register-only agent through the same polling code path as an
	// install, without register ever touching Relay/Start. Skipped cleanly
	// when the engine isn't wired — register must work standalone (it only
	// ever needs the DB).
	if d.Provision.Store != nil {
		job := d.Provision.Store.Create(provision.KindRegister, enrollment.Nickname, provision.Template(provision.KindRegister))
		d.Provision.Store.Update(job.ID, func(j *provision.Job) { j.State = provision.StateSuccess })
		resp.JobID = job.ID
		resp.Steps = job.Steps
	}

	if d.Logger != nil {
		d.Logger.Info("dashboard provision: register", "nickname", enrollment.Nickname)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

// dashboardHandleProvisionInstall validates the router-access fields, resolves
// + signature-verifies the target release, rejects a downgrade, mints an
// enrollment token, assembles the bootstrap_install relay job, and starts the
// async engine. Returns ONLY {job_id, steps} — never the token, never any
// relay output.
func dashboardHandleProvisionInstall(w http.ResponseWriter, r *http.Request, d Deps, req dashboardProvisionReq) {
	if d.Provision.Store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "provision_not_configured", "provisioning engine not configured")
		return
	}

	awgmURL := strings.TrimSpace(req.AWGMURL)
	if err := validateDashboardAWGMURL(awgmURL); err != nil || awgmURL == "" {
		writeJSONError(w, http.StatusBadRequest, "no_awgm_url", "awgm_url is required and must be an absolute http(s) URL")
		return
	}
	rootPassword := strings.TrimSpace(req.RootPassword)
	if rootPassword == "" {
		writeJSONError(w, http.StatusBadRequest, "root_password_required",
			"router root password is required (used once for the terminal login, never stored)")
		return
	}

	version, err := resolveProvisionVersion(r.Context(), req.Version)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "latest_version_failed", err.Error())
		return
	}

	sums, err := verifiedChecksumsFetcher(r.Context(), releaseDownloadBase, version)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "checksums_failed", err.Error())
		return
	}

	existing, err := lookupExistingUser(d.DB, req.Nickname)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}
	if existing != nil && !req.AllowDowngrade && isVersionDowngrade(version, stringValue(existing.LastDeployedVersion)) {
		writeJSONError(w, http.StatusBadRequest, "downgrade_rejected",
			fmt.Sprintf("target version %s is older than the currently installed %s — pass allow_downgrade to override",
				version, stringValue(existing.LastDeployedVersion)))
		return
	}

	backendURL := strings.TrimRight(strings.TrimSpace(d.PublicBaseURL), "/")
	if backendURL == "" {
		writeJSONError(w, http.StatusInternalServerError, "no_public_base_url", "backend PublicBaseURL is not configured")
		return
	}

	enrollment, userID, err := createAgentEnrollment(d.DB, req.Nickname, req.AgentKind, req.ThreadID)
	if err != nil {
		writeProvisionEnrollmentError(w, err)
		return
	}
	if err := d.DB.Users().UpdateTelegramTopic(userID, req.TelegramGroup, req.ThreadID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}
	req.AWGMURL = awgmURL
	if err := d.DB.Users().UpdateDeployInfo(enrollment.Nickname, provisionDeployInfo(existing, req)); err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}

	job := awgmInstallJob{
		BaseURL:          awgmURL,
		APIKey:           strings.TrimSpace(req.AWGMAPIKey),
		Login:            strings.TrimSpace(req.AWGMLogin),
		Password:         req.AWGMPassword,
		TerminalUser:     defaultProvisionTerminalUser,
		TerminalPassword: rootPassword,
		Mode:             "bootstrap_install",
		Nickname:         enrollment.Nickname,
		TargetVersion:    version,
		BackendURL:       backendURL,
		RawToken:         enrollment.RawToken,
		ReleaseBase:      backendURL + "/v1/releases/download",
		InitScript:       installtmpl.InitScript(),
		Checksums:        sums,
	}
	jobJSON, err := json.Marshal(job)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}

	jobID, err := d.Provision.Start(provision.StartReq{
		Kind:          provision.KindProvision,
		Nickname:      enrollment.Nickname,
		RelayPath:     resolvedRelayPath(d),
		JobJSON:       jobJSON,
		RawToken:      enrollment.RawToken,
		Version:       version,
		InstallBudget: provisionInstallBudget,
		VerifyBudget:  provisionVerifyBudget,
	})
	if err != nil {
		writeProvisionStartError(w, err)
		return
	}
	if d.Logger != nil {
		d.Logger.Info("dashboard provision: install started", "nickname", enrollment.Nickname, "job_id", jobID, "version", version)
	}
	respondProvisionJobStarted(w, d, jobID, provision.KindProvision)
}

// dashboardProvisionPollHandler serves GET /v1/dashboard/provision/{job_id}:
// the client's poll target while a provision/repair job runs. 404 on an
// unknown id (never existed, or already evicted by the store's TTL sweep).
func dashboardProvisionPollHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if d.Provision.Store == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "provision_not_configured", "provisioning engine not configured")
			return
		}
		jobID := r.PathValue("job_id")
		if jobID == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "job_id required")
			return
		}
		job, ok := d.Provision.Store.Get(jobID)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "job_not_found", "unknown job id")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(job)
	}
}

// dashboardRepairHandler serves POST /v1/dashboard/agents/{nickname}/repair
// for an already-enrolled agent: mode "repoint" rewrites backend.url and
// restarts; "reinstall" re-runs the full signature-verified bootstrap_install
// against the existing nickname (fresh token, same as provision-install).
func dashboardRepairHandler(d Deps) http.HandlerFunc {
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
		var req dashboardRepairReq
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
		if user == nil {
			writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
			return
		}

		switch strings.TrimSpace(req.Mode) {
		case "repoint":
			dashboardHandleRepairRepoint(w, r, d, nickname, user, req)
		case "reinstall":
			dashboardHandleRepairReinstall(w, r, d, nickname, user, req)
		default:
			writeJSONError(w, http.StatusBadRequest, "invalid_repair_mode", `mode must be "repoint" or "reinstall"`)
		}
	}
}

// dashboardHandleRepairRepoint rewrites the router's backend.url and restarts
// the agent over the awg-manager terminal — no reinstall, no fresh token (the
// router keeps the enrollment it already has on disk).
func dashboardHandleRepairRepoint(w http.ResponseWriter, r *http.Request, d Deps, nickname string, user *db.User, req dashboardRepairReq) {
	newURL, err := reviveNewBackendURL(req.NewBackendURL, d.PublicBaseURL)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_backend_url", err.Error())
		return
	}
	awgmURL := strings.TrimSpace(stringValue(user.AWGMURL))
	if err := validateDashboardAWGMURL(awgmURL); err != nil || awgmURL == "" {
		writeJSONError(w, http.StatusBadRequest, "no_awgm_url",
			"agent has no AWG Manager URL — repoint runs over the awg-manager terminal, set awgm_url first")
		return
	}

	job := awgmReviveJob{
		BaseURL:          awgmURL,
		APIKey:           strings.TrimSpace(req.AWGMAPIKey),
		Login:            strings.TrimSpace(req.AWGMLogin),
		Password:         req.AWGMPassword,
		TerminalUser:     defaultProvisionTerminalUser,
		TerminalPassword: req.RootPassword,
		BootstrapScript:  buildReviveScript(newURL),
		Mode:             "", // default relay mode: run_bootstrap over the terminal
	}
	jobJSON, err := json.Marshal(job)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}

	jobID, err := d.Provision.Start(provision.StartReq{
		Kind:          provision.KindRepairRepoint,
		Nickname:      nickname,
		RelayPath:     resolvedRelayPath(d),
		JobJSON:       jobJSON,
		InstallBudget: provisionInstallBudget,
		VerifyBudget:  provisionVerifyBudget,
	})
	if err != nil {
		writeProvisionStartError(w, err)
		return
	}
	if d.Logger != nil {
		d.Logger.Info("dashboard repair: repoint started", "nickname", nickname, "job_id", jobID, "new_backend_url", newURL)
	}
	respondProvisionJobStarted(w, d, jobID, provision.KindRepairRepoint)
}

// dashboardHandleRepairReinstall re-runs the full signature-verified
// bootstrap_install against an already-enrolled nickname: same shape as
// provision-install, but awgm_url comes from the stored row (the repair
// request carries no awgm_url of its own) and the enrollment token is
// re-minted fresh.
func dashboardHandleRepairReinstall(w http.ResponseWriter, r *http.Request, d Deps, nickname string, user *db.User, req dashboardRepairReq) {
	awgmURL := strings.TrimSpace(stringValue(user.AWGMURL))
	if err := validateDashboardAWGMURL(awgmURL); err != nil || awgmURL == "" {
		writeJSONError(w, http.StatusBadRequest, "no_awgm_url",
			"agent has no AWG Manager URL — reinstall runs over the awg-manager terminal, set awgm_url first")
		return
	}

	version, err := resolveProvisionVersion(r.Context(), req.Version)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "latest_version_failed", err.Error())
		return
	}
	// Anti-downgrade before the checksums fetch (the reverse order from
	// dashboardHandleProvisionInstall, which follows the brief's literal
	// provision-install sequence): user.LastDeployedVersion is already in
	// hand here (dashboardRepairHandler's shared GetByNickname), so rejecting
	// a downgrade costs nothing extra, whereas provision-install has no
	// existing row to check against until later. Reinstall targets an
	// already-enrolled nickname, where a downgrade attempt (e.g. rolling back
	// a bad deploy without the explicit override) is the realistic case this
	// guards, so failing fast here also skips a wasted GitHub round trip.
	if !req.AllowDowngrade && isVersionDowngrade(version, stringValue(user.LastDeployedVersion)) {
		writeJSONError(w, http.StatusBadRequest, "downgrade_rejected",
			fmt.Sprintf("target version %s is older than the currently installed %s — pass allow_downgrade to override",
				version, stringValue(user.LastDeployedVersion)))
		return
	}

	sums, err := verifiedChecksumsFetcher(r.Context(), releaseDownloadBase, version)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "checksums_failed", err.Error())
		return
	}

	backendURL := strings.TrimRight(strings.TrimSpace(d.PublicBaseURL), "/")
	if backendURL == "" {
		writeJSONError(w, http.StatusInternalServerError, "no_public_base_url", "backend PublicBaseURL is not configured")
		return
	}

	// Re-mint the enrollment token: the DB only keeps the hash, and a fresh
	// config.yaml needs the raw token (mirrors dashboardDeployRouterHandler's
	// existing re-mint-on-every-install convention). Idempotent — UpsertEnrollment upserts.
	enrollment, _, err := createAgentEnrollment(d.DB, nickname, user.Kind, int64Value(user.TelegramThreadID))
	if err != nil {
		writeProvisionEnrollmentError(w, err)
		return
	}

	job := awgmInstallJob{
		BaseURL:          awgmURL,
		APIKey:           strings.TrimSpace(req.AWGMAPIKey),
		Login:            strings.TrimSpace(req.AWGMLogin),
		Password:         req.AWGMPassword,
		TerminalUser:     defaultProvisionTerminalUser,
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
	jobJSON, err := json.Marshal(job)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}

	jobID, err := d.Provision.Start(provision.StartReq{
		Kind:          provision.KindRepairReinstall,
		Nickname:      nickname,
		RelayPath:     resolvedRelayPath(d),
		JobJSON:       jobJSON,
		RawToken:      enrollment.RawToken,
		Version:       version,
		InstallBudget: provisionInstallBudget,
		VerifyBudget:  provisionVerifyBudget,
	})
	if err != nil {
		writeProvisionStartError(w, err)
		return
	}
	if d.Logger != nil {
		d.Logger.Info("dashboard repair: reinstall started", "nickname", nickname, "job_id", jobID, "version", version)
	}
	respondProvisionJobStarted(w, d, jobID, provision.KindRepairReinstall)
}

// respondProvisionJobStarted writes the {job_id, steps} response shared by
// provision-install and both repair modes. It reads the freshly-created Job
// back from the Store when possible (the just-started worker may already
// have advanced a step by the time this runs) and falls back to the kind's
// static template otherwise.
func respondProvisionJobStarted(w http.ResponseWriter, d Deps, jobID string, kind provision.JobKind) {
	steps := provision.Template(kind)
	if stored, ok := d.Provision.Store.Get(jobID); ok {
		steps = stored.Steps
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(dashboardJobStartResp{JobID: jobID, Steps: steps})
}

// resolvedRelayPath returns the awgm-relay.py path to hand the engine: a
// wizard-installed override when configured, else the well-known path a
// wizard install would have used — mirrors agent_deploy_router.go/
// agent_revive.go's identical fallback. provision.resolveRelayScript (its own
// copy, see relay.go) still self-provisions the embedded relay to a temp file
// whenever neither path exists on disk, so this is a pure preference, never a
// hard requirement.
func resolvedRelayPath(d Deps) string {
	if d.AWGMRelayPath != "" {
		return d.AWGMRelayPath
	}
	return defaultAWGMRelayPath
}

// resolveProvisionVersion returns the operator-requested version, trimmed, or
// falls back to the latest published stable release.
func resolveProvisionVersion(ctx context.Context, requested string) (string, error) {
	version := strings.TrimSpace(requested)
	if version != "" {
		return version, nil
	}
	return lookupDashboardLatestVersion(ctx)
}

// isVersionDowngrade reports whether target ranks strictly below current in
// this project's own release-tag order (major.minor.patch, then RC number —
// see compareDashboardReleaseTags/parseDashboardReleaseTagRank in
// dashboard_handler.go, already used to rank the GitHub releases feed).
//
// This deliberately does NOT reuse upstream.SoftwareNewerThan: that helper's
// golang.org/x/mod/semver-backed prerelease compare treats a whole "-rcN"
// suffix as one opaque ASCII-lexical identifier (no dot separator to split
// "rc" from the number), so "rc9" sorts AFTER "rc10" — '9' (0x39) is a
// greater byte than '1' (0x31). That is the wrong order for a fleet that has
// actually shipped double- and triple-digit RC tags (rc133 seen in
// production — see project memory). compareDashboardReleaseTags parses the
// RC suffix as an integer instead, so rc9 < rc10 correctly, and reusing it
// keeps "which version is newer" a single already-tested source of truth
// shared with "latest available" resolution, rather than a second, subtly
// different implementation of the same question.
//
// An empty target or current is never a downgrade — there is nothing to
// compare against (a brand-new nickname has no LastDeployedVersion yet), and
// this must never produce a false-positive rejection.
func isVersionDowngrade(target, current string) bool {
	target = strings.TrimSpace(target)
	current = strings.TrimSpace(current)
	if target == "" || current == "" {
		return false
	}
	return compareDashboardReleaseTags(target, current) < 0
}

// provisionDeployInfo builds the DeployInfo to persist for a register/provision
// request. current is the row as it stood before this request (nil for a
// brand-new nickname) — system-managed fields (versions, pending markers) and
// metadata this request shape has no fields for (ssh/ring/arch/mac; the
// engine auto-detects arch and always uses the awgm/relay path, per the
// provisioning-rework design spec) are preserved from it rather than blanked,
// matching dashboardEditDeployInfo/dashboardDeployInfoFromEnrollmentReq's
// existing merge-safe convention.
func provisionDeployInfo(current *db.User, req dashboardProvisionReq) db.DeployInfo {
	info := db.DeployInfo{
		Kind:       req.AgentKind,
		ThreadID:   req.ThreadID,
		AWGMURL:    strings.TrimSpace(req.AWGMURL),
		AWGMAuth:   strings.TrimSpace(req.AWGMAuth),
		DeployMode: "awgm",
	}
	if current == nil {
		return info
	}
	if info.AWGMURL == "" {
		info.AWGMURL = stringValue(current.AWGMURL)
	}
	if info.AWGMAuth == "" {
		info.AWGMAuth = stringValue(current.AWGMAuth)
	}
	info.SSHHost = stringValue(current.SSHHost)
	info.SSHPort = int64Value(current.SSHPort)
	info.SSHUser = stringValue(current.SSHUser)
	info.Arch = stringValue(current.Arch)
	info.Ring = stringValue(current.Ring)
	info.ExpectedMAC = stringValue(current.ExpectedMAC)
	info.LastDeployedVersion = stringValue(current.LastDeployedVersion)
	info.PendingVersion = stringValue(current.PendingVersion)
	info.PendingSince = stringValue(current.PendingSince)
	info.LastDeploy = stringValue(current.LastDeploy)
	return info
}

// lookupExistingUser is a nil-friendly GetByNickname: db.ErrUserNotFound
// collapses to (nil, nil) rather than propagating as an error, since "no row
// yet" is the expected, non-exceptional case for a brand-new provision.
func lookupExistingUser(database *db.DB, nickname string) (*db.User, error) {
	u, err := database.Users().GetByNickname(nickname)
	if err != nil {
		if errors.Is(err, db.ErrUserNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

// writeProvisionEnrollmentError maps createAgentEnrollment's sentinel errors
// to the same 400 codes the existing wizard/dashboard enrollment endpoints
// use — kept name-compatible with the request field this endpoint actually
// has (agent_kind, not kind — kind is the register/provision selector here).
func writeProvisionEnrollmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errEnrollmentInvalidNickname):
		writeJSONError(w, http.StatusBadRequest, "invalid_nickname", "nickname must match ^[a-z][a-z0-9_-]{1,15}$")
	case errors.Is(err, errEnrollmentInvalidKind):
		writeJSONError(w, http.StatusBadRequest, "invalid_kind", "agent_kind must be static or mobile")
	default:
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
	}
}

// writeProvisionStartError maps provision.Deps.Start's only documented
// failure mode (ErrAlreadyRunning) to 409 Conflict, handing its already
// operator-friendly Error() text straight through — the same convention
// agent_revive.go's dashboardReviveAgentHandler uses for a relay error's
// Error() text today (see runner.go's ErrAlreadyRunning doc).
func writeProvisionStartError(w http.ResponseWriter, err error) {
	if errors.Is(err, provision.ErrAlreadyRunning) {
		writeJSONError(w, http.StatusConflict, "already_running", err.Error())
		return
	}
	writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
}
