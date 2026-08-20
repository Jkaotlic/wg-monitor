package backend

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const wizardMaxJSONBodyBytes = 64 << 10

var (
	enrollmentNicknameRe         = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)
	trafficPeriodRe              = regexp.MustCompile(`^[A-Za-z0-9]{1,8}$`)
	errEnrollmentInvalidNickname = errors.New("invalid enrollment nickname")
	errEnrollmentInvalidKind     = errors.New("invalid enrollment kind")
)

func decodeWizardJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, wizardMaxJSONBodyBytes))
	if err := dec.Decode(dst); err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			return false
		}
		writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "bad json: "+err.Error())
		return false
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "bad json: multiple JSON values")
		return false
	}
	return true
}

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
	Nickname            string     `json:"nickname"`
	Kind                string     `json:"kind"`
	ThreadID            int64      `json:"thread_id"`
	SSHHost             string     `json:"ssh_host"`
	SSHPort             int64      `json:"ssh_port"`
	SSHUser             string     `json:"ssh_user"`
	Arch                string     `json:"arch"`
	LastDeployedVersion string     `json:"last_deployed_version"`
	Ring                string     `json:"ring"`
	PendingVersion      string     `json:"pending_version"`
	PendingSince        string     `json:"pending_since"`
	LastDeploy          string     `json:"last_deploy"`
	DeployMode          string     `json:"deploy_mode"`
	AWGMURL             string     `json:"awgm_url"`
	AWGMAuth            string     `json:"awgm_auth"`
	ExpectedMAC         string     `json:"expected_mac"`
	LastSeenAt          *time.Time `json:"last_seen_at,omitempty"`
	HasTopic            bool       `json:"has_topic"`
}

type wizardAgentList struct {
	Agents []wizardAgent `json:"agents"`
}

type wizardEnrollmentReq struct {
	Nickname string `json:"nickname"`
	Kind     string `json:"kind"`
	ThreadID int64  `json:"thread_id,omitempty"`
}

type wizardEnrollmentResp struct {
	Nickname   string `json:"nickname"`
	BackendURL string `json:"backend_url"`
	RawToken   string `json:"raw_token"`
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
			if u.Ring != nil {
				a.Ring = *u.Ring
			}
			if u.PendingVersion != nil {
				a.PendingVersion = *u.PendingVersion
			}
			if u.PendingSince != nil {
				a.PendingSince = *u.PendingSince
			}
			if u.LastDeploy != nil {
				a.LastDeploy = *u.LastDeploy
			}
			if u.DeployMode != nil {
				a.DeployMode = *u.DeployMode
			}
			if u.AWGMURL != nil {
				a.AWGMURL = *u.AWGMURL
			}
			if u.AWGMAuth != nil {
				a.AWGMAuth = *u.AWGMAuth
			}
			if u.ExpectedMAC != nil {
				a.ExpectedMAC = *u.ExpectedMAC
			}
			if u.LastSeenAt != nil {
				ts := *u.LastSeenAt
				a.LastSeenAt = &ts
			}
			out.Agents = append(out.Agents, a)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// wizardEnrollmentHandler creates or rotates the per-agent token that a fresh
// Entware bootstrap writes to /opt/etc/wg-monitor/config.yaml.
func wizardEnrollmentHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req wizardEnrollmentReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		resp, _, err := createAgentEnrollment(d.DB, req.Nickname, req.Kind, req.ThreadID)
		if err != nil {
			switch {
			case errors.Is(err, errEnrollmentInvalidNickname):
				writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "nickname must match ^[a-z][a-z0-9_-]{1,15}$")
			case errors.Is(err, errEnrollmentInvalidKind):
				writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "kind must be static or mobile")
			default:
				writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			}
			return
		}
		resp.BackendURL = wizardEnrollmentBackendURL(r, d.PublicBaseURL)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// mintProvisionToken validates nickname/kind and returns a fresh raw enrollment
// token WITHOUT persisting it. The DB write is deferred to the provision
// engine's config_written commit hook (see the 2026-07-13 token-atomicity
// design) so a failed install never rotates a live agent's token.
func mintProvisionToken(nickname, kind string) (rawToken, normNick, normKind string, err error) {
	normNick = strings.TrimSpace(nickname)
	if !enrollmentNicknameRe.MatchString(normNick) {
		return "", "", "", errEnrollmentInvalidNickname
	}
	normKind = strings.TrimSpace(kind)
	if normKind == "" {
		normKind = db.KindStatic
	}
	if !db.IsValidKind(normKind) {
		return "", "", "", errEnrollmentInvalidKind
	}
	rawToken, err = newAgentEnrollmentToken()
	if err != nil {
		return "", "", "", fmt.Errorf("token gen: %w", err)
	}
	return rawToken, normNick, normKind, nil
}

