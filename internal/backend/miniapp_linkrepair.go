package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
)

// Починка линии со стороны HTTP. Формы ответов повторяют мастер замены
// (miniapp_replace.go) намеренно: экран починки и экран замены читаются на
// клиенте одним кодом, а задание у них и правда одного устройства.

type miniappRepairReq struct {
	CheckName string `json:"check_name"`
}

type miniappRepairAutoReq struct {
	Enabled bool `json:"enabled"`
}

// miniappRepairStartHandler запускает починку по кнопке. Доступно всем, у
// кого есть доступ к роутеру: починка ничего не удаляет, а прежний туннель
// остаётся на месте выключенным.
func miniappRepairStartHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if d.LinkRepair == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "link repair is not configured")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req miniappRepairReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		check := strings.TrimSpace(req.CheckName)
		if check == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_fields", "check_name is required")
			return
		}
		u, err := d.DB.Users().GetByID(routerID)
		if errors.Is(err, db.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "router lookup failed")
			return
		}
		agentVersion := ""
		if u.LastDeployedVersion != nil {
			agentVersion = *u.LastDeployedVersion
		}

		jobID, err := d.LinkRepair.Start(linkrepair.StartReq{
			RouterID:     routerID,
			Nickname:     u.Nickname,
			CheckName:    check,
			AgentVersion: agentVersion,
		})
		switch {
		case errors.Is(err, linkrepair.ErrNoScenario):
			// Не ошибка запроса, а свойство поломки: чинить отсюда нечем, и
			// экран обязан сказать это словами, а не «что-то пошло не так».
			writeJSONError(w, http.StatusUnprocessableEntity, "no_scenario",
				"эту поломку отсюда починить нечем")
			return
		case errors.Is(err, linkrepair.ErrUnknownOrigin):
			writeJSONError(w, http.StatusUnprocessableEntity, "unknown_origin",
				"не помним, каким конфигом поднята эта линия — перевыпустить нечего")
			return
		case errors.Is(err, linkrepair.ErrAlreadyRunning):
			writeJSONError(w, http.StatusConflict, "already_running", err.Error())
			return
		case errors.Is(err, replace.ErrAgentTooOld):
			writeJSONError(w, http.StatusBadRequest, "agent_too_old", err.Error())
			return
		case err != nil:
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp link repair started",
				"nickname", u.Nickname, "user_id", u.ID, "check", check, "job_id", jobID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(miniappReplaceResp{
			JobID: jobID, State: string(provision.StateRunning), Running: true,
		})
	}
}

// miniappRepairStatusHandler отдаёт ход последней починки этого роутера.
func miniappRepairStatusHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if d.LinkRepair == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "link repair is not configured")
			return
		}
		u, err := d.DB.Users().GetByID(routerID)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		job, ok := d.LinkRepair.Store.LatestFor(u.Nickname)
		if !ok || job.Kind != linkrepair.KindLinkRepair {
			// Починки не было -- это не ошибка. Пустой ответ честнее 404:
			// экран рисует кнопку, а не сообщение о пропаже.
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(miniappReplaceResp{})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(miniappReplaceJobResp(job))
	}
}

// miniappRepairAutoHandler включает и выключает полуавтомат на роутере.
func miniappRepairAutoHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req miniappRepairAutoReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		if err := d.DB.RepairSettings().SetAutoRepair(routerID, req.Enabled); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "settings not saved")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": req.Enabled})
	}
}
