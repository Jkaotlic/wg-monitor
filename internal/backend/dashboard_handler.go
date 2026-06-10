package backend

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

const dashboardSessionCookieName = "wg_dashboard_session"

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
	mux.Handle("POST /v1/dashboard/agents/{nickname}/deploy", requestIDMiddleware()(dashAuth(wizardDeployHandler(d))))
	mux.Handle("POST /v1/dashboard/backend/deploy", requestIDMiddleware()(dashAuth(wizardBackendDeployHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/commands", requestIDMiddleware()(dashAuth(wizardCommandHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/maintenance", requestIDMiddleware()(dashAuth(wizardMaintenanceHandler(d))))
	mux.Handle("GET /v1/dashboard/commands/{cmd_id}", requestIDMiddleware()(dashAuth(wizardCmdResultHandler(d))))
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
		Value:    dashboardSessionValue(token),
		Path:     "/",
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
	want := dashboardSessionValue(token)
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(want)) == 1
}

func dashboardSessionValue(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("wg-monitor-dashboard-session-v1"))
	return "v1:" + hex.EncodeToString(mac.Sum(nil))
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
	Status      string                  `json:"status"`
	Version     string                  `json:"version"`
	GeneratedAt string                  `json:"generated_at"`
	Totals      dashboardSummaryTotals  `json:"totals"`
	Agents      []dashboardSummaryAgent `json:"agents"`
}

type dashboardSummaryTotals struct {
	Agents         int `json:"agents"`
	Online         int `json:"online"`
	Offline        int `json:"offline"`
	Alerts         int `json:"alerts"`
	PendingDeploys int `json:"pending_deploys"`
}

type dashboardSummaryAgent struct {
	ID              int64               `json:"id"`
	Nickname        string              `json:"nickname"`
	Kind            string              `json:"kind"`
	Status          string              `json:"status"`
	ExpectedExitIP  string              `json:"expected_exit_ip"`
	AWGIface        string              `json:"awg_iface"`
	LastSeenAt      *time.Time          `json:"last_seen_at,omitempty"`
	LastSeenAgeSec  *int64              `json:"last_seen_age_sec,omitempty"`
	SSHHost         string              `json:"ssh_host"`
	SSHPort         int64               `json:"ssh_port"`
	SSHUser         string              `json:"ssh_user"`
	Arch            string              `json:"arch"`
	AgentVersion    string              `json:"agent_version"`
	Ring            string              `json:"ring"`
	PendingVersion  string              `json:"pending_version"`
	PendingSince    string              `json:"pending_since"`
	LastDeploy      string              `json:"last_deploy"`
	DeployMode      string              `json:"deploy_mode"`
	AWGMURL         string              `json:"awgm_url"`
	HasTopic        bool                `json:"has_topic"`
	ActiveIncidents []dashboardIncident `json:"active_incidents"`
}

type dashboardIncident struct {
	CheckName string `json:"check_name"`
	HardSince string `json:"hard_since"`
	FailCount int    `json:"fail_count"`
}

func dashboardSummaryHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if d.DB == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "db_not_configured", "db not configured")
			return
		}
		resp, err := buildDashboardSummary(d.DB, time.Now().UTC())
		if err != nil {
			if d.Logger != nil {
				d.Logger.Error("dashboard summary failed", "err", err)
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "dashboard summary failed")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func buildDashboardSummary(database *db.DB, now time.Time) (dashboardSummary, error) {
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
		agent := dashboardAgentFromUser(user, hardsByUser[user.ID], now)
		resp.Agents = append(resp.Agents, agent)
		resp.Totals.Agents++
		switch agent.Status {
		case "alert":
			resp.Totals.Alerts++
		case "online":
			resp.Totals.Online++
		case "offline":
			resp.Totals.Offline++
		}
		if agent.PendingVersion != "" {
			resp.Totals.PendingDeploys++
		}
	}
	return resp, nil
}

func dashboardAgentFromUser(user db.User, incidents []dashboardIncident, now time.Time) dashboardSummaryAgent {
	status := "online"
	if len(incidents) > 0 {
		status = "alert"
	} else if user.LastSeenAt == nil {
		status = "offline"
	}

	var lastSeenAge *int64
	if user.LastSeenAt != nil {
		age := int64(now.Sub(user.LastSeenAt.UTC()).Seconds())
		if age < 0 {
			age = 0
		}
		lastSeenAge = &age
	}

	return dashboardSummaryAgent{
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
		AWGMURL:         stringValue(user.AWGMURL),
		HasTopic:        user.TelegramThreadID != nil && *user.TelegramThreadID != 0,
		ActiveIncidents: incidents,
	}
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