func createAgentEnrollment(database *db.DB, nickname, kind string, threadID int64) (wizardEnrollmentResp, int64, error) {
	rawToken, normNick, normKind, err := mintProvisionToken(nickname, kind)
	if err != nil {
		return wizardEnrollmentResp{}, 0, err
	}
	userID, err := database.Users().UpsertEnrollment(normNick, rawToken, normKind, threadID)
	if err != nil {
		return wizardEnrollmentResp{}, 0, err
	}
	return wizardEnrollmentResp{Nickname: normNick, RawToken: rawToken}, userID, nil
}

func newAgentEnrollmentToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func wizardBackendURL(r *http.Request) string {
	proto := firstForwardedValue(r.Header.Get("X-WG-Public-Proto"))
	if proto == "" {
		proto = firstForwardedValue(r.Header.Get("X-Forwarded-Proto"))
	}
	if proto == "" {
		proto = "https"
		if r.TLS == nil && isLoopbackHost(wizardBackendHost(r)) {
			proto = "http"
		}
	}
	host := firstForwardedValue(r.Header.Get("X-WG-Public-Host"))
	if host == "" {
		host = firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
	}
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host
}

func wizardEnrollmentBackendURL(r *http.Request, publicBaseURL string) string {
	if configured, ok := configuredPublicBackendURL(publicBaseURL); ok {
		return configured
	}
	return wizardBackendURL(r)
}

