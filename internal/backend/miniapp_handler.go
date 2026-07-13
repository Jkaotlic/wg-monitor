package backend

import (
	"encoding/json"
	"net/http"
	"time"
)

func registerMiniappRoutes(mux *http.ServeMux, d Deps) {
	reqID := requestIDMiddleware()
	auth := MiniAppAuthMiddleware(d.TelegramBotToken, d.Logger)

	mux.Handle("POST /v1/miniapp/session", reqID(miniappSessionHandler(d)))
	mux.Handle("GET /v1/miniapp/routers", reqID(auth(miniappRoutersHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}", reqID(auth(miniappRouterDetailHandler(d))))
	mux.Handle("GET /v1/miniapp/routers/{id}/events", reqID(auth(miniappRouterEventsHandler(d))))
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

// miniappIsAdmin mirrors callbacks.Router.isAdminTG: an unset AdminUserID (0)
// is a defensive fallback that config-loading never actually allows in
// production (Telegram.AdminUserID is required non-zero) — kept here only so
// the two admin checks can't silently diverge.
func miniappIsAdmin(telegramUserID, adminUserID int64) bool {
	return adminUserID == 0 || telegramUserID == adminUserID
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

// miniappRouterDetailHandler and miniappRouterEventsHandler are trivial
// stubs so this package compiles and Task 7's own tests can run standalone.
// Task 9 replaces these. Do not implement these for real here — that's out
// of scope for Task 8.
func miniappRouterDetailHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
}
func miniappRouterEventsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
}
