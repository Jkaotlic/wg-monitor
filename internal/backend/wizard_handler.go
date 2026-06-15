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

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/pkg/wire"
)

const wizardMaxJSONBodyBytes = 64 << 10

var (
	enrollmentNicknameRe         = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)
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

func createAgentEnrollment(database *db.DB, nickname, kind string, threadID int64) (wizardEnrollmentResp, int64, error) {
	nickname = strings.TrimSpace(nickname)
	if !enrollmentNicknameRe.MatchString(nickname) {
		return wizardEnrollmentResp{}, 0, errEnrollmentInvalidNickname
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = db.KindStatic
	}
	if !db.IsValidKind(kind) {
		return wizardEnrollmentResp{}, 0, errEnrollmentInvalidKind
	}
	rawToken, err := newAgentEnrollmentToken()
	if err != nil {
		return wizardEnrollmentResp{}, 0, fmt.Errorf("token gen: %w", err)
	}
	userID, err := database.Users().UpsertEnrollment(nickname, rawToken, kind, threadID)
	if err != nil {
		return wizardEnrollmentResp{}, 0, err
	}
	return wizardEnrollmentResp{
		Nickname: nickname,
		RawToken: rawToken,
	}, userID, nil
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
	host := wizardBackendHost(r)
	if host == "" {
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
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isNonPublicHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
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
		err := d.DB.Users().UpdateDeployInfo(nickname, db.DeployInfo{
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
}

type wizardDeployResp struct {
	CmdID string `json:"cmd_id"`
}

type backendUpdateRequest struct {
	TargetVersion string `json:"target_version"`
	RepoBase      string `json:"repo_base"`
	RepoResolveIP string `json:"repo_resolve_ip,omitempty"`
	RequestedAt   string `json:"requested_at"`
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
		if req.TargetVersion == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "target_version required")
			return
		}
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
		if ip := wizardRepoResolveIP(r); ip != "" {
			cmd.Args["repo_resolve_ip"] = ip
		}
		if err := d.CommandSink.Enqueue(u.ID, cmd); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "enqueue: "+err.Error())
			return
		}
		if err := d.DB.Users().MarkPendingDeploy(u.ID, req.TargetVersion, issuedAt.Format(time.RFC3339)); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
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
		req.TargetVersion = strings.TrimSpace(req.TargetVersion)
		if req.TargetVersion == "" {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "target_version required")
			return
		}
		repoBaseURL, ok := wizardDeployBackendURL(r, d.PublicBaseURL)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON,
				"public backend host required for deploy; set X-Forwarded-Host/X-Forwarded-Proto or call the public wizard URL")
			return
		}
		req.RepoBase = repoBaseURL + "/v1/releases/download"
		req.RepoResolveIP = wizardRepoResolveIP(r)
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
		if !wizardCommandAllowlist[req.Action] || !wire.IsValidCommandAction(req.Action) {
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
	case "tunnel_enable", "tunnel_disable", "tunnel_restart":
		ndms, _ := args["ndms_name"].(string)
		ndms = strings.TrimSpace(ndms)
		if !wizardNDMSNameLooksSafe(ndms) {
			writeJSONError(w, http.StatusBadRequest, "invalid_ndms_name", "ndms_name must match ^[A-Za-z0-9_-]{1,32}$")
			return nil, false
		}
		return map[string]any{"ndms_name": ndms}, true
	default:
		return args, true
	}
}

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
		d.Logger.Info("wizard command enqueued",
			"nickname", nickname, "user_id", u.ID, "cmd_id", id, "action", action)
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