func configuredPublicBackendURL(publicBaseURL string) (string, bool) {
	publicBaseURL = strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	if publicBaseURL == "" {
		return "", false
	}
	u, err := url.Parse(publicBaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	if isNonPublicHost(hostOnly(u.Host)) {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

var lookupHostForRepoResolve = net.LookupHost

func wizardRepoResolveIP(r *http.Request) string {
	return wizardRepoResolveIPForHost(wizardBackendHost(r))
}

func wizardRepoResolveIPForBackendURL(r *http.Request, backendURL string) string {
	host := ""
	if u, err := url.Parse(strings.TrimSpace(backendURL)); err == nil && u.Host != "" {
		host = hostOnly(u.Host)
	}
	if host == "" {
		host = wizardBackendHost(r)
	}
	return wizardRepoResolveIPForHost(host)
}

func wizardRepoResolveIPForHost(host string) string {
	host = hostOnly(host)
	if host == "" || isNonPublicHost(host) {
		return ""
	}
	ips, err := lookupHostForRepoResolve(host)
	if err != nil {
		return ""
	}
	for _, raw := range ips {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.String()
	}
	return ""
}

func wizardBackendHost(r *http.Request) string {
	host := firstForwardedValue(r.Header.Get("X-WG-Public-Host"))
	if host == "" {
		host = firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
	}
	if host == "" {
		host = r.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(host, "[]")
}

func wizardDeployBackendURL(r *http.Request, publicBaseURL string) (string, bool) {
	if host := firstForwardedValue(r.Header.Get("X-WG-Public-Host")); host != "" {
		if !isNonPublicHost(hostOnly(host)) {
			return wizardBackendURL(r), true
		}
		if configured, ok := configuredPublicBackendURL(publicBaseURL); ok {
			return configured, true
		}
		return "", false
	}
	proto := firstForwardedValue(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		proto = "https"
		if r.TLS == nil && isLoopbackHost(wizardBackendHost(r)) {
			proto = "http"
		}
	}
	for _, rawHost := range []string{
		firstForwardedValue(r.Header.Get("X-Forwarded-Host")),
		r.Host,
	} {
		host := hostOnly(rawHost)
		if host == "" || isNonPublicHost(host) {
			continue
		}
		return proto + "://" + rawHost, true
	}
	if configured, ok := configuredPublicBackendURL(publicBaseURL); ok {
		return configured, true
	}
	return "", false
}

func hostOnly(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(host, "[]")
}

func isLoopbackHost(host string) bool {
	host = normalizePublicCheckHost(host)
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isNonPublicHost(host string) bool {
	host = normalizePublicCheckHost(host)
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
}

func normalizePublicCheckHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimRight(host, ".")
	return strings.Trim(host, "[]")
}

func firstForwardedValue(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

type wizardPutAgentReq struct {
	Kind                string `json:"kind"`
	ThreadID            int64  `json:"thread_id"`
	SSHHost             string `json:"ssh_host"`
	SSHPort             int64  `json:"ssh_port"`
	SSHUser             string `json:"ssh_user"`
	Arch                string `json:"arch"`
	LastDeployedVersion string `json:"last_deployed_version"`
	Ring                string `json:"ring"`
	PendingVersion      string `json:"pending_version"`
	PendingSince        string `json:"pending_since"`
	LastDeploy          string `json:"last_deploy"`
	DeployMode          string `json:"deploy_mode"`
	AWGMURL             string `json:"awgm_url"`
	AWGMAuth            string `json:"awgm_auth"`
	ExpectedMAC         string `json:"expected_mac"`
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
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.Kind = strings.TrimSpace(req.Kind)
		if req.Kind != "" && !db.IsValidKind(req.Kind) {
			writeJSONError(w, http.StatusBadRequest, "invalid_kind", "kind must be static or mobile")
			return
		}
		arch, err := normalizeAgentDeployArch(req.Arch)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_arch", err.Error())
			return
		}
		req.Arch = arch
		if err := validateDashboardAWGMURL(req.AWGMURL); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_awgm_url", err.Error())
			return
		}
		err = d.DB.Users().UpdateDeployInfo(nickname, db.DeployInfo{
			Kind:                req.Kind,
			ThreadID:            req.ThreadID,
			SSHHost:             req.SSHHost,
			SSHPort:             req.SSHPort,
			SSHUser:             req.SSHUser,
			Arch:                req.Arch,
			LastDeployedVersion: req.LastDeployedVersion,
			Ring:                req.Ring,
			PendingVersion:      req.PendingVersion,
			PendingSince:        req.PendingSince,
			LastDeploy:          req.LastDeploy,
			DeployMode:          req.DeployMode,
			AWGMURL:             req.AWGMURL,
			AWGMAuth:            req.AWGMAuth,
			ExpectedMAC:         req.ExpectedMAC,
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
// target_version must be a published release tag. The backend adds repo_base
// so agents pull public release assets through the VPS mirror instead of
// depending on router-side DNS/reachability to GitHub.
type wizardDeployReq struct {
	TargetVersion string `json:"target_version"`
	// AllowDowngrade overrides the anti-downgrade rejection below (same
	// escape hatch as the provisioning repair/reinstall flow's field of the
	// same name — see isVersionDowngrade in provision_handler.go).
	AllowDowngrade bool `json:"allow_downgrade"`
}

type wizardDeployResp struct {
	CmdID string `json:"cmd_id"`
}

type backendUpdateRequest struct {
	TargetVersion     string `json:"target_version"`
	RepoBase          string `json:"repo_base"`
	RepoResolveIP     string `json:"repo_resolve_ip,omitempty"`
	TrustedBackendURL string `json:"trusted_backend_url,omitempty"`
	RequestedAt       string `json:"requested_at"`
	// AllowDowngrade overrides the anti-downgrade rejection below.
	AllowDowngrade bool `json:"allow_downgrade,omitempty"`
}

type wizardCommandReq struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args"`
}

type wizardMaintenanceReq struct {
	Name string `json:"name"`
}

var wizardCommandAllowlist = map[string]bool{
	"diag_now":              true,
	"force_recheck":         true,
	"check_via_tunnel":      true,
	"check_direct":          true,
	"pingcheck_now":         true,
	"pingcheck_status":      true,
	"router_doctor":         true,
	"route_status":          true,
	"tunnels_status":        true,
	"route_rebind":          true,
	"opkg_cron_status":      true,
	"opkg_cron_install":     true,
	"opkg_cron_logs":        true,
	"opkg_cron_remove":      true,
	"entware_clean_status":  true,
	"entware_clean_install": true,
	"entware_clean_run":     true,
	"entware_clean_logs":    true,
	"entware_clean_remove":  true,
	"version_audit":         true,
	"tunnel_enable":         true,
	"tunnel_disable":        true,
	"tunnel_restart":        true,
	"tunnel_delete":         true,
	"update_backend_url":    true,
	"agent_config_get":      true,
	"update_agent_config":   true,
}

var dashboardCommandAllowlist = map[string]bool{
	"diag_now":              true,
	"force_recheck":         true,
	"route_status":          true,
	"tunnels_status":        true,
	"opkg_cron_status":      true,
	"opkg_cron_install":     true,
	"opkg_cron_logs":        true,
	"opkg_cron_remove":      true,
	"entware_clean_status":  true,
	"entware_clean_install": true,
	"entware_clean_run":     true,
	"entware_clean_logs":    true,
	"entware_clean_remove":  true,
	"version_audit":         true,
	// DNS reset: wipe the dns-proxy DoT/DoH upstream set and re-apply the
	// reference DNS-over-TLS upstreams on the router (ndmc, no args). Destructive
	// but router-local and recoverable; the dashboard confirms before dispatch.
	"dns_reset": true,
	// Safe on-router config editing: read the safe config subset and patch the
	// whitelisted keys (interval, awg-manager URL/login, external_reach,
	// maintenance gates) then restart the agent. backend.url is NOT touchable here
	// — that whitelist lives agent-side in update_agent_config.
	"agent_config_get":    true,
	"update_agent_config": true,
	// NB: update_backend_url is intentionally NOT here. Re-pointing the fleet's
	// backend domain from a browser session is fleet-takeover blast radius, so it
	// stays gated to the wizard token / deploy CLI (see
	// TestDashboardCommandDispatchRejectsHiddenBackendURLUpdate).
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
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		targetVersion, err := releaseorigin.ValidateReleaseTag(req.TargetVersion)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, err.Error())
			return
		}
		req.TargetVersion = targetVersion
		repoBaseURL, ok := wizardDeployBackendURL(r, d.PublicBaseURL)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON,
				"public backend host required for deploy; set X-Forwarded-Host/X-Forwarded-Proto or call the public wizard URL")
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
		// Anti-downgrade floor: reject a target older than the version the
		// agent's own heartbeat last reported (users.last_deployed_version),
		// unless the operator explicitly opts in. Mirrors the provisioning
		// install/repair path's isVersionDowngrade guard (same helper, so
		// rc9-vs-rc10 compares correctly — see its doc comment) so this
		// legacy self_update-over-/v1/cmd deploy path cannot be used to slip
		// an older, previously-patched build past an operator who forgot
		// this flag existed here too.
		if !req.AllowDowngrade && isVersionDowngrade(req.TargetVersion, stringValue(u.LastDeployedVersion)) {
			writeJSONError(w, http.StatusBadRequest, "downgrade_rejected",
				fmt.Sprintf("target version %s is older than the currently installed %s — pass allow_downgrade to override",
					req.TargetVersion, stringValue(u.LastDeployedVersion)))
			return
		}
		if pending := strings.TrimSpace(stringValue(u.PendingVersion)); pending != "" {
			writeJSONError(w, http.StatusConflict, "deploy_pending", "agent already has pending deploy "+pending)
			return
		}

		id, err := newCmdID()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "id gen: "+err.Error())
			return
		}
		issuedAt := time.Now().UTC()
		cmd := wire.Command{
			ID:     id,
			Action: "self_update",
			Args: map[string]any{
				"version":   req.TargetVersion,
				"repo_base": repoBaseURL + "/v1/releases/download",
			},
			IssuedAt: issuedAt,
		}
		if ip := wizardRepoResolveIPForBackendURL(r, repoBaseURL); ip != "" {
			cmd.Args["repo_resolve_ip"] = ip
		}
		if err := d.DB.Users().MarkPendingDeploy(u.ID, req.TargetVersion, issuedAt.Format(time.RFC3339)); err != nil {
			if errors.Is(err, db.ErrDeployPending) {
				writeJSONError(w, http.StatusConflict, "deploy_pending", "agent already has pending deploy")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if err := d.CommandSink.Enqueue(u.ID, cmd); err != nil {
			if _, clearErr := d.DB.Users().ClearPendingDeployIfMatches(u.ID, req.TargetVersion); clearErr != nil && d.Logger != nil {
				d.Logger.Warn("rollback pending deploy after enqueue failure",
					"nickname", nickname, "user_id", u.ID, "target_version", req.TargetVersion, "err", clearErr)
			}
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

func wizardBackendDeployHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if strings.TrimSpace(d.BackendUpdatePath) == "" {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "backend update queue not configured")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req backendUpdateRequest
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		targetVersion, err := releaseorigin.ValidateReleaseTag(req.TargetVersion)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, err.Error())
			return
		}
		req.TargetVersion = targetVersion
		// Anti-downgrade floor: reject a target older than the currently
		// running backend binary (serverVersion, set once at startup via
		// SetVersion) unless the operator explicitly opts in. Same
		// isVersionDowngrade helper as the agent deploy path above and the
		// provisioning install/repair flow, so rc9-vs-rc10 compares
		// correctly instead of sorting lexically.
		if !req.AllowDowngrade && isVersionDowngrade(req.TargetVersion, serverVersion) {
			writeJSONError(w, http.StatusBadRequest, "downgrade_rejected",
				fmt.Sprintf("target version %s is older than the running backend %s — pass allow_downgrade to override",
					req.TargetVersion, serverVersion))
			return
		}
		repoBaseURL, ok := wizardDeployBackendURL(r, d.PublicBaseURL)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON,
				"public backend host required for deploy; set X-Forwarded-Host/X-Forwarded-Proto or call the public wizard URL")
			return
		}
		req.RepoBase = repoBaseURL + "/v1/releases/download"
		req.RepoResolveIP = wizardRepoResolveIPForBackendURL(r, repoBaseURL)
		req.TrustedBackendURL = repoBaseURL
		req.RequestedAt = time.Now().UTC().Format(time.RFC3339)
		if err := writeBackendUpdateRequest(d.BackendUpdatePath, req); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if d.Logger != nil {
			d.Logger.Info("wizard backend deploy queued",
				"target_version", req.TargetVersion, "path", d.BackendUpdatePath)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(struct {
			Accepted bool `json:"accepted"`
		}{Accepted: true})
	}
}

