package backend

import (
	"encoding/json"
	"net/http"
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

// miniappRoutersHandler, miniappRouterDetailHandler, and
// miniappRouterEventsHandler are trivial stubs so this package compiles and
// this task's own tests can run standalone. Task 8 replaces
// miniappRoutersHandler with a real implementation; Task 9 replaces the other
// two. Do not implement these for real here — that's out of scope for Task 7.
func miniappRoutersHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
}
func miniappRouterDetailHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
}
func miniappRouterEventsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
}
