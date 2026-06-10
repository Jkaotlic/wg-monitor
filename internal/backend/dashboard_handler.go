package backend

import (
	"crypto/subtle"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

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

func registerDashboardRoutes(mux *http.ServeMux, d Deps) {
	staticFS, err := fs.Sub(dashboardStaticFS, "dashboard_static")
	if err != nil {
		panic(err)
	}
	staticHandler := http.StripPrefix("/dashboard/", http.FileServer(http.FS(staticFS)))
	dashAuth := DashboardAuthMiddleware(d.DashboardToken, d.Logger)
	mux.Handle("GET /dashboard", requestIDMiddleware()(http.RedirectHandler("/dashboard/", http.StatusFound)))
	mux.Handle("GET /dashboard/", requestIDMiddleware()(staticHandler))
	mux.Handle("GET /v1/dashboard/summary", requestIDMiddleware()(dashAuth(dashboardSummaryHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/deploy", requestIDMiddleware()(dashAuth(wizardDeployHandler(d))))
	mux.Handle("POST /v1/dashboard/backend/deploy", requestIDMiddleware()(dashAuth(wizardBackendDeployHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/commands", requestIDMiddleware()(dashAuth(wizardCommandHandler(d))))
	mux.Handle("POST /v1/dashboard/agents/{nickname}/maintenance", requestIDMiddleware()(dashAuth(wizardMaintenanceHandler(d))))
	mux.Handle("GET /v1/dashboard/commands/{cmd_id}", requestIDMiddleware()(dashAuth(wizardCmdResultHandler(d))))
}

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