func writeBackendUpdateRequest(path string, req backendUpdateRequest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("backend update queue mkdir: %w", err)
	}
	body, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("backend update queue write: %w", err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("backend update queue chmod: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("backend update queue rename: %w", err)
	}
	return nil
}

// wizardMaintenanceHandler enqueues a small allowlist of maintenance commands
// for emergency recovery when the operator cannot reach the router directly.
func wizardMaintenanceHandler(d Deps) http.HandlerFunc {
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
		var req wizardMaintenanceReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name != "awgmgr" {
			writeJSONError(w, http.StatusBadRequest, "unsupported_maintenance",
				"name must be awgmgr")
			return
		}
		enqueueWizardAgentCommand(w, d, nickname, "service_restart", map[string]any{"name": req.Name})
	}
}

// wizardCommandHandler enqueues an allowlisted operational command for an
// already enrolled agent. It is intentionally narrower than the agent's full
// command surface: destructive firmware installs, route mutation, token
// rotation, and self-update stay on their dedicated audited flows.
func wizardCommandHandler(d Deps) http.HandlerFunc {
	return commandHandlerWithAllowlist(d, wizardCommandAllowlist)
}

func dashboardCommandHandler(d Deps) http.HandlerFunc {
	return commandHandlerWithAllowlist(d, dashboardCommandAllowlist)
}

