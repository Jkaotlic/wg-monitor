package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
)

// Мастер замены конфига со стороны HTTP: запустить и посмотреть, как идёт.
//
// Задание живёт в бэкенде, а не в экране: операция длиннее любого запроса и
// обязана переживать закрытие приложения. Поэтому запуск отвечает сразу, а
// экран потом спрашивает состояние -- с любого устройства и в любой момент.

type miniappReplaceReq struct {
	Provider    string `json:"provider"`
	OptionID    string `json:"option_id"`
	OldTunnelID string `json:"old_tunnel_id"`
	PolicyName  string `json:"policy_name"`
}

type miniappReplaceStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type miniappReplaceResp struct {
	JobID string               `json:"job_id,omitempty"`
	State string               `json:"state,omitempty"`
	Hint  string               `json:"hint,omitempty"`
	Steps []miniappReplaceStep `json:"steps,omitempty"`
	// Running -- идёт ли замена прямо сейчас. Экран рисует по нему шаги
	// вместо кнопки: параллельные замены запрещены.
	Running bool `json:"running"`
}

func miniappReplaceJobResp(job provision.Job) miniappReplaceResp {
	out := miniappReplaceResp{
		JobID:   job.ID,
		State:   string(job.State),
		Hint:    job.Hint,
		Running: job.State == provision.StateRunning,
	}
	for _, s := range job.Steps {
		out.Steps = append(out.Steps, miniappReplaceStep{Name: s.Name, Status: string(s.Status), Detail: s.Detail})
	}
	return out
}

// miniappReplaceStartHandler запускает замену. Доступно всем, у кого есть
// доступ к роутеру: замена конфига -- это починка (прежний туннель остаётся
// на месте), а дробить права оператора значит показывать ему кнопку, которая
// ему запрещена (спека рабочего места, §4.2).
func miniappReplaceStartHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if d.Replace == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "config replacement is not configured")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req miniappReplaceReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		provider := strings.ToLower(strings.TrimSpace(req.Provider))
		known := false
		for _, p := range miniappVPNProviders {
			if p == provider {
				known = true
				break
			}
		}
		if !known {
			writeJSONError(w, http.StatusBadRequest, "unknown_provider", "provider is not one this app knows")
			return
		}
		if strings.TrimSpace(req.OptionID) == "" || strings.TrimSpace(req.OldTunnelID) == "" || strings.TrimSpace(req.PolicyName) == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_fields", "option_id, old_tunnel_id and policy_name are required")
			return
		}
		// Заменяемый туннель проверяется по событиям ЭТОГО роутера -- та же
		// граница, что у всех остальных туннельных действий: чужой или
		// выдуманный идентификатор сюда не проходит.
		if _, ok := miniappResolveTunnelArgs(d, routerID, strings.TrimSpace(req.OldTunnelID)); !ok {
			writeJSONError(w, http.StatusBadRequest, "unknown_tunnel", "old_tunnel_id does not match a known tunnel on this router")
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

		// Версия агента -- та, о которой роутер сообщил сам в последнем
		// отчёте. Мастер по ней решает, доживёт ли задание до конца: команд
		// promote и tunnel_power у старого агента нет, а узнать это на
		// четвёртом шаге -- значит уже потратить конфиг и завести на роутере
		// лишний туннель.
		agentVersion := ""
		if u.LastDeployedVersion != nil {
			agentVersion = *u.LastDeployedVersion
		}
		jobID, err := d.Replace.Start(replace.StartReq{
			RouterID:     routerID,
			Nickname:     u.Nickname,
			Provider:     provider,
			OptionID:     strings.TrimSpace(req.OptionID),
			OldTunnelID:  strings.TrimSpace(req.OldTunnelID),
			PolicyName:   strings.TrimSpace(req.PolicyName),
			AgentVersion: agentVersion,
		})
		if errors.Is(err, replace.ErrAgentTooOld) {
			// Отказ по состоянию роутера, а не по форме запроса: экран должен
			// сказать «обновите агента», а не «что-то пошло не так».
			writeJSONError(w, http.StatusBadRequest, "agent_too_old", err.Error())
			return
		}
		if errors.Is(err, replace.ErrAlreadyRunning) {
			// Не ошибка запроса, а состояние роутера: экран покажет идущую
			// операцию вместо кнопки.
			writeJSONError(w, http.StatusConflict, "already_running", err.Error())
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp replace started",
				"nickname", u.Nickname, "user_id", u.ID, "provider", provider, "option", req.OptionID, "job_id", jobID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(miniappReplaceResp{JobID: jobID, State: string(provision.StateRunning), Running: true})
	}
}

// miniappReplaceStatusHandler отвечает, что сейчас с заменой на этом роутере.
// Спрашивают именно про роутер, а не про идентификатор задания: операцию
// могли запустить с другого устройства.
func miniappReplaceStatusHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if d.Replace == nil || d.Replace.Store == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "config replacement is not configured")
			return
		}
		u, err := d.DB.Users().GetByID(routerID)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		job, ok := d.Replace.Store.LatestFor(u.Nickname)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if !ok {
			// Пустой ответ -- это «замен не было», а не ошибка.
			_ = json.NewEncoder(w).Encode(miniappReplaceResp{})
			return
		}
		_ = json.NewEncoder(w).Encode(miniappReplaceJobResp(job))
	}
}
