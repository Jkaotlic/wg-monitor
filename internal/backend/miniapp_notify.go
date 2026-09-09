package backend

import (
	"encoding/json"
	"net/http"
)

// miniappNotifyResp -- состояние личного выключателя уведомлений.
type miniappNotifyResp struct {
	Muted bool `json:"muted"`
}

// miniappNotifyHandler ставит и снимает личный выключатель уведомлений на
// роутер.
//
// Выключатель именно личный: у роутера несколько получателей (владелец и
// операторы), и решение каждого касается только его. Доступ к экранам он не
// трогает -- заглушивший по-прежнему заходит и смотрит, что происходит.
func miniappNotifyHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		var body struct {
			Muted bool `json:"muted"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid body")
			return
		}
		if err := d.DB.NotifyMutes().SetMuted(telegramUserID, routerID, body.Muted); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "notify toggle failed")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(miniappNotifyResp{Muted: body.Muted})
	}
}