func commandHandlerWithAllowlist(d Deps, allowlist map[string]bool) http.HandlerFunc {
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
		var req wizardCommandReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.Action = strings.TrimSpace(req.Action)
		if !allowlist[req.Action] || !wire.IsValidCommandAction(req.Action) {
			writeJSONError(w, http.StatusBadRequest, "unsupported_command", "action is not allowed for wizard command dispatch")
			return
		}
		args, ok := sanitizeWizardCommandArgs(w, req.Action, req.Args)
		if !ok {
			return
		}
		enqueueWizardAgentCommand(w, d, nickname, req.Action, args)
	}
}

func sanitizeWizardCommandArgs(w http.ResponseWriter, action string, args map[string]any) (map[string]any, bool) {
	if args == nil {
		args = map[string]any{}
	}
	switch action {
	case "diag_now", "force_recheck", "check_via_tunnel", "check_direct", "pingcheck_now", "pingcheck_status", "router_doctor", "hrneo_doctor", "route_status", "tunnels_status", "dns_reset":
		return map[string]any{}, true
	case "opkg_cron_install", "entware_clean_install":
		schedule, _ := args["schedule"].(string)
		schedule = strings.TrimSpace(schedule)
		if schedule == "" {
			if action == "entware_clean_install" {
				schedule = "05:15"
			} else {
				schedule = "04:30"
			}
		}
		if !dashboardScheduleLooksSafe(schedule) {
			writeJSONError(w, http.StatusBadRequest, "invalid_schedule", "schedule must be HH:MM or a five-field cron expression")
			return nil, false
		}
		return map[string]any{"schedule": schedule}, true
	case "opkg_cron_status", "opkg_cron_logs", "entware_clean_status", "entware_clean_logs":
		lines := 80
		switch v := args["lines"].(type) {
		case float64:
			lines = int(v)
		case int:
			lines = v
		}
		if lines < 1 {
			lines = 1
		}
		if lines > 300 {
			lines = 300
		}
		return map[string]any{"lines": lines}, true
	case "opkg_cron_remove", "entware_clean_run", "entware_clean_remove", "version_audit":
		return map[string]any{}, true
	case "route_templates":
		// Каталог читается без параметров; всё, что прислал клиент, -- лишнее.
		return map[string]any{}, true
	case "route_add", "route_add_plan":
		kind := strings.ToLower(strings.TrimSpace(argString(args, "kind")))
		if kind != "dns" && kind != "static" {
			writeJSONError(w, http.StatusBadRequest, "invalid_route_kind", "kind must be dns or static")
			return nil, false
		}
		tunnelID := strings.TrimSpace(argString(args, "tunnel_id"))
		if !wizardRouteTargetIDLooksSafe(tunnelID) {
			writeJSONError(w, http.StatusBadRequest, "invalid_tunnel_id", "tunnel_id must be a safe route target id")
			return nil, false
		}
		targets := sanitizeRouteTargets(args["targets"])
		templateID := strings.TrimSpace(argString(args, "template_id"))
		// Либо явные цели, либо набор из каталога: без того и другого плану
		// не из чего собирать правило, и агент ответит ошибкой уже после
		// похода на роутер.
		if len(targets) == 0 && templateID == "" {
			writeJSONError(w, http.StatusBadRequest, "empty_route_targets", "targets or template_id is required")
			return nil, false
		}
		if templateID != "" && !wizardRouteTargetIDLooksSafe(templateID) {
			writeJSONError(w, http.StatusBadRequest, "invalid_template_id", "template_id must be a safe id")
			return nil, false
		}
		out := map[string]any{"kind": kind, "tunnel_id": tunnelID}
		if len(targets) > 0 {
			out["targets"] = targets
		}
		if templateID != "" {
			out["template_id"] = templateID
		}
		if name := strings.TrimSpace(argString(args, "name")); name != "" {
			if len([]rune(name)) > 64 {
				writeJSONError(w, http.StatusBadRequest, "invalid_route_name", "name must be at most 64 characters")
				return nil, false
			}
			out["name"] = name
		}
		if v, isBool := args["use_hr_neo"].(bool); isBool {
			out["use_hr_neo"] = v
		}
		if h := strings.TrimSpace(argString(args, "draft_hash")); h != "" {
			out["draft_hash"] = h
		}
		return out, true
	case "route_delete", "route_delete_plan":
		kind := strings.ToLower(strings.TrimSpace(argString(args, "kind")))
		if kind != "dns" && kind != "static" {
			writeJSONError(w, http.StatusBadRequest, "invalid_route_kind", "kind must be dns or static")
			return nil, false
		}
		routeID := strings.TrimSpace(argString(args, "route_id"))
		if !wizardRouteTargetIDLooksSafe(routeID) {
			writeJSONError(w, http.StatusBadRequest, "invalid_route_id", "route_id must be a safe route id")
			return nil, false
		}
		out := map[string]any{"kind": kind, "route_id": routeID}
		if h := strings.TrimSpace(argString(args, "preview_hash")); h != "" {
			out["preview_hash"] = h
		}
		return out, true
	case "route_policy_promote":
		policy := strings.TrimSpace(argString(args, "policy_name"))
		tunnelID := strings.TrimSpace(argString(args, "tunnel_id"))
		if policy == "" || len([]rune(policy)) > 64 {
			writeJSONError(w, http.StatusBadRequest, "invalid_policy_name", "policy_name is required and must be at most 64 characters")
			return nil, false
		}
		if !wizardRouteTargetIDLooksSafe(tunnelID) {
			writeJSONError(w, http.StatusBadRequest, "invalid_tunnel_id", "tunnel_id must be a safe route target id")
			return nil, false
		}
		return map[string]any{"policy_name": policy, "tunnel_id": tunnelID}, true
	case "route_rebind":
		src, _ := args["src_tunnel_id"].(string)
		dst, _ := args["dst_tunnel_id"].(string)
		src = strings.TrimSpace(src)
		dst = strings.TrimSpace(dst)
		if !wizardRouteTargetIDLooksSafe(src) || !wizardRouteTargetIDLooksSafe(dst) {
			writeJSONError(w, http.StatusBadRequest, "invalid_route_id", "src_tunnel_id and dst_tunnel_id must be safe route target ids")
			return nil, false
		}
		if src == dst {
			writeJSONError(w, http.StatusBadRequest, "same_route_id", "src_tunnel_id and dst_tunnel_id must be different")
			return nil, false
		}
		return map[string]any{"src_tunnel_id": src, "dst_tunnel_id": dst}, true
	case "tunnel_enable", "tunnel_disable":
		ndms, _ := args["ndms_name"].(string)
		ndms = strings.TrimSpace(ndms)
		if !wizardNDMSNameLooksSafe(ndms) {
			writeJSONError(w, http.StatusBadRequest, "invalid_ndms_name", "ndms_name must match ^[A-Za-z0-9_-]{1,32}$")
			return nil, false
		}
		return map[string]any{"ndms_name": ndms}, true
	case "tunnel_traffic":
		// Период уезжает в query-строку awg-manager'а. Словарь периодов
		// принадлежит роутеру, и своей копии здесь нет -- есть запрет на то,
		// чему в query делать нечего.
		tunnelID := strings.TrimSpace(argString(args, "tunnel_id"))
		if !wizardRouteTargetIDLooksSafe(tunnelID) {
			writeJSONError(w, http.StatusBadRequest, "invalid_tunnel_id", "tunnel_id must be a safe tunnel id")
			return nil, false
		}
		period := strings.TrimSpace(argString(args, "period"))
		if period != "" && !wizardTrafficPeriodLooksSafe(period) {
			writeJSONError(w, http.StatusBadRequest, "invalid_period", "period must match ^[A-Za-z0-9]{1,8}$")
			return nil, false
		}
		out := map[string]any{"tunnel_id": tunnelID}
		if period != "" {
			out["period"] = period
		}
		return out, true
	case "pingcheck_toggle":
		// tunnel_id и ndms_name сюда приезжают уже разрешёнными сервером
		// (miniappResolveTunnelArgs), но ветка обязана быть явной: default
		// пропускает аргументы как есть, и действие, забытое здесь, отдало бы
		// агенту клиентский ввод без единой проверки.
		tunnelID := strings.TrimSpace(argString(args, "tunnel_id"))
		ndms := strings.TrimSpace(argString(args, "ndms_name"))
		if !wizardRouteTargetIDLooksSafe(tunnelID) {
			writeJSONError(w, http.StatusBadRequest, "invalid_tunnel_id", "tunnel_id must be a safe tunnel id")
			return nil, false
		}
		if !wizardNDMSNameLooksSafe(ndms) {
			writeJSONError(w, http.StatusBadRequest, "invalid_ndms_name", "ndms_name must match ^[A-Za-z0-9_-]{1,32}$")
			return nil, false
		}
		enable, _ := args["enable"].(bool)
		return map[string]any{"tunnel_id": tunnelID, "ndms_name": ndms, "enable": enable}, true
	case "tunnel_restart":
		// Either identifier is enough: awg-manager restarts by tunnel id, and
		// opkg tunnels have no NDMS name at all. Whatever is present must still
		// be safe — both values end up in a URL query or an ndmc command line.
		out := map[string]any{}
		if tunnelID, _ := args["tunnel_id"].(string); strings.TrimSpace(tunnelID) != "" {
			tunnelID = strings.TrimSpace(tunnelID)
			if !wizardRouteTargetIDLooksSafe(tunnelID) {
				writeJSONError(w, http.StatusBadRequest, "invalid_tunnel_id", "tunnel_id must be a safe route target id")
				return nil, false
			}
			out["tunnel_id"] = tunnelID
		}
		if ndms, _ := args["ndms_name"].(string); strings.TrimSpace(ndms) != "" {
			ndms = strings.TrimSpace(ndms)
			if !wizardNDMSNameLooksSafe(ndms) {
				writeJSONError(w, http.StatusBadRequest, "invalid_ndms_name", "ndms_name must match ^[A-Za-z0-9_-]{1,32}$")
				return nil, false
			}
			out["ndms_name"] = ndms
		}
		if len(out) == 0 {
			// Не "невалидный ndms_name", а вовсе не переданный идентификатор:
			// код ошибки -- контракт, и он обязан называть настоящую причину.
			writeJSONError(w, http.StatusBadRequest, "missing_tunnel_identifier", "tunnel_id or ndms_name is required")
			return nil, false
		}
		return out, true
	case "tunnel_delete":
		tunnelID, _ := args["tunnel_id"].(string)
		checkName, _ := args["check_name"].(string)
		tunnelID = strings.TrimSpace(tunnelID)
		checkName = strings.TrimSpace(checkName)
		out := map[string]any{}
		if tunnelID != "" {
			if !wizardRouteTargetIDLooksSafe(tunnelID) {
				writeJSONError(w, http.StatusBadRequest, "invalid_tunnel_id", "tunnel_id must be a safe tunnel id")
				return nil, false
			}
			out["tunnel_id"] = tunnelID
		}
		if checkName != "" {
			if !wizardTunnelCheckNameLooksSafe(checkName) {
				writeJSONError(w, http.StatusBadRequest, "invalid_check_name", "check_name must be tunnel_<safe tunnel id>")
				return nil, false
			}
			out["check_name"] = checkName
		}
		if len(out) == 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid_tunnel_id", "tunnel_id or check_name is required")
			return nil, false
		}
		if force, _ := args["force_legacy_cleanup"].(bool); force {
			out["force_legacy_cleanup"] = true
		}
		return out, true
	case "update_backend_url":
		rawURL, _ := args["url"].(string)
		normalized, err := sanitizeWizardBackendURL(rawURL)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_backend_url", err.Error())
			return nil, false
		}
		return map[string]any{"url": normalized}, true
	case "agent_config_get":
		return map[string]any{}, true
	case "update_agent_config":
		return sanitizeAgentConfigArgs(w, args)
	default:
		return args, true
	}
}

