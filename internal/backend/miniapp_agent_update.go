// internal/backend/miniapp_agent_update.go
package backend

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Обновление агента из мини-аппа. Только админ: радиус -- агент на чужом
// роутере. Отказ 404, как у экрана «Парк»: по коду нельзя узнать, есть ли
// такая поверхность и такой роутер.

const miniappAgentUpdateMaxBody = 4096

type miniappAgentUpdateReq struct {
	Confirm       string `json:"confirm"`
	TargetVersion string `json:"target_version"`
}

type miniappAgentUpdateResp struct {
	Queued        bool   `json:"queued"`
	Deferred      bool   `json:"deferred"`
	TargetVersion string `json:"target_version"`
}

// miniappAdminOrNotFound -- гейт админских маршрутов обновления. В отличие от
// miniappRequireAdmin (403) отвечает 404, как GET /v1/miniapp/fleet.
func miniappAdminOrNotFound(d Deps, w http.ResponseWriter, r *http.Request) (int64, bool) {
	telegramUserID, _ := miniappUserFromContext(r.Context())
	if !miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
		writeMiniappDeployError(w, http.StatusNotFound, "not_found", "not found")
		return 0, false
	}
	return telegramUserID, true
}

// writeMiniappDeployError пишет код отказа под двумя ключами: "code" читает
// клиент (miniapp/src/api.js), "error" -- имя из контракта экрана «Парк».
func writeMiniappDeployError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}{Code: code, Error: code, Message: message})
}

// miniappDeployErrorText -- отказ словами. Сервер отдаёт и код, и текст:
// экран может показать текст как есть.
//
// Ключ -- обычно код ответа (JSON "code"/"error"), но для причин, у которых
// один код прикрывает разные по смыслу тексты в разных маршрутах (массовое
// обновление подтверждается словом «обновить», а не именем роутера; отказ
// «не настроено» бывает по трём разным причинам), здесь отдельные,
// не-wire ключи -- см. их комментарии ниже.
func miniappDeployErrorText(code string) string {
	switch code {
	case "confirm_mismatch":
		return "Имя роутера набрано неверно."
	// fleet_confirm_mismatch -- тот же wire-код confirm_mismatch, но текст
	// про слово «обновить», а не про имя роутера (B5c): массовое обновление
	// подтверждается фразой, а не ником конкретного роутера.
	case "fleet_confirm_mismatch":
		return "Для подтверждения наберите «обновить»."
	case "agent_too_old":
		return "Агент на роутере слишком старый, чтобы обновиться сам: нужна переустановка."
	case deployErrPending:
		return "Обновление этому роутеру уже назначено."
	case deployErrDowngrade:
		return "На роутере уже стоит версия новее выбранной."
	case deployErrNoRelease:
		return "Такой версии агента нет среди выпусков."
	// Три причины "not_configured" (B5a): их нельзя было различить по тексту,
	// хотя чинить их админу нужно по-разному. Wire-код у всех троих остаётся
	// "not_configured" -- клиент по нему не ветвится (агентUpdate.js не знает
	// этого кода и показывает общий текст), различие только в сообщении.
	case "not_configured_db":
		return "У сервера не настроена база данных."
	case "not_configured_queue":
		return "У сервера не настроена очередь команд."
	case "not_configured":
		return "У сервера не задан публичный адрес: обновлению неоткуда скачаться."
	default:
		return "Не удалось назначить обновление."
	}
}

// miniappAgentDeployOpts -- адрес загрузки для мини-аппа. Заголовков
// обратного прокси, как у мастера, здесь нет: только настроенный
// public_base_url и public_ip.
func miniappAgentDeployOpts(d Deps, source string) (agentDeployOpts, bool) {
	base, ok := configuredPublicBackendURL(d.PublicBaseURL)
	if !ok {
		return agentDeployOpts{}, false
	}
	ip := strings.TrimSpace(d.PublicIP)
	return agentDeployOpts{
		RepoBaseURL: base,
		ResolveIP:   func() string { return ip },
		Source:      source,
	}, true
}

