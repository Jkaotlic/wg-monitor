package backend

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func registerMiniappRoutes(mux *http.ServeMux, d Deps) {
	reqID := requestIDMiddleware()
	auth := MiniAppAuthMiddleware(d.TelegramBotToken, d.Logger)

	staticFS, err := fs.Sub(miniappStaticFS, "miniapp_static")
	if err != nil {
		panic(err)
	}
	// The app shell (HTML/JS/CSS) carries no sensitive data — only the JSON
	// API calls it makes are auth-gated, same principle as any SPA's public
	// login page. Telegram must be able to load it before a session exists.
	staticHandler := http.StripPrefix("/miniapp/", http.FileServer(http.FS(staticFS)))
	mux.Handle("GET /miniapp/", reqID(staticHandler))

	mux.Handle("POST /v1/miniapp/session", reqID(miniappSessionHandler(d)))
	mux.Handle("GET /v1/miniapp/routers", reqID(auth(miniappRoutersHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}", reqID(auth(miniappRouterDetailHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/events", reqID(auth(miniappRouterEventsHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/timeline", reqID(auth(miniappRouterTimelineHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/commands", reqID(auth(miniappCommandHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/commands/{cmd_id}", reqID(auth(miniappCommandResultHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/incidents/{check}/silence", reqID(auth(miniappSilenceHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/incidents/{check}/ack", reqID(auth(miniappAckHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/incidents/{check}/mute", reqID(auth(miniappMuteHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/incidents/{check}/history", reqID(auth(miniappHistoryHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/access", reqID(auth(miniappAccessHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/access/operators", reqID(auth(miniappAddOperatorHandler(d))))
	mux.Handle("DELETE /v1/miniapp/routers/{id}/access/operators/{tgid}", reqID(auth(miniappRemoveOperatorHandler(d))))
	mux.Handle("DELETE /v1/miniapp/routers/{id}/access/owner", reqID(auth(miniappUnbindOwnerHandler(d))))
}

type miniappSessionReq struct {
	InitData string `json:"init_data"`
}

type miniappSessionResp struct {
	OK             bool  `json:"ok"`
	TelegramUserID int64 `json:"telegram_user_id"`
	IsAdmin        bool  `json:"is_admin"`
}

func miniappSessionHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req miniappSessionReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InitData == "" {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "init_data required")
			return
		}
		user, err := verifyInitData(req.InitData, d.TelegramBotToken, miniappNow())
		if err != nil {
			if d.Logger != nil {
				d.Logger.Warn("miniapp session: init_data rejected", "err", err)
			}
			writeJSONError(w, http.StatusUnauthorized, "invalid_init_data", "could not verify Telegram init data")
			return
		}
		http.SetCookie(w, miniappSessionCookie(r, d.TelegramBotToken, user.ID))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(miniappSessionResp{
			OK:             true,
			TelegramUserID: user.ID,
			IsAdmin:        miniappIsAdmin(user.ID, d.TelegramAdminUserID),
		})
	}
}

// miniappIsAdmin enforces a fail-closed access gate for the mini app: only the
// explicitly configured TelegramAdminUserID is treated as admin. Unlike the
// bot's callbacks.Router.isAdminTG (which is fail-open for UI hints only),
// miniappIsAdmin is the actual authorization boundary: it controls fleet
// visibility and query access. Returning true grants complete router access,
// so it must reject any unset adminUserID (0) even though config-loading
// prevents that in practice.
func miniappIsAdmin(telegramUserID, adminUserID int64) bool {
	return adminUserID != 0 && telegramUserID == adminUserID
}

type miniappRouterSummary struct {
	ID              int64               `json:"id"`
	Nickname        string              `json:"nickname"`
	Status          string              `json:"status"`
	LastSeenAt      *time.Time          `json:"last_seen_at,omitempty"`
	LastSeenAgeSec  *int64              `json:"last_seen_age_sec,omitempty"`
	ActiveIncidents []dashboardIncident `json:"active_incidents,omitempty"`
}

func miniappRouterSummaryFromAgent(a dashboardSummaryAgent) miniappRouterSummary {
	return miniappRouterSummary{
		ID:              a.ID,
		Nickname:        a.Nickname,
		Status:          a.Status,
		LastSeenAt:      a.LastSeenAt,
		LastSeenAgeSec:  a.LastSeenAgeSec,
		ActiveIncidents: a.ActiveIncidents,
	}
}

type miniappRoutersResp struct {
	Routers []miniappRouterSummary `json:"routers"`
}

// miniappRoutersHandler lists the routers visible to the caller: the full
// fleet for the Telegram admin, or only routers telegramUserID owns or
// operates (via db.AccessibleRouterIDs) for everyone else. The response
// deliberately excludes dashboardSummaryAgent's SSH/AWGM/deploy-metadata
// fields — mini-app callers include non-admin operators who shouldn't see
// those.
func miniappRoutersHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		summary, err := buildDashboardSummary(d.DB, time.Now().UTC(), dashboardStatusPolicyFromDeps(d))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "fleet summary failed")
			return
		}
		// Пустой слайс, а не nil: клиент делает .map по этому полю, и null
		// уронил бы экран человека, которому пока не выдали ни одного роутера.
		resp := miniappRoutersResp{Routers: []miniappRouterSummary{}}
		if miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
			for _, a := range summary.Agents {
				resp.Routers = append(resp.Routers, miniappRouterSummaryFromAgent(a))
			}
		} else {
			allowed, err := d.DB.AccessibleRouterIDs(telegramUserID)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "access lookup failed")
				return
			}
			allowedSet := make(map[int64]bool, len(allowed))
			for _, id := range allowed {
				allowedSet[id] = true
			}
			for _, a := range summary.Agents {
				if allowedSet[a.ID] {
					resp.Routers = append(resp.Routers, miniappRouterSummaryFromAgent(a))
				}
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

type miniappRouterResp struct {
	Router    miniappRouterSummary `json:"router"`
	Incidents []miniappIncident    `json:"incidents"`
}

func miniappRouterDetailHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		summary, err := buildDashboardSummary(d.DB, time.Now().UTC(), dashboardStatusPolicyFromDeps(d))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "fleet summary failed")
			return
		}
		for _, a := range summary.Agents {
			if a.ID == routerID {
				incidents, err := d.DB.State().HardIncidentsForUser(routerID)
				if err != nil {
					writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "incident lookup failed")
					return
				}
				resp := miniappRouterResp{Router: miniappRouterSummaryFromAgent(a)}
				// The enriched top-level Incidents supersedes the summary's
				// lightweight active_incidents on the detail view; omitempty
				// then drops it from the JSON. (Kept on the fleet-list summary.)
				resp.Router.ActiveIncidents = nil
				for _, st := range incidents {
					resp.Incidents = append(resp.Incidents, miniappIncidentFromState(st))
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
		}
		writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
	}
}

type miniappCheckStatus struct {
	CheckName string `json:"check_name"`
	Status    string `json:"status"`
	Timestamp string `json:"ts"`
}

type miniappRouterEventsResp struct {
	Checks []miniappCheckStatus `json:"checks"`
	// Tunnels is the projection of the tunnel_* rows' details -- the screen's
	// "are my tunnels alive" answer. Checks keeps carrying the same rows in flat
	// form for backwards compatibility; the two are views of one query, not two
	// sources of truth.
	Tunnels []miniappTunnel `json:"tunnels"`
	Traffic miniappTraffic  `json:"traffic"`
}

// miniappEventsWindow bounds how far back "the latest status of every
// check" looks. A pragmatic default, not a hard requirement — easy to tune
// later if it proves too short/long in practice.
const miniappEventsWindow = 30 * 24 * time.Hour

func miniappRouterEventsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		// An empty prefix matches every check_name, so this returns one row
		// per check: its latest known status — the same query shape the
		// bot's tunnels panel/alert formatter already use, not a full
		// chronological history.
		rows, err := d.DB.Events().LatestEventsByPrefixSince(routerID, "", time.Now().UTC().Add(-miniappEventsWindow))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "events lookup failed")
			return
		}
		resp := miniappRouterEventsResp{Tunnels: []miniappTunnel{}}
		byCheck := make(map[string]db.EventRow, len(rows))
		for _, row := range rows {
			byCheck[row.CheckName] = row
			resp.Checks = append(resp.Checks, miniappCheckStatus{
				CheckName: row.CheckName,
				Status:    row.Status,
				Timestamp: row.TS.UTC().Format(time.RFC3339),
			})
			if tu, ok := miniappTunnelFromEvent(row); ok {
				resp.Tunnels = append(resp.Tunnels, tu)
			}
		}
		sort.Slice(resp.Tunnels, func(i, j int) bool { return resp.Tunnels[i].TunnelID < resp.Tunnels[j].TunnelID })
		resp.Traffic = miniappDeriveTraffic(resp.Tunnels, byCheck)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// miniappTimelineEvent is one row of the router's timeline: what changed and
// when. Deliberately NOT the same shape as miniappTransition (the per-incident
// history): that one answers "how did THIS check flap", this one answers "what
// happened on the router", and squeezing both into one type would force every
// future reader to work out which question they are looking at.
type miniappTimelineEvent struct {
	CheckName string `json:"check_name"`
	Status    string `json:"status"`
	Timestamp string `json:"ts"`
}

type miniappTimelineResp struct {
	Events []miniappTimelineEvent `json:"events"`
	// Days is the window actually applied, not the one asked for -- the client
	// prints it, so a clamped request must not be reported back as honoured.
	Days      int  `json:"days"`
	Truncated bool `json:"truncated"`
}

const (
	miniappTimelineDefaultDays = 7
	miniappTimelineMaxDays     = 30
	// Столько строк экран ещё способен показать осмысленно; всё, что сверх,
	// помечается truncated -- молча показанная часть выглядела бы как целое.
	miniappTimelineMaxRows = 500
)

// miniappTimelineDays reads the window from the query string. Anything absent,
// unparseable, or out of range falls back to the default instead of erroring:
// a timeline is a read-only screen, and refusing to draw it over a bad query
// parameter would be a worse answer than drawing the usual week.
func miniappTimelineDays(raw string) int {
	if raw == "" {
		return miniappTimelineDefaultDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return miniappTimelineDefaultDays
	}
	if n > miniappTimelineMaxDays {
		return miniappTimelineMaxDays
	}
	return n
}

func miniappRouterTimelineHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		days := miniappTimelineDays(r.URL.Query().Get("days"))
		since := time.Now().UTC().AddDate(0, 0, -days)
		// Запрашиваем на одну строку больше предела: только так видно, что
		// строки кончились не потому, что событий больше нет.
		rows, err := d.DB.Events().ListAllSince(routerID, since, miniappTimelineMaxRows+1)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "timeline lookup failed")
			return
		}
		resp := miniappTimelineResp{Events: []miniappTimelineEvent{}, Days: days}
		if len(rows) > miniappTimelineMaxRows {
			rows = rows[:miniappTimelineMaxRows]
			resp.Truncated = true
		}
		for _, row := range rows {
			resp.Events = append(resp.Events, miniappTimelineEvent{
				CheckName: row.CheckName,
				Status:    row.Status,
				Timestamp: row.TS.UTC().Format(time.RFC3339),
			})
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func parseMiniappRouterID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func miniappRouterAllowed(d Deps, telegramUserID, routerID int64) bool {
	if miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
		return true
	}
	role, err := d.DB.RouterAccessRole(routerID, telegramUserID)
	return err == nil && role != ""
}