// agentConfigArgSpec mirrors the agent-side whitelist (actions.agentConfigWhitelist):
// only these keys can be pushed via update_agent_config. backend.url/token are
// absent on purpose — re-pointing the backend stays on the wizard/CLI path.
var agentConfigBoolArgs = map[string]bool{
	"external_reach_enabled": true,
	"allow_router_reboot":    true,
	"allow_firmware_install": true,
}

// sanitizeAgentConfigArgs validates and narrows update_agent_config args to the
// known-safe keys, with the same type/range checks the agent enforces. At least
// one recognized key must be present.
func sanitizeAgentConfigArgs(w http.ResponseWriter, args map[string]any) (map[string]any, bool) {
	out := map[string]any{}
	reject := func(field string) (map[string]any, bool) {
		writeJSONError(w, http.StatusBadRequest, "invalid_agent_config", "invalid value for "+field)
		return nil, false
	}
	if v, ok := args["interval_sec"]; ok {
		n, ok := agentConfigArgInt(v)
		if !ok || n < 10 || n > 86400 {
			return reject("interval_sec (want 10..86400)")
		}
		out["interval_sec"] = n
	}
	if v, ok := args["external_reach_fail_threshold"]; ok {
		n, ok := agentConfigArgInt(v)
		if !ok || n < 1 || n > 20 {
			return reject("external_reach_fail_threshold (want 1..20)")
		}
		out["external_reach_fail_threshold"] = n
	}
	if v, ok := args["awgm_base_url"]; ok {
		s, ok := v.(string)
		if !ok {
			return reject("awgm_base_url")
		}
		s = strings.TrimSpace(s)
		if s != "" {
			u, err := url.Parse(s)
			if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return reject("awgm_base_url (want absolute http(s) URL)")
			}
		}
		out["awgm_base_url"] = s
	}
	if v, ok := args["awgm_login"]; ok {
		s, ok := v.(string)
		if !ok || len(strings.TrimSpace(s)) > 64 {
			return reject("awgm_login")
		}
		out["awgm_login"] = strings.TrimSpace(s)
	}
	for key := range agentConfigBoolArgs {
		if v, ok := args[key]; ok {
			b, ok := v.(bool)
			if !ok {
				return reject(key)
			}
			out[key] = b
		}
	}
	if len(out) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_agent_config", "no recognized settings to change")
		return nil, false
	}
	return out, true
}

func agentConfigArgInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

func sanitizeWizardBackendURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", fmt.Errorf("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("url must be an absolute https URL")
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("url must start with https://")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" {
		return "", fmt.Errorf("url must not use localhost")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return "", fmt.Errorf("url must not use private or loopback IP")
	}
	u.Scheme = "https"
	u.Host = normalizedWizardURLHost(u)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String(), nil
}

func normalizedWizardURLHost(u *url.URL) string {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if port := u.Port(); port != "" {
		return net.JoinHostPort(host, port)
	}
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func wizardTrafficPeriodLooksSafe(v string) bool { return trafficPeriodRe.MatchString(v) }

func wizardNDMSNameLooksSafe(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func dashboardScheduleLooksSafe(schedule string) bool {
	if strings.Count(schedule, ":") == 1 && !strings.Contains(schedule, " ") {
		parts := strings.Split(schedule, ":")
		if len(parts) != 2 || len(parts[0]) > 2 || len(parts[1]) != 2 {
			return false
		}
		hour, hErr := strconv.Atoi(parts[0])
		min, mErr := strconv.Atoi(parts[1])
		return hErr == nil && mErr == nil && hour >= 0 && hour <= 23 && min >= 0 && min <= 59
	}
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return false
	}
	for _, f := range fields {
		for _, r := range f {
			if (r < '0' || r > '9') && r != '*' && r != '/' && r != '-' && r != ',' {
				return false
			}
		}
	}
	return true
}

func argString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// sanitizeRouteTargets приводит цели к []string, выбрасывая пустые и обрезая
// список. Верхняя граница не косметическая: правило с тысячей доменов роутер
// примет, а потом утонет при каждом обновлении маршрутов.
func sanitizeRouteTargets(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, _ := item.(string)
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 253 {
			continue
		}
		out = append(out, s)
		if len(out) == 200 {
			break
		}
	}
	return out
}

