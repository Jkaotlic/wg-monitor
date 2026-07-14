package backend

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"time"
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
	mux.Handle("POST /v1/miniapp/routers/{id}/incidents/{check}/silence", reqID(auth(miniappSilenceHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/incidents/{check}/ack", reqID(auth(miniappAckHandler(d))))
	mux.Handle("POST /v1/miniapp/routers/{id}/incidents/{check}/mute", reqID(auth(miniappMuteHandler(d))))
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
		resp := miniappRoutersResp{}
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
	Router miniappRouterSummary `json:"router"`
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
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(miniappRouterResp{Router: miniappRouterSummaryFromAgent(a)})
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
		resp := miniappRouterEventsResp{}
		for _, row := range rows {
			resp.Checks = append(resp.Checks, miniappCheckStatus{
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
