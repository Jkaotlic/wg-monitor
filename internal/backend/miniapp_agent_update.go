// internal/backend/miniapp_agent_update.go
package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/rollout"
)

// Обновление агента из мини-аппа. Только админ: радиус -- агент на чужом
// роутере. Отказ 404, как у экрана «Парк»: по коду нельзя узнать, есть ли
// такая поверхность и такой роутер.

const miniappAgentUpdateMaxBody = 4096

type miniappAgentUpdateReq struct {
	Confirm       string `json:"confirm"`
	TargetVersion string `json:"target_version"`
	// AllowDowngrade -- админ сам выбрал версию ниже текущей и подтвердил
	// откат (спека цикла 2, п. 6). Без него agentDeployCore отказывает.
	AllowDowngrade bool `json:"allow_downgrade"`
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
		return miniappAdminOpsErrorText(deployErrDowngrade)
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
		opts.AllowDowngrade = req.AllowDowngrade
		opts.Context = r.Context()
		opts.ActorID = adminID
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
	Job     db.RolloutJob                   `json:"job"`
	Targets []db.RolloutTarget              `json:"targets"`
	Results []miniappFleetAgentUpdateResult `json:"results,omitempty"` // old clients ignore the new job fields safely
}

func miniappRolloutEngine(d Deps, ctx context.Context, actorID int64) (rollout.Engine, bool) {
	opts, ok := miniappAgentDeployOpts(d, "miniapp-fleet-canary")
	if !ok {
		return rollout.Engine{}, false
	}
	return rollout.Engine{
		DB: d.DB,
		Deploy: func(routerID int64, target string) (string, error) {
			u, err := d.DB.Users().GetByID(routerID)
			if err != nil {
				return "", err
			}
			local := opts
			local.Context, local.ActorID = ctx, actorID
			res, derr := agentDeployCore(d, u, target, local)
			if derr != nil {
				return "", derr
			}
			return res.CmdID, nil
		},
		Probe: func(routerID int64) rollout.Probe {
			u, err := d.DB.Users().GetByID(routerID)
			if err != nil {
				return rollout.Probe{ReportGap: true}
			}
			now := time.Now().UTC()
			fresh := u.LastSeenAt != nil && now.Sub(u.LastSeenAt.UTC()) <= 5*time.Minute
			hard, _ := d.DB.State().HardCount(routerID)
			return rollout.Probe{Fresh: fresh, Version: stringValue(u.LastDeployedVersion), NewHardIncidents: hard, ReportGap: !fresh}
		},
	}, true
}

// RunRolloutWorker advances the persisted canary state machine independently
// from the mini-app.  Therefore an observation window and the remaining fleet
// continue after a backend restart even when no operator keeps Park open.
func RunRolloutWorker(ctx context.Context, d Deps, interval time.Duration) {
	if d.DB == nil || d.CommandSink == nil {
		return
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	tick := func() {
		job, _, err := d.DB.Rollouts().Active()
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			if d.Logger != nil {
				d.Logger.Error("rollout worker read failed", "err", err)
			}
			return
		}
		engine, ok := miniappRolloutEngine(d, ctx, job.CreatedBy)
		if !ok {
			return
		}
		if _, err := engine.Tick(job.ID); err != nil && d.Logger != nil {
			d.Logger.Error("rollout worker tick failed", "job_id", job.ID, "err", err)
		}
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

func writeMiniappRollout(w http.ResponseWriter, d *db.DB, job db.RolloutJob) {
	current, targets, err := d.Rollouts().Get(job.ID)
	if err != nil {
		writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, "Не удалось прочитать раскатку.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(miniappFleetAgentUpdateResp{Job: current, Targets: targets})
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
		engine, ok := miniappRolloutEngine(d, context.WithoutCancel(r.Context()), adminID)
		if !ok {
			writeMiniappDeployError(w, http.StatusServiceUnavailable, "not_configured", miniappDeployErrorText("not_configured"))
			return
		}
		users, err := d.DB.Users().GetAll()
		if err != nil {
			writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, miniappDeployErrorText(errCodeInternal))
			return
		}
		if active, _, err := d.DB.Rollouts().Active(); err == nil {
			writeMiniappRollout(w, d.DB, active)
			return
		} else if !errors.Is(err, sql.ErrNoRows) {
			writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, "Не удалось прочитать текущую раскатку.")
			return
		}
		candidates := []rollout.Candidate{}
		candidateUsers := map[int64]db.User{}
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
			hard, _ := d.DB.State().HardCount(u.ID)
			fresh := u.LastSeenAt != nil && time.Since(u.LastSeenAt.UTC()) <= 5*time.Minute
			candidates = append(candidates, rollout.Candidate{RouterID: u.ID, Fresh: fresh, HardIncidents: hard})
			candidateUsers[u.ID] = *u
		}
		canary := rollout.SelectCanary(candidates)
		if canary == 0 {
			writeMiniappDeployError(w, http.StatusConflict, "no_targets", "Нет отставших агентов для обновления.")
			return
		}
		ids := []int64{canary}
		for _, candidate := range candidates {
			if candidate.RouterID != canary {
				ids = append(ids, candidate.RouterID)
			}
		}
		job, err := engine.Create(serverVersion, adminID, ids)
		if err != nil {
			writeMiniappDeployError(w, http.StatusConflict, "rollout_exists", "Раскатка уже идёт.")
			return
		}
		job, err = engine.Tick(job.ID)
		if err != nil {
			writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, "Не удалось запустить canary.")
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp fleet canary rollout started", "job_id", job.ID, "target_version", serverVersion, "canary_router_id", canary, "targets", len(candidateUsers), "by", adminID)
		}
		writeMiniappRollout(w, d.DB, job)
	}
}

func miniappFleetRolloutStatusHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		job, _, err := d.DB.Rollouts().Active()
		if errors.Is(err, sql.ErrNoRows) {
			// Normal idle state, not a failed network request.  Returning 200 keeps
			// the Park poll quiet in browser telemetry and layout diagnostics.
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(miniappFleetAgentUpdateResp{Targets: []db.RolloutTarget{}})
			return
		}
		if err != nil {
			writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, "Не удалось прочитать раскатку.")
			return
		}
		writeMiniappRollout(w, d.DB, job)
	}
}

func miniappFleetRolloutCancelHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := miniappAdminOrNotFound(d, w, r)
		_ = actor
		if !ok {
			return
		}
		job, _, err := d.DB.Rollouts().Active()
		if errors.Is(err, sql.ErrNoRows) {
			writeMiniappDeployError(w, http.StatusNotFound, "no_rollout", "Активной раскатки нет.")
			return
		}
		if err != nil {
			writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, "Не удалось прочитать раскатку.")
			return
		}
		engine, ok := miniappRolloutEngine(d, context.WithoutCancel(r.Context()), actor)
		if !ok {
			return
		}
		if err := engine.Cancel(job.ID); err != nil {
			writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, "Не удалось отменить раскатку.")
			return
		}
		job, _, _ = d.DB.Rollouts().Get(job.ID)
		writeMiniappRollout(w, d.DB, job)
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