func wizardRouteTargetIDLooksSafe(id string) bool {
	if id == wire.RouteOtherID {
		return true
	}
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func wizardTunnelCheckNameLooksSafe(name string) bool {
	const prefix = "tunnel_"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	return wizardRouteTargetIDLooksSafe(strings.TrimPrefix(name, prefix))
}

func enqueueWizardAgentCommand(w http.ResponseWriter, d Deps, nickname, action string, args map[string]any) {
	u, err := d.DB.Users().GetByNickname(nickname)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}
	if u == nil {
		writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
		return
	}
	enqueueAgentCommandForUser(w, d, u, action, args)
}

// enqueueAgentCommandForUser queues a command for an ALREADY-RESOLVED agent.
// The wizard/dashboard reach it by nickname via enqueueWizardAgentCommand's
// thin wrapper above; the mini app already holds the router's users.id (that
// is what its whole ACL is keyed on -- see db.RouterAccessRole(routerUserID,
// ...)), so making it round-trip id -> nickname -> id just to enqueue would be
// silly. Callers do their own authorization before calling this: it enforces
// none.
func enqueueAgentCommandForUser(w http.ResponseWriter, d Deps, u *db.User, action string, args map[string]any) {
	id, err := newCmdID()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "id gen: "+err.Error())
		return
	}
	if args == nil {
		args = map[string]any{}
	}
	cmd := wire.Command{
		ID:       id,
		Action:   action,
		Args:     args,
		IssuedAt: time.Now().UTC(),
	}
	if err := d.CommandSink.Enqueue(u.ID, cmd); err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "enqueue: "+err.Error())
		return
	}
	if d.Logger != nil {
		d.Logger.Info("agent command enqueued",
			"nickname", u.Nickname, "user_id", u.ID, "cmd_id", id, "action", action)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(wizardDeployResp{CmdID: id})
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
