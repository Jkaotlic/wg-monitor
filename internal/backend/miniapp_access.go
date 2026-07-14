package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type miniappAccessOwner struct {
	TelegramUserID int64 `json:"telegram_user_id"`
}

type miniappAccessOperator struct {
	TelegramUserID int64  `json:"telegram_user_id"`
	GrantedBy      int64  `json:"granted_by"`
	GrantedAt      string `json:"granted_at"` // RFC3339 UTC
}

type miniappAccessResp struct {
	Owner     *miniappAccessOwner     `json:"owner"`
	Operators []miniappAccessOperator `json:"operators"`
}

// buildMiniappAccess composes a router's owner (users.telegram_user_id) and its
// operators. routerID is users.id. Returns db.ErrUserNotFound if the router
// row doesn't exist, so callers can map it to 404.
func buildMiniappAccess(d Deps, routerID int64) (miniappAccessResp, error) {
	user, err := d.DB.Users().GetByID(routerID)
	if err != nil {
		return miniappAccessResp{}, err
	}
	resp := miniappAccessResp{}
	if user.TelegramUserID != nil && *user.TelegramUserID != 0 {
		resp.Owner = &miniappAccessOwner{TelegramUserID: *user.TelegramUserID}
	}
	ops, err := d.DB.RouterOperators().List(routerID)
	if err != nil {
		return miniappAccessResp{}, err
	}
	for _, op := range ops {
		resp.Operators = append(resp.Operators, miniappAccessOperator{
			TelegramUserID: op.TelegramUserID,
			GrantedBy:      op.GrantedBy,
			GrantedAt:      op.GrantedAt.UTC().Format(time.RFC3339),
		})
	}
	return resp, nil
}

// miniappRequireAdmin returns (adminTelegramID, true) if the caller is the
// configured admin; otherwise it writes 403 and returns (0, false). Admin is
// checked BEFORE any router-existence check so a non-admin never learns whether
// a given router id exists.
func miniappRequireAdmin(d Deps, w http.ResponseWriter, r *http.Request) (int64, bool) {
	telegramUserID, _ := miniappUserFromContext(r.Context())
	if !miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "admin only")
		return 0, false
	}
	return telegramUserID, true
}

// miniappRespondAccess writes the router's current access object (or 500).
func miniappRespondAccess(d Deps, w http.ResponseWriter, routerID int64) {
	resp, err := buildMiniappAccess(d, routerID)
	if errors.Is(err, db.ErrUserNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "access lookup failed")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

func miniappAccessHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := miniappRequireAdmin(d, w, r); !ok {
			return
		}
		routerID, ok := parseMiniappRouterID(r)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		resp, err := buildMiniappAccess(d, routerID)
		if errors.Is(err, db.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "access lookup failed")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func miniappAddOperatorHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappRequireAdmin(d, w, r)
		if !ok {
			return
		}
		routerID, ok := parseMiniappRouterID(r)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		var body struct {
			TelegramUserID int64 `json:"telegram_user_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid body")
			return
		}
		if body.TelegramUserID <= 0 {
			writeJSONError(w, http.StatusBadRequest, "bad_operator_id", "telegram_user_id must be positive")
			return
		}
		if _, err := d.DB.Users().GetByID(routerID); errors.Is(err, db.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		} else if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "router lookup failed")
			return
		}
		if err := d.DB.RouterOperators().Add(routerID, body.TelegramUserID, adminID); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "add operator failed")
			return
		}
		miniappRespondAccess(d, w, routerID)
	}
}

func miniappRemoveOperatorHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := miniappRequireAdmin(d, w, r); !ok {
			return
		}
		routerID, ok := parseMiniappRouterID(r)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		tgid, err := strconv.ParseInt(r.PathValue("tgid"), 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "operator not found")
			return
		}
		if err := d.DB.RouterOperators().Remove(routerID, tgid); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "remove operator failed")
			return
		}
		miniappRespondAccess(d, w, routerID)
	}
}

func miniappUnbindOwnerHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := miniappRequireAdmin(d, w, r); !ok {
			return
		}
		routerID, ok := parseMiniappRouterID(r)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if _, err := d.DB.Users().GetByID(routerID); errors.Is(err, db.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		} else if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "router lookup failed")
			return
		}
		if err := d.DB.Users().SetTelegramUserID(routerID, 0); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "unbind owner failed")
			return
		}
		miniappRespondAccess(d, w, routerID)
	}
}
