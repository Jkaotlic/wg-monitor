package backend

import (
	"encoding/json"
	"net/http"
	"strings"
)

type miniappBackendDeployReq struct {
	TargetVersion string `json:"target_version"`
	Confirm       string `json:"confirm"`
}

type miniappBackendDeployResp struct {
	Accepted      bool   `json:"accepted"`
	TargetVersion string `json:"target_version"`
}

// miniappBackendDeployHandler -- POST /v1/miniapp/backend/deploy. Подтверждение
// -- набор версии целиком. Откат бэкенда отсюда не делается (спека цикла 2,
// п. 1): бэкенд старше -- аварийный путь, allow_downgrade не читается вовсе.
// Адрес зеркала -- только настроенный public_base_url: заголовкам прокси, как
// у мастера, здесь неоткуда взяться.
func miniappBackendDeployHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		var req miniappBackendDeployReq
		if !decodeMiniappOpsBody(w, r, &req) {
			return
		}
		target := strings.TrimSpace(req.TargetVersion)
		if target == "" || strings.TrimSpace(req.Confirm) != target {
			writeMiniappOpsError(w, http.StatusBadRequest, "confirm_mismatch")
			return
		}
		if strings.TrimSpace(d.BackendUpdatePath) == "" {
			writeMiniappOpsError(w, http.StatusServiceUnavailable, "backend_update_not_configured")
			return
		}
		base, hasBase := configuredPublicBackendURL(d.PublicBaseURL)
		if !hasBase {
			writeMiniappOpsError(w, http.StatusInternalServerError, "no_public_base_url")
			return
		}
		queued, serr := queueBackendUpdate(d, backendUpdateInput{
			TargetVersion: target,
			RepoBase: func() (string, bool) {
				return base, true
			},
		})
		if serr != nil {
			if serr.Code == "downgrade_rejected" {
				writeMiniappDeployError(w, serr.Status, serr.Code, miniappAdminOpsErrorText("backend_downgrade_rejected"))
				return
			}
			writeMiniappStartError(w, serr)
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp backend deploy queued", "target_version", queued.TargetVersion, "by", adminID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(miniappBackendDeployResp{Accepted: true, TargetVersion: queued.TargetVersion})
	}
}