func miniappLoadRouterForAdmin(d Deps, w http.ResponseWriter, r *http.Request) (*db.User, bool) {
	if d.DB == nil {
		writeMiniappDeployError(w, http.StatusServiceUnavailable, "not_configured", miniappDeployErrorText("not_configured_db"))
		return nil, false
	}
	routerID, ok := parseMiniappRouterID(r)
	if !ok {
		writeMiniappDeployError(w, http.StatusNotFound, "not_found", "Роутер не найден.")
		return nil, false
	}
	u, err := d.DB.Users().GetByID(routerID)
	if errors.Is(err, db.ErrUserNotFound) {
		writeMiniappDeployError(w, http.StatusNotFound, "not_found", "Роутер не найден.")
		return nil, false
	}
	if err != nil {
		writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, miniappDeployErrorText(errCodeInternal))
		return nil, false
	}
	return u, true
}

// miniappAgentUpdateHandler -- POST /v1/miniapp/routers/{id}/agent/update.
func miniappAgentUpdateHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		var req miniappAgentUpdateReq
		if err := json.NewDecoder(io.LimitReader(r.Body, miniappAgentUpdateMaxBody)).Decode(&req); err != nil {
			writeMiniappDeployError(w, http.StatusBadRequest, "bad_request", "Не удалось прочитать запрос.")
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		if !confirmPhraseMatches(req.Confirm, u.Nickname) {
			writeMiniappDeployError(w, http.StatusBadRequest, "confirm_mismatch", miniappDeployErrorText("confirm_mismatch"))
			return
		}
		verdict := agentUpdateVerdictFor(stringValue(u.LastDeployedVersion), serverVersion)
		if verdict.TooOld || verdict.Unknown {
			writeMiniappDeployError(w, http.StatusConflict, "agent_too_old", miniappDeployErrorText("agent_too_old"))
			return
		}
		if d.CommandSink == nil {
			writeMiniappDeployError(w, http.StatusServiceUnavailable, "not_configured", miniappDeployErrorText("not_configured_queue"))
			return
		}
		opts, ok := miniappAgentDeployOpts(d, "miniapp")
		if !ok {
			writeMiniappDeployError(w, http.StatusServiceUnavailable, "not_configured", miniappDeployErrorText("not_configured"))
			return
		}
		target := strings.TrimSpace(req.TargetVersion)
		if target == "" {
			target = serverVersion
		}
		// Выпуска новее самого бэкенда просто не существует: мирор
		// /v1/releases/download раздаёт то, что зеркалит бэкенд, и версии
		// агента вперёд себя он предложить не может. agentDeployCore проверяет
		// только ФОРМАТ тега (releaseorigin.ValidateReleaseTag), не его
		// реальность (B5d).
		if compareDashboardReleaseTags(target, serverVersion) > 0 {
			writeMiniappDeployError(w, http.StatusConflict, deployErrNoRelease, miniappDeployErrorText(deployErrNoRelease))
			return
		}
		res, derr := agentDeployCore(d, u, target, opts)
		if derr != nil {
			writeMiniappDeployError(w, derr.Status, derr.Code, miniappDeployErrorText(derr.Code))
			return
		}
		asleep, _, _ := miniappWakeWindow(d, u, "self_update", time.Now().UTC())
		if d.Logger != nil {
			d.Logger.Info("miniapp agent update queued",
				"router_id", u.ID, "nickname", u.Nickname, "target_version", res.TargetVersion,
				"deferred", asleep, "by", adminID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(miniappAgentUpdateResp{Queued: true, Deferred: asleep, TargetVersion: res.TargetVersion})
	}
}

// fleetAgentUpdateConfirm -- слово подтверждения массового обновления.
const fleetAgentUpdateConfirm = "обновить"

type miniappFleetAgentUpdateResult struct {
	RouterID   int64  `json:"router_id"`
	Nickname   string `json:"nickname"`
	Outcome    string `json:"outcome"`
	ReasonCode string `json:"reason_code"`
	ReasonText string `json:"reason_text"`
}

type miniappFleetAgentUpdateResp struct {
	Results []miniappFleetAgentUpdateResult `json:"results"`
}

// miniappFleetAgentUpdateHandler -- POST /v1/miniapp/fleet/agent/update.
// Назначает обновление до версии бэкенда всем отставшим. Выключенный или
// спящий роутер -- не ошибка: команда и отметка ждут его включения.
func miniappFleetAgentUpdateHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		var req struct {
			Confirm string `json:"confirm"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, miniappAgentUpdateMaxBody)).Decode(&req); err != nil {
			writeMiniappDeployError(w, http.StatusBadRequest, "bad_request", "Не удалось прочитать запрос.")
			return
		}
		if normalizeConfirmPhrase(req.Confirm) != fleetAgentUpdateConfirm {
			writeMiniappDeployError(w, http.StatusBadRequest, "confirm_mismatch", miniappDeployErrorText("fleet_confirm_mismatch"))
			return
		}
		if d.DB == nil {
			writeMiniappDeployError(w, http.StatusServiceUnavailable, "not_configured", miniappDeployErrorText("not_configured_db"))
			return
		}
		if d.CommandSink == nil {
			writeMiniappDeployError(w, http.StatusServiceUnavailable, "not_configured", miniappDeployErrorText("not_configured_queue"))
			return
		}
		if _, ok := parseDashboardReleaseTagRank(serverVersion); !ok {
			writeMiniappDeployError(w, http.StatusConflict, deployErrNoRelease,
				"Сервер собран без номера выпуска: обновлять агентов не до чего.")
			return
		}
		opts, ok := miniappAgentDeployOpts(d, "miniapp-fleet")
		if !ok {
			writeMiniappDeployError(w, http.StatusServiceUnavailable, "not_configured", miniappDeployErrorText("not_configured"))
			return
		}
		users, err := d.DB.Users().GetAll()
		if err != nil {
			writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, miniappDeployErrorText(errCodeInternal))
			return
		}
		now := time.Now().UTC()
		resp := miniappFleetAgentUpdateResp{Results: []miniappFleetAgentUpdateResult{}}
		counts := map[string]int{}
		for i := range users {
			u := &users[i]
			// agentUpdateVerdictFor держит Behind=false для агентов ниже
			// agentSelfUpdateFloor (B6): такой роутер self_update не умеет
			// вовсе, и ему тут не место -- ни в счётчике, ни в этом отчёте.
			// Экран «Парк» покажет причину отдельно, через
			// agent_update_warning (/fleet).
			verdict := agentUpdateVerdictFor(stringValue(u.LastDeployedVersion), serverVersion)
			if !verdict.Behind {
				continue
			}
			row := miniappFleetAgentUpdateResult{RouterID: u.ID, Nickname: u.Nickname}
			switch {
			case strings.TrimSpace(stringValue(u.PendingVersion)) != "":
				row.Outcome, row.ReasonCode = "skipped", deployErrPending
			default:
				if _, derr := agentDeployCore(d, u, serverVersion, opts); derr != nil {
					if derr.Code == deployErrPending {
						row.Outcome, row.ReasonCode = "skipped", deployErrPending
					} else {
						row.Outcome, row.ReasonCode = "error", derr.Code
					}
				} else if asleep, _, _ := miniappWakeWindow(d, u, "self_update", now); asleep {
					row.Outcome, row.ReasonCode = "deferred", "router_asleep"
					row.ReasonText = "Роутер не на связи: обновится, когда выйдет на связь."
				} else {
					row.Outcome = "queued"
					row.ReasonText = "Обновление отправлено."
				}
			}
			if row.ReasonText == "" {
				row.ReasonText = miniappDeployErrorText(row.ReasonCode)
			}
			counts[row.Outcome]++
			resp.Results = append(resp.Results, row)
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp fleet agent update",
				"target_version", serverVersion, "by", adminID,
				"queued", counts["queued"], "deferred", counts["deferred"],
				"skipped", counts["skipped"], "error", counts["error"])
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// miniappAgentUpdateCancelHandler -- POST /v1/miniapp/routers/{id}/agent/update/cancel.
func miniappAgentUpdateCancelHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		cleared, dropped, err := cancelAgentDeploy(d, u)
		if err != nil {
			writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, "Не удалось отменить обновление.")
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp agent update cancelled",
				"router_id", u.ID, "nickname", u.Nickname, "cleared", cleared, "dropped_commands", dropped, "by", adminID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			Cleared bool `json:"cleared"`
		}{Cleared: cleared})
	}
}
