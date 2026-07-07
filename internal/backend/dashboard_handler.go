package backend

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

const dashboardSessionCookieName = "wg_dashboard_session"
const dashboardSessionTTL = 12 * time.Hour

var dashboardNow = time.Now

// DashboardAuthMiddleware gates /dashboard and /v1/dashboard/* with the
// dashboard token loaded at startup. Empty expected tokens must never be wired.
func DashboardAuthMiddleware(expected string, logger *slog.Logger) func(http.Handler) http.Handler {
	logReject := func(r *http.Request, reason string) {
		if logger == nil {
			return
		}
		logger.Warn("dashboard auth: rejected",
			"reason", reason, "remote", r.RemoteAddr, "path", r.URL.Path,
		)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if strings.HasPrefix(hdr, prefix) {
				presented := strings.TrimPrefix(hdr, prefix)
				if presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1 {
					next.ServeHTTP(w, r)
					return
				}
				logReject(r, "token-mismatch")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if dashboardSessionValid(r, expected) {
				next.ServeHTTP(w, r)
				return
			}
			logReject(r, "missing-auth")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

func registerDashboardRoutes(mux *http.ServeMux, d Deps) {
	staticFS, err := fs.Sub(dashboardStaticFS, "dashboard_static")
	if err != nil {
		panic(err)
	}
	staticHandler := http.StripPrefix("/dashboard/", http.FileServer(http.FS(staticFS)))
	dashAuth := DashboardAuthMiddleware(d.DashboardToken, d.Logger)
	pageAuth := DashboardPageAuthMiddleware(d.DashboardToken)
	mux.Handle("GET /dashboard", requestIDMiddleware()(http.RedirectHandler("/dashboard/", http.StatusFound)))
	mux.Handle("GET /dashboard/login", requestIDMiddleware()(dashboardLoginPageHandler()))
	mux.Handle("POST /v1/dashboard/login", requestIDMiddleware()(dashboardLoginHandler(d)))
	mux.Handle("POST /v1/dashboard/logout", requestIDMiddleware()(dashboardLogoutHandler()))
	mux.Handle("GET /dashboard/", requestIDMiddleware()(pageAuth(staticHandler)))
	mux.Handle("GET /v1/dashboard/summary", requestIDMiddleware()(dashAuth(dashboardSummaryHandler(d))))
	mux.Handle("POST /v1/dashboard/enrollments", requestIDMiddleware()(dashAuth(dashboardEnrollmentHandler(d))))
	mux.Handle("PUT /v1/dashboard/agents/{nickname}", requestIDMiddleware()(dashAuth(dashboardEditAgentHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/deploy", requestIDMiddleware()(dashAuth(wizardDeployHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/deploy/cancel", requestIDMiddleware()(dashAuth(dashboardCancelDeployHandler(d))))
	mux.Handle("POST /v1/dashboard/backend/deploy", requestIDMiddleware()(dashAuth(wizardBackendDeployHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/commands", requestIDMiddleware()(dashAuth(dashboardCommandHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/maintenance", requestIDMiddleware()(dashAuth(wizardMaintenanceHandler(d))))
	mux.Handle("GET /v1/dashboard/commands/{cmd_id}", requestIDMiddleware()(dashAuth(wizardCmdResultHandler(d))))
	// Router-provisioning engine (register/install a new router, poll its
	// progress, repair repoint/reinstall — see internal/backend/provision and
	// provision_handler.go). Same dashAuth-gated pattern as every other
	// operator-only endpoint above: never exposed unauthenticated.
	mux.Handle("POST /v1/dashboard/provision", requestIDMiddleware()(dashAuth(dashboardProvisionHandler(d))))
	mux.Handle("GET /v1/dashboard/provision/{job_id}", requestIDMiddleware()(dashAuth(dashboardProvisionPollHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/repair", requestIDMiddleware()(dashAuth(dashboardRepairHandler(d))))
}

// dashboardCancelDeployHandler cancels a pending agent deploy: it drops any
// queued self_update (so it never fires when a sleeping mobile agent wakes) and
// unconditionally clears the pending_version marker. Idempotent — returns 200
// with cleared=false if there was nothing pending.
func dashboardCancelDeployHandler(d Deps) http.HandlerFunc {
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
		u, err := d.DB.Users().GetByNickname(nickname)
		if err != nil {
			if errors.Is(err, db.ErrUserNotFound) {
				writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if u == nil {
			writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
			return
		}
		var dropped int
		if d.CommandSink != nil {
			dropped = len(d.CommandSink.DropPending(u.ID, "self_update"))
		}
		cleared, err := d.DB.Users().ClearPendingDeploy(u.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if d.Logger != nil {
			d.Logger.Info("dashboard cancel pending deploy",
				"nickname", nickname, "user_id", u.ID, "cleared", cleared, "dropped_commands", dropped)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			OK              bool `json:"ok"`
			Cleared         bool `json:"cleared"`
			DroppedCommands int  `json:"dropped_commands"`
		}{OK: true, Cleared: cleared, DroppedCommands: dropped})
	}
}

// DashboardPageAuthMiddleware protects the browser app itself. The JSON API
// accepts the same session cookie through DashboardAuthMiddleware.
func DashboardPageAuthMiddleware(expected string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if dashboardSessionValid(r, expected) {
				next.ServeHTTP(w, r)
				return
			}
			http.Redirect(w, r, "/dashboard/login", http.StatusFound)
		})
	}
}

func dashboardLoginPageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dashboardLoginHTML))
	})
}

func dashboardLoginHandler(d Deps) http.Handler {
	type loginRequest struct {
		Token string `json:"token"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireJSONContentType(w, r) {
			return
		}
		var req loginRequest
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		if req.Token == "" || subtle.ConstantTimeCompare([]byte(req.Token), []byte(d.DashboardToken)) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		http.SetCookie(w, dashboardSessionCookie(r, d.DashboardToken))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			OK bool `json:"ok"`
		}{OK: true})
	})
}

func dashboardLogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     dashboardSessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		w.WriteHeader(http.StatusNoContent)
	})
}

func dashboardSessionCookie(r *http.Request, token string) *http.Cookie {
	return &http.Cookie{
		Name:     dashboardSessionCookieName,
		Value:    dashboardSessionValue(token, dashboardNow().Add(dashboardSessionTTL)),
		Path:     "/",
		MaxAge:   int(dashboardSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	}
}

func dashboardSessionValid(r *http.Request, token string) bool {
	cookie, err := r.Cookie(dashboardSessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	parts := strings.Split(cookie.Value, ":")
	if len(parts) != 3 || parts[0] != "v3" {
		return false
	}
	expUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	expiresAt := time.Unix(expUnix, 0).UTC()
	if !dashboardNow().Before(expiresAt) {
		return false
	}
	want := dashboardSessionValue(token, expiresAt)
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(want)) == 1
}

// dashboardSessionEpoch is a server-side secret mixed into every session HMAC.
// It is random per process start and can be rotated on demand, which lets the
// operator invalidate ALL outstanding dashboard sessions (e.g. a cookie leaked
// from a browser) without rotating the dashboard token itself (SEC-05). A side
// effect is that a backend restart logs dashboard sessions out — acceptable for
// an operator-driven tool, and a mild hardening on its own.
var (
	dashboardSessionEpochMu sync.RWMutex
	dashboardSessionEpoch   = newDashboardSessionEpoch()
)

func newDashboardSessionEpoch() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is near-impossible; fall back to a per-start value
		// so the epoch still differs across process lifetimes.
		return strconv.FormatInt(dashboardNow().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

func currentDashboardSessionEpoch() string {
	dashboardSessionEpochMu.RLock()
	defer dashboardSessionEpochMu.RUnlock()
	return dashboardSessionEpoch
}

// RotateDashboardSessions invalidates every outstanding dashboard session
// cookie. Intended for "log out everywhere" without changing the token.
func RotateDashboardSessions() {
	dashboardSessionEpochMu.Lock()
	defer dashboardSessionEpochMu.Unlock()
	dashboardSessionEpoch = newDashboardSessionEpoch()
}

func dashboardSessionValue(token string, expiresAt time.Time) string {
	exp := strconv.FormatInt(expiresAt.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("wg-monitor-dashboard-session-v1"))
	_, _ = mac.Write([]byte(":"))
	_, _ = mac.Write([]byte(currentDashboardSessionEpoch()))
	_, _ = mac.Write([]byte(":"))
	_, _ = mac.Write([]byte(exp))
	return "v3:" + exp + ":" + hex.EncodeToString(mac.Sum(nil))
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

const dashboardLoginHTML = `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Dashboard Login</title>
  <link rel="icon" href="data:,">
  <style>
    * { box-sizing: border-box; }
    body {
      min-height: 100vh;
      margin: 0;
      display: grid;
      place-items: center;
      background:
        linear-gradient(180deg, rgba(37, 99, 235, .14), rgba(37, 99, 235, 0) 330px),
        #f4f7fb;
      color: #111827;
      font-family: "Segoe UI", "Aptos", sans-serif;
    }
    main {
      width: min(430px, calc(100vw - 32px));
      border: 1px solid #d9e2ef;
      border-top: 4px solid #2563eb;
      border-radius: 8px;
      background: #fff;
      box-shadow: 0 22px 60px rgba(16,24,40,.14);
      padding: 24px;
    }
    .mark {
      display: grid;
      width: 48px;
      height: 48px;
      place-items: center;
      border-radius: 8px;
      background: #111827;
      color: #91caff;
      font-weight: 900;
      margin-bottom: 18px;
    }
    .eyebrow {
      color: #667085;
      font-size: 12px;
      font-weight: 900;
      letter-spacing: 0;
      text-transform: uppercase;
    }
    h1 {
      margin: 4px 0 8px;
      font-size: 28px;
      line-height: 1.12;
      letter-spacing: 0;
    }
    p {
      margin: 0 0 18px;
      color: #667085;
      line-height: 1.5;
    }
    label {
      display: grid;
      gap: 7px;
      color: #344054;
      font-size: 12px;
      font-weight: 900;
      text-transform: uppercase;
    }
    input {
      width: 100%;
      min-height: 44px;
      border: 1px solid #d9e2ef;
      border-radius: 8px;
      padding: 0 12px;
      font: inherit;
      outline: none;
    }
    input:focus {
      border-color: #77a7ff;
      box-shadow: 0 0 0 3px rgba(37, 99, 235, .16);
    }
    button {
      width: 100%;
      min-height: 44px;
      margin-top: 14px;
      border: 0;
      border-radius: 8px;
      background: #2563eb;
      color: #fff;
      cursor: pointer;
      font: inherit;
      font-weight: 900;
      box-shadow: 0 12px 28px rgba(37,99,235,.25);
    }
    .error {
      min-height: 20px;
      margin-top: 12px;
      color: #b42318;
      font-size: 13px;
      font-weight: 800;
    }
  </style>
</head>
<body>
  <main>
    <div class="mark">WG</div>
    <div class="eyebrow">protected area</div>
    <h1>Dashboard Login</h1>
    <p>Введите dashboard token. Сервер выдаст HttpOnly session cookie, сам токен в UI храниться не будет.</p>
    <form id="loginForm">
      <label>
        Token
        <input id="tokenInput" type="password" autocomplete="current-password" autofocus>
      </label>
      <button type="submit">Open Dashboard</button>
      <div id="error" class="error"></div>
    </form>
  </main>
  <script>
    document.getElementById("loginForm").addEventListener("submit", async (event) => {
      event.preventDefault();
      const error = document.getElementById("error");
      error.textContent = "";
      const token = document.getElementById("tokenInput").value.trim();
      try {
        const res = await fetch("/v1/dashboard/login", {
          method: "POST",
          headers: {"Content-Type": "application/json"},
          body: JSON.stringify({token})
        });
        if (!res.ok) throw new Error("Неверный token");
        window.location.href = "/dashboard/";
      } catch (err) {
        error.textContent = err.message || "Login failed";
      }
    });
  </script>
</body>
</html>`

type dashboardSummary struct {
	Status        string                   `json:"status"`
	Version       string                   `json:"version"`
	LatestVersion string                   `json:"latest_version"`
	GeneratedAt   string                   `json:"generated_at"`
	Totals        dashboardSummaryTotals   `json:"totals"`
	Telegram      dashboardTelegramSummary `json:"telegram"`
	Agents        []dashboardSummaryAgent  `json:"agents"`
}

type dashboardTelegramSummary struct {
	PrimaryChatID int64   `json:"primary_chat_id"`
	ExtraChatIDs  []int64 `json:"extra_chat_ids"`
}

type dashboardSummaryTotals struct {
	Agents         int `json:"agents"`
	Online         int `json:"online"`
	Sleeping       int `json:"sleeping"`
	Offline        int `json:"offline"`
	Alerts         int `json:"alerts"`
	PendingDeploys int `json:"pending_deploys"`
}

type dashboardSummaryAgent struct {
	ID               int64               `json:"id"`
	Nickname         string              `json:"nickname"`
	Kind             string              `json:"kind"`
	Status           string              `json:"status"`
	ExpectedExitIP   string              `json:"expected_exit_ip"`
	AWGIface         string              `json:"awg_iface"`
	LastSeenAt       *time.Time          `json:"last_seen_at,omitempty"`
	LastSeenAgeSec   *int64              `json:"last_seen_age_sec,omitempty"`
	SSHHost          string              `json:"ssh_host"`
	SSHPort          int64               `json:"ssh_port"`
	SSHUser          string              `json:"ssh_user"`
	Arch             string              `json:"arch"`
	AgentVersion     string              `json:"agent_version"`
	Ring             string              `json:"ring"`
	PendingVersion   string              `json:"pending_version"`
	PendingSince     string              `json:"pending_since"`
	LastDeploy       string              `json:"last_deploy"`
	DeployMode       string              `json:"deploy_mode"`
	AWGMURL          string              `json:"awgm_url"`
	AWGMAuth         string              `json:"awgm_auth"`
	ExpectedMAC      string              `json:"expected_mac"`
	TelegramChatID   int64               `json:"telegram_chat_id"`
	TelegramThreadID int64               `json:"telegram_thread_id"`
	HasTopic         bool                `json:"has_topic"`
	ActiveIncidents  []dashboardIncident `json:"active_incidents"`
}

type dashboardIncident struct {
	CheckName string `json:"check_name"`
	HardSince string `json:"hard_since"`
	FailCount int    `json:"fail_count"`
}

type dashboardStatusPolicy struct {
	StaticStaleAfter   time.Duration
	MobileStaleAfter   time.Duration
	MobileOfflineAfter time.Duration
}

const (
	defaultDashboardStaticStaleAfter   = 5 * time.Minute
	defaultDashboardMobileStaleAfter   = 30 * time.Minute
	defaultDashboardMobileOfflineAfter = 24 * time.Hour
)

func dashboardStatusPolicyFromDeps(d Deps) dashboardStatusPolicy {
	p := dashboardStatusPolicy{
		StaticStaleAfter:   defaultDashboardStaticStaleAfter,
		MobileStaleAfter:   defaultDashboardMobileStaleAfter,
		MobileOfflineAfter: defaultDashboardMobileOfflineAfter,
	}
	if d.DashboardStaleAfterStatic > 0 {
		p.StaticStaleAfter = d.DashboardStaleAfterStatic
	}
	if d.DashboardStaleAfterMobile > 0 {
		p.MobileStaleAfter = d.DashboardStaleAfterMobile
	}
	if p.MobileOfflineAfter < p.MobileStaleAfter {
		p.MobileOfflineAfter = p.MobileStaleAfter
	}
	return p
}

func dashboardSummaryHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "db_not_configured", "db not configured")
			return
		}
		resp, err := buildDashboardSummary(d.DB, time.Now().UTC(), dashboardStatusPolicyFromDeps(d))
		if err != nil {
			if d.Logger != nil {
				d.Logger.Error("dashboard summary failed", "err", err)
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "dashboard summary failed")
			return
		}
		latest, err := lookupDashboardLatestVersion(r.Context())
		if err != nil && d.Logger != nil {
			d.Logger.Warn("dashboard latest release lookup failed", "err", err)
		}
		if latest == "" {
			latest = serverVersion
		}
		resp.LatestVersion = latest
		resp.Telegram = dashboardTelegramSummary{
			PrimaryChatID: d.TelegramPrimaryChatID,
			ExtraChatIDs:  append([]int64(nil), d.TelegramExtraChatIDs...),
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func buildDashboardSummary(database *db.DB, now time.Time, policy dashboardStatusPolicy) (dashboardSummary, error) {
	users, err := database.Users().GetAll()
	if err != nil {
		return dashboardSummary{}, err
	}
	hards, err := database.State().AllActiveHard()
	if err != nil {
		return dashboardSummary{}, err
	}
	hardsByUser := make(map[int64][]dashboardIncident)
	for _, hard := range hards {
		incident := dashboardIncident{
			CheckName: hard.CheckName,
			FailCount: hard.FailCount,
		}
		if !hard.HardSince.IsZero() {
			incident.HardSince = hard.HardSince.UTC().Format(time.RFC3339)
		}
		hardsByUser[hard.UserID] = append(hardsByUser[hard.UserID], incident)
	}

	resp := dashboardSummary{
		Status:      "ok",
		Version:     serverVersion,
		GeneratedAt: now.UTC().Format(time.RFC3339),
	}
	for _, user := range users {
		agent := dashboardAgentFromUser(user, hardsByUser[user.ID], now, policy)
		resp.Agents = append(resp.Agents, agent)
		resp.Totals.Agents++
		switch agent.Status {
		case "alert":
			resp.Totals.Alerts++
		case "online":
			resp.Totals.Online++
		case "sleeping":
			resp.Totals.Sleeping++
		case "offline":
			resp.Totals.Offline++
		}
		if agent.PendingVersion != "" {
			resp.Totals.PendingDeploys++
		}
	}
	return resp, nil
}

type dashboardGitHubRelease struct {
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
}

type dashboardLatestCache struct {
	version string
	at      time.Time
}

const dashboardLatestCacheTTL = 5 * time.Minute
const dashboardGitHubReleasesURL = "https://api.github.com/repos/Jkaotlic/wg-monitor/releases?per_page=30"
const maxDashboardGitHubReleaseListBytes = 1 << 20

var (
	lookupDashboardLatestVersion = fetchDashboardLatestVersion
	dashboardLatestMu            sync.Mutex
	dashboardLatestCached        dashboardLatestCache
)

func fetchDashboardLatestVersion(ctx context.Context) (string, error) {
	dashboardLatestMu.Lock()
	if dashboardLatestCached.version != "" && time.Since(dashboardLatestCached.at) < dashboardLatestCacheTTL {
		version := dashboardLatestCached.version
		dashboardLatestMu.Unlock()
		return version, nil
	}
	dashboardLatestMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dashboardGitHubReleasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "wg-monitor-dashboard/"+serverVersion)
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("github releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github releases HTTP %d", resp.StatusCode)
	}
	version, err := decodeDashboardLatestRelease(resp.Body)
	if err != nil {
		return "", err
	}
	dashboardLatestMu.Lock()
	dashboardLatestCached = dashboardLatestCache{version: version, at: time.Now()}
	dashboardLatestMu.Unlock()
	return version, nil
}

func decodeDashboardLatestRelease(r io.Reader) (string, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxDashboardGitHubReleaseListBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxDashboardGitHubReleaseListBytes {
		return "", fmt.Errorf("github releases response too large: limit %d bytes", maxDashboardGitHubReleaseListBytes)
	}
	var releases []dashboardGitHubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return "", fmt.Errorf("decode github releases: %w", err)
	}
	version := dashboardOperatorLatestRelease(releases)
	if version == "" {
		return "", errors.New("github releases returned no usable tags")
	}
	return version, nil
}

func dashboardOperatorLatestRelease(releases []dashboardGitHubRelease) string {
	var best dashboardGitHubRelease
	for _, rel := range releases {
		if rel.Draft || rel.TagName == "" {
			continue
		}
		if best.TagName == "" || dashboardReleaseNewer(rel, best) {
			best = rel
		}
	}
	return best.TagName
}

func dashboardReleaseNewer(a, b dashboardGitHubRelease) bool {
	if sameDashboardRCSeries(a.TagName, b.TagName) {
		return compareDashboardReleaseTags(a.TagName, b.TagName) > 0
	}
	if !a.PublishedAt.IsZero() || !b.PublishedAt.IsZero() {
		if a.PublishedAt.After(b.PublishedAt) {
			return true
		}
		if a.PublishedAt.Before(b.PublishedAt) {
			return false
		}
	}
	return compareDashboardReleaseTags(a.TagName, b.TagName) > 0
}

func sameDashboardRCSeries(a, b string) bool {
	ar, aok := parseDashboardReleaseTagRank(a)
	br, bok := parseDashboardReleaseTagRank(b)
	if !aok || !bok || !strings.Contains(a, "-rc") || !strings.Contains(b, "-rc") {
		return false
	}
	for i := 0; i < 3; i++ {
		if ar[i] != br[i] {
			return false
		}
	}
	return true
}

func compareDashboardReleaseTags(a, b string) int {
	av, aok := parseDashboardReleaseTagRank(a)
	bv, bok := parseDashboardReleaseTagRank(b)
	if !aok || !bok {
		return strings.Compare(a, b)
	}
	for i := range av {
		if av[i] > bv[i] {
			return 1
		}
		if av[i] < bv[i] {
			return -1
		}
	}
	return 0
}

func parseDashboardReleaseTagRank(tag string) ([4]int, bool) {
	var rank [4]int
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "v")
	main, rcRaw, hasRC := strings.Cut(tag, "-rc")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return rank, false
	}
	for i, p := range parts {
		n, ok := parseDashboardReleaseRankNumber(p)
		if !ok {
			return rank, false
		}
		rank[i] = n
	}
	if !hasRC {
		rank[3] = 1 << 30
		return rank, true
	}
	rc, ok := parseDashboardReleaseRankNumber(rcRaw)
	if !ok {
		return rank, false
	}
	rank[3] = rc
	return rank, true
}

func parseDashboardReleaseRankNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func dashboardAgentFromUser(user db.User, incidents []dashboardIncident, now time.Time, policy dashboardStatusPolicy) dashboardSummaryAgent {
	status := "online"
	age := dashboardLastSeenAge(user, now)
	if len(incidents) > 0 {
		status = "alert"
	} else if age == nil {
		status = "offline"
	} else if user.IsMobile() {
		gap := time.Duration(*age) * time.Second
		if gap >= policy.MobileOfflineAfter {
			status = "offline"
		} else if gap >= policy.MobileStaleAfter {
			status = "sleeping"
		}
	} else if time.Duration(*age)*time.Second >= policy.StaticStaleAfter {
		status = "offline"
	}

	var lastSeenAge *int64
	if age != nil {
		v := *age
		lastSeenAge = &v
	}

	agent := dashboardSummaryAgent{
		ID:              user.ID,
		Nickname:        user.Nickname,
		Kind:            user.Kind,
		Status:          status,
		ExpectedExitIP:  user.ExpectedExitIP,
		AWGIface:        user.AWGIface,
		LastSeenAt:      utcTimePtr(user.LastSeenAt),
		LastSeenAgeSec:  lastSeenAge,
		SSHHost:         stringValue(user.SSHHost),
		SSHPort:         int64Value(user.SSHPort),
		SSHUser:         stringValue(user.SSHUser),
		Arch:            stringValue(user.Arch),
		AgentVersion:    stringValue(user.LastDeployedVersion),
		Ring:            stringValue(user.Ring),
		PendingVersion:  stringValue(user.PendingVersion),
		PendingSince:    stringValue(user.PendingSince),
		LastDeploy:      stringValue(user.LastDeploy),
		DeployMode:      stringValue(user.DeployMode),
		AWGMURL:         safeDashboardAWGMURL(stringValue(user.AWGMURL)),
		AWGMAuth:        stringValue(user.AWGMAuth),
		ExpectedMAC:     stringValue(user.ExpectedMAC),
		HasTopic:        user.TelegramThreadID != nil && *user.TelegramThreadID != 0,
		ActiveIncidents: incidents,
	}
	if user.TelegramChatID != nil {
		agent.TelegramChatID = *user.TelegramChatID
	}
	if user.TelegramThreadID != nil {
		agent.TelegramThreadID = *user.TelegramThreadID
	}
	return agent
}

func safeDashboardAWGMURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if err := validateDashboardAWGMURL(raw); err != nil {
		return ""
	}
	return raw
}

func dashboardLastSeenAge(user db.User, now time.Time) *int64 {
	if user.LastSeenAt == nil {
		return nil
	}
	age := int64(now.UTC().Sub(user.LastSeenAt.UTC()).Seconds())
	if age < 0 {
		age = 0
	}
	return &age
}

type dashboardEnrollmentReq struct {
	Nickname           string `json:"nickname"`
	Kind               string `json:"kind"`
	TelegramChatID     int64  `json:"telegram_chat_id"`
	TelegramThreadID   int64  `json:"telegram_thread_id"`
	CustomTelegramChat bool   `json:"custom_telegram_chat"`
	DeployMode         string `json:"deploy_mode"`
	AWGMURL            string `json:"awgm_url"`
	AWGMAuth           string `json:"awgm_auth"`
	SSHHost            string `json:"ssh_host"`
	SSHPort            int64  `json:"ssh_port"`
	SSHUser            string `json:"ssh_user"`
	Arch               string `json:"arch"`
	Ring               string `json:"ring"`
	ExpectedMAC        string `json:"expected_mac"`
}

type dashboardEnrollmentResp struct {
	Nickname         string `json:"nickname"`
	BackendURL       string `json:"backend_url"`
	RawToken         string `json:"raw_token"`
	TelegramChatID   int64  `json:"telegram_chat_id"`
	TelegramThreadID int64  `json:"telegram_thread_id"`
	Message          string `json:"message"`
}

func dashboardEnrollmentHandler(d Deps) http.HandlerFunc {
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
		var req dashboardEnrollmentReq
		if !decodeWizardJSON(w, r, &req) {
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
		if !dashboardTelegramChatAllowed(d, req.TelegramChatID, req.CustomTelegramChat) {
			writeJSONError(w, http.StatusBadRequest, "invalid_telegram_chat", "telegram chat is not allowed")
			return
		}
		enrollment, userID, err := createAgentEnrollment(d.DB, req.Nickname, req.Kind, req.TelegramThreadID)
		if err != nil {
			switch {
			case errors.Is(err, errEnrollmentInvalidNickname):
				writeJSONError(w, http.StatusBadRequest, "invalid_nickname", "nickname must match ^[a-z][a-z0-9_-]{1,15}$")
			case errors.Is(err, errEnrollmentInvalidKind):
				writeJSONError(w, http.StatusBadRequest, "invalid_kind", "kind must be static or mobile")
			default:
				writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			}
			return
		}
		if err := d.DB.Users().UpdateTelegramTopic(userID, req.TelegramChatID, req.TelegramThreadID); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if dashboardEnrollmentHasDeployInfo(req) {
			if err := d.DB.Users().UpdateDeployInfo(enrollment.Nickname, dashboardDeployInfoFromEnrollmentReq(d.DB, enrollment.Nickname, req)); err != nil {
				writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
				return
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(dashboardEnrollmentResp{
			Nickname:         enrollment.Nickname,
			BackendURL:       wizardEnrollmentBackendURL(r, d.PublicBaseURL),
			RawToken:         enrollment.RawToken,
			TelegramChatID:   req.TelegramChatID,
			TelegramThreadID: req.TelegramThreadID,
			Message:          "Agent enrollment created. Save the token now; it will not be shown once this panel is closed.",
		})
	}
}

// dashboardEditAgentReq carries the operator-editable router/deploy metadata.
// System-managed fields (versions, pending deploy markers) are intentionally
// absent — the handler preserves them from the current row.
type dashboardEditAgentReq struct {
	Kind             string `json:"kind"`
	TelegramThreadID int64  `json:"telegram_thread_id"`
	DeployMode       string `json:"deploy_mode"`
	AWGMURL          string `json:"awgm_url"`
	AWGMAuth         string `json:"awgm_auth"`
	SSHHost          string `json:"ssh_host"`
	SSHPort          int64  `json:"ssh_port"`
	SSHUser          string `json:"ssh_user"`
	Arch             string `json:"arch"`
	Ring             string `json:"ring"`
	ExpectedMAC      string `json:"expected_mac"`
}

// dashboardEditAgentHandler updates an existing agent's router/deploy metadata
// (AWG Manager URL = router domain, SSH host, arch, ring, deploy mode, expected
// MAC, telegram topic) without re-enrolling. It merges with the current row so
// blank fields are preserved — UpdateDeployInfo overwrites ring/deploy_mode/
// awgm_*/expected_mac unconditionally, which would otherwise wipe them on a
// partial edit. Pair with the update_backend_url command to "resurrect" an
// agent after its domain changes.
func dashboardEditAgentHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
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
		var req dashboardEditAgentReq
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
		current, err := d.DB.Users().GetByNickname(nickname)
		if err != nil {
			if errors.Is(err, db.ErrUserNotFound) {
				writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if current == nil {
			writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
			return
		}
		if err := d.DB.Users().UpdateDeployInfo(nickname, dashboardEditDeployInfo(*current, req)); err != nil {
			if errors.Is(err, db.ErrUserNotFound) {
				writeJSONError(w, http.StatusNotFound, "user_not_found", "nickname not registered")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if d.Logger != nil {
			d.Logger.Info("dashboard agent edited", "nickname", nickname)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// dashboardEditDeployInfo builds a full DeployInfo from the edit request,
// preserving system-managed fields and keeping current values for blank
// overwrite-semantics fields (ring/deploy_mode/awgm_*/expected_mac).
func dashboardEditDeployInfo(current db.User, req dashboardEditAgentReq) db.DeployInfo {
	return db.DeployInfo{
		Kind:                req.Kind,
		ThreadID:            req.TelegramThreadID,
		SSHHost:             strings.TrimSpace(req.SSHHost),
		SSHPort:             req.SSHPort,
		SSHUser:             strings.TrimSpace(req.SSHUser),
		Arch:                strings.TrimSpace(req.Arch),
		LastDeployedVersion: stringValue(current.LastDeployedVersion),
		PendingVersion:      stringValue(current.PendingVersion),
		PendingSince:        stringValue(current.PendingSince),
		LastDeploy:          stringValue(current.LastDeploy),
		Ring:                firstNonEmptyTrimmed(req.Ring, stringValue(current.Ring)),
		DeployMode:          firstNonEmptyTrimmed(req.DeployMode, stringValue(current.DeployMode)),
		AWGMURL:             firstNonEmptyTrimmed(req.AWGMURL, stringValue(current.AWGMURL)),
		AWGMAuth:            firstNonEmptyTrimmed(req.AWGMAuth, stringValue(current.AWGMAuth)),
		ExpectedMAC:         firstNonEmptyTrimmed(req.ExpectedMAC, stringValue(current.ExpectedMAC)),
	}
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func validateDashboardAWGMURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("AWG Manager URL must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("AWG Manager URL scheme must be http or https")
	}
	return nil
}

func dashboardEnrollmentHasDeployInfo(req dashboardEnrollmentReq) bool {
	return req.DeployMode != "" || req.AWGMURL != "" || req.AWGMAuth != "" || req.SSHHost != "" ||
		req.SSHPort != 0 || req.SSHUser != "" || req.Arch != "" || req.Ring != "" || req.ExpectedMAC != ""
}

func dashboardDeployInfoFromEnrollmentReq(database *db.DB, nickname string, req dashboardEnrollmentReq) db.DeployInfo {
	info := db.DeployInfo{
		Kind:        req.Kind,
		ThreadID:    req.TelegramThreadID,
		SSHHost:     strings.TrimSpace(req.SSHHost),
		SSHPort:     req.SSHPort,
		SSHUser:     strings.TrimSpace(req.SSHUser),
		Arch:        strings.TrimSpace(req.Arch),
		Ring:        strings.TrimSpace(req.Ring),
		DeployMode:  strings.TrimSpace(req.DeployMode),
		AWGMURL:     strings.TrimSpace(req.AWGMURL),
		AWGMAuth:    strings.TrimSpace(req.AWGMAuth),
		ExpectedMAC: strings.TrimSpace(req.ExpectedMAC),
	}
	if database == nil {
		return info
	}
	current, err := database.Users().GetByNickname(nickname)
	if err != nil {
		return info
	}
	if info.SSHHost == "" {
		info.SSHHost = stringValue(current.SSHHost)
	}
	if info.SSHPort == 0 {
		info.SSHPort = int64Value(current.SSHPort)
	}
	if info.SSHUser == "" {
		info.SSHUser = stringValue(current.SSHUser)
	}
	if info.Arch == "" {
		info.Arch = stringValue(current.Arch)
	}
	info.LastDeployedVersion = stringValue(current.LastDeployedVersion)
	if info.Ring == "" {
		info.Ring = stringValue(current.Ring)
	}
	info.PendingVersion = stringValue(current.PendingVersion)
	info.PendingSince = stringValue(current.PendingSince)
	info.LastDeploy = stringValue(current.LastDeploy)
	if info.DeployMode == "" {
		info.DeployMode = stringValue(current.DeployMode)
	}
	if info.AWGMURL == "" {
		info.AWGMURL = stringValue(current.AWGMURL)
	}
	if info.AWGMAuth == "" {
		info.AWGMAuth = stringValue(current.AWGMAuth)
	}
	if info.ExpectedMAC == "" {
		info.ExpectedMAC = stringValue(current.ExpectedMAC)
	}
	return info
}

func dashboardTelegramChatAllowed(d Deps, chatID int64, custom bool) bool {
	if chatID == 0 || chatID == d.TelegramPrimaryChatID {
		return true
	}
	for _, extra := range d.TelegramExtraChatIDs {
		if chatID == extra {
			return true
		}
	}
	return custom
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func int64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func utcTimePtr(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	out := v.UTC()
	return &out
}
