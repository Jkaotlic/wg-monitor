package backend

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Переустановка агента сейчас и перенаправление на другой бэкенд (спека
// цикла 2, п. 7-8). Оба -- через терминал панели awg-manager, пароль root
// уходит на сервер один раз и живёт только в памяти задания. Ход работы --
// GET /v1/miniapp/jobs/{job_id}.

type miniappJobStartResp struct {
	JobID string `json:"job_id"`
}

type miniappAgentReinstallReq struct {
	RootPassword string `json:"root_password"`
	AWGMLogin    string `json:"awgm_login"`
	AWGMPassword string `json:"awgm_password"`
	AWGMAPIKey   string `json:"awgm_api_key"`
	Version      string `json:"version"`
	Confirm      string `json:"confirm"`
}

func (miniappAgentReinstallReq) String() string       { return "miniappAgentReinstallReq{скрыто}" }
func (miniappAgentReinstallReq) GoString() string     { return "miniappAgentReinstallReq{скрыто}" }
func (miniappAgentReinstallReq) LogValue() slog.Value { return slog.StringValue("скрыто") }

type miniappAgentRepointReq struct {
	RootPassword  string `json:"root_password"`
	NewBackendURL string `json:"new_backend_url"`
	AWGMLogin     string `json:"awgm_login"`
	AWGMPassword  string `json:"awgm_password"`
	AWGMAPIKey    string `json:"awgm_api_key"`
	Confirm       string `json:"confirm"`
}

func (miniappAgentRepointReq) String() string       { return "miniappAgentRepointReq{скрыто}" }
func (miniappAgentRepointReq) GoString() string     { return "miniappAgentRepointReq{скрыто}" }
func (miniappAgentRepointReq) LogValue() slog.Value { return slog.StringValue("скрыто") }

func writeMiniappJobStarted(w http.ResponseWriter, jobID string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(miniappJobStartResp{JobID: jobID})
}

// miniappAgentReinstallHandler -- POST /v1/miniapp/routers/{id}/agent/reinstall.
// Только роутеру на связи: спящему и молчащему -- «Оживить агент», который
// дождётся его сам. Статус -- то же правило, что откладывает обновление
// (miniappWakeWindow), чтобы кнопка и отказ не расходились.
func miniappAgentReinstallHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		var req miniappAgentReinstallReq
		if !decodeMiniappOpsBody(w, r, &req) {
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		if !confirmPhraseMatches(req.Confirm, u.Nickname) {
			writeMiniappOpsError(w, http.StatusBadRequest, "confirm_mismatch")
			return
		}
		if asleep, _, _ := miniappWakeWindow(d, u, "self_update", time.Now().UTC()); asleep {
			writeMiniappOpsError(w, http.StatusConflict, "router_offline")
			return
		}
		rootPassword := strings.TrimSpace(req.RootPassword)
		if rootPassword == "" {
			writeMiniappOpsError(w, http.StatusBadRequest, "root_password_required")
			return
		}
		jobID, version, serr := startRepairReinstall(r.Context(), d, u.Nickname, u, reinstallInput{
			RootPassword: rootPassword,
			AWGMLogin:    strings.TrimSpace(req.AWGMLogin),
			AWGMPassword: req.AWGMPassword,
			AWGMAPIKey:   strings.TrimSpace(req.AWGMAPIKey),
			Version:      miniappAgentVersionOrServer(req.Version),
		})
		if serr != nil {
			if d.Logger != nil {
				d.Logger.Warn("miniapp agent reinstall refused", "router_id", u.ID, "code", serr.Code, "by", adminID)
			}
			writeMiniappStartError(w, serr)
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp agent reinstall started", "router_id", u.ID, "nickname", u.Nickname, "job_id", jobID,
				"version", version, "credentials", miniappCredentialKinds(req.RootPassword, req.AWGMLogin, req.AWGMPassword, req.AWGMAPIKey), "by", adminID)
		}
		writeMiniappJobStarted(w, jobID)
	}
}

// miniappAgentRepointHandler -- POST /v1/miniapp/routers/{id}/agent/repoint.
// Роутер «не на связи» -- не помеха: агент, который стучится на другой сервер,
// отсюда и выглядит молчащим.
func miniappAgentRepointHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		var req miniappAgentRepointReq
		if !decodeMiniappOpsBody(w, r, &req) {
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		if !confirmPhraseMatches(req.Confirm, u.Nickname) {
			writeMiniappOpsError(w, http.StatusBadRequest, "confirm_mismatch")
			return
		}
		rootPassword := strings.TrimSpace(req.RootPassword)
		if rootPassword == "" {
			writeMiniappOpsError(w, http.StatusBadRequest, "root_password_required")
			return
		}
		jobID, newURL, serr := startRepairRepoint(d, u.Nickname, u, repointInput{
			RootPassword:  rootPassword,
			NewBackendURL: req.NewBackendURL,
			AWGMLogin:     req.AWGMLogin,
			AWGMPassword:  req.AWGMPassword,
			AWGMAPIKey:    req.AWGMAPIKey,
		})
		if serr != nil {
			if d.Logger != nil {
				d.Logger.Warn("miniapp agent repoint refused", "router_id", u.ID, "code", serr.Code, "by", adminID)
			}
			writeMiniappStartError(w, serr)
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp agent repoint started", "router_id", u.ID, "nickname", u.Nickname, "job_id", jobID,
				"new_backend_url", newURL, "credentials", miniappCredentialKinds(req.RootPassword, req.AWGMLogin, req.AWGMPassword, req.AWGMAPIKey), "by", adminID)
		}
		writeMiniappJobStarted(w, jobID)
	}
}
