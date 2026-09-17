package backend

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
)

// Мастер «Добавить роутер» (спека цикла 2, п. 2-3). Два пути одного маршрута:
// provision -- поставить агента сейчас через терминал панели awg-manager
// (пароль root уходит на сервер один раз и живёт только в памяти задания);
// register -- только выдать токен и команду установки руками.
//
// Группа и тема Telegram здесь не спрашиваются и не трогаются: уведомления
// ушли в личку (решение оператора 14.09), владельца назначают после.

type miniappProvisionReq struct {
	Kind         string `json:"kind"`
	Nickname     string `json:"nickname"`
	AgentKind    string `json:"agent_kind"`
	AWGMURL      string `json:"awgm_url"`
	AWGMAuth     string `json:"awgm_auth"`
	RootPassword string `json:"root_password"`
	AWGMLogin    string `json:"awgm_login"`
	AWGMPassword string `json:"awgm_password"`
	AWGMAPIKey   string `json:"awgm_api_key"`
	Version      string `json:"version"`
	Confirm      string `json:"confirm"`
}

func (miniappProvisionReq) String() string       { return "miniappProvisionReq{скрыто}" }
func (miniappProvisionReq) GoString() string     { return "miniappProvisionReq{скрыто}" }
func (miniappProvisionReq) LogValue() slog.Value { return slog.StringValue("скрыто") }

type miniappProvisionStartResp struct {
	JobID    string `json:"job_id"`
	Nickname string `json:"nickname"`
}

type miniappRegisterResp struct {
	Nickname       string `json:"nickname"`
	RawToken       string `json:"raw_token"`
	BackendURL     string `json:"backend_url"`
	InstallCommand string `json:"install_command"`
}

// miniappProvisionHandler -- POST /v1/miniapp/provision.
func miniappProvisionHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		var req miniappProvisionReq
		if !decodeMiniappOpsBody(w, r, &req) {
			return
		}
		nickname := strings.TrimSpace(req.Nickname)
		if !confirmPhraseMatches(req.Confirm, nickname) {
			writeMiniappOpsError(w, http.StatusBadRequest, "confirm_mismatch")
			return
		}
		if d.DB == nil {
			writeMiniappOpsError(w, http.StatusServiceUnavailable, "db_not_configured")
			return
		}
		kind := provision.JobKind(strings.TrimSpace(req.Kind))
		if kind != provision.KindProvision && kind != provision.KindRegister {
			writeMiniappOpsError(w, http.StatusBadRequest, "invalid_kind")
			return
		}
		if !enrollmentNicknameRe.MatchString(nickname) {
			writeMiniappOpsError(w, http.StatusBadRequest, "invalid_nickname")
			return
		}
		agentKind := strings.TrimSpace(req.AgentKind)
		if agentKind != "" && !db.IsValidKind(agentKind) {
			writeMiniappOpsError(w, http.StatusBadRequest, "invalid_kind")
			return
		}
		existing, err := lookupExistingUser(d.DB, nickname)
		if err != nil {
			writeMiniappOpsError(w, http.StatusInternalServerError, errCodeInternal)
			return
		}
		// Имя живого роутера не перевыпускаем отсюда: приглашение сразу
		// перезаписало бы хеш токена работающего агента. Для него --
		// «Переустановить агент». Ни разу не выходивший на связь -- не помеха.
		if existing != nil && existing.LastSeenAt != nil {
			writeMiniappOpsError(w, http.StatusConflict, "nickname_taken")
			return
		}
		backendURL, ok := configuredPublicBackendURL(d.PublicBaseURL)
		if !ok {
			writeMiniappOpsError(w, http.StatusInternalServerError, "no_public_base_url")
			return
		}
		awgmURL := strings.TrimSpace(req.AWGMURL)
		awgmAuth := strings.TrimSpace(req.AWGMAuth)

		if kind == provision.KindRegister {
			if err := validateDashboardAWGMURL(awgmURL); err != nil {
				writeMiniappOpsError(w, http.StatusBadRequest, "invalid_awgm_url")
				return
			}
			// Приглашение перевыпускает токен -- под тем же замком, что
			// установка и переустановка: идущая установка этого роутера иначе
			// закоммитила бы на config_written токен, который приглашение уже
			// переписало. Без движка замка нет, и гонку делить не с кем.
			if d.Provision.Store != nil {
				release, locked := tryProvisionMintLock(d.Provision.Store, nickname)
				if !locked {
					writeMiniappOpsError(w, http.StatusConflict, "provision_already_running")
					return
				}
				defer release()
			}
			// Повторное приглашение без типа не сбрасывает «в машине» в «дома»:
			// createAgentEnrollment пустой тип считает static.
			if agentKind == "" && existing != nil {
				agentKind = existing.Kind
			}
			enrollment, serr := registerAgent(d, registerAgentInput{
				Nickname: nickname, AgentKind: agentKind, AWGMURL: awgmURL, AWGMAuth: awgmAuth,
			})
			if serr != nil {
				writeMiniappStartError(w, serr)
				return
			}
			installCommand := miniappManualInstallCommand(r.Context(), d, backendURL, enrollment)
			if d.Logger != nil {
				d.Logger.Info("miniapp provision: register", "nickname", enrollment.Nickname, "by", adminID)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(miniappRegisterResp{
				Nickname:       enrollment.Nickname,
				RawToken:       enrollment.RawToken,
				BackendURL:     backendURL,
				InstallCommand: installCommand,
			})
			return
		}

		if d.Provision.Store == nil {
			writeMiniappOpsError(w, http.StatusServiceUnavailable, "provision_not_configured")
			return
		}
		if awgmURL == "" || validateDashboardAWGMURL(awgmURL) != nil {
			writeMiniappOpsError(w, http.StatusBadRequest, "no_awgm_url")
			return
		}
		rootPassword := strings.TrimSpace(req.RootPassword)
		if rootPassword == "" {
			writeMiniappOpsError(w, http.StatusBadRequest, "root_password_required")
			return
		}
		jobID, version, serr := startProvisionInstall(r.Context(), d, provisionInstallCoreParams{
			Nickname:     nickname,
			AgentKind:    agentKind,
			AWGMURL:      awgmURL,
			AWGMAuth:     awgmAuth,
			RootPassword: rootPassword,
			AWGMLogin:    strings.TrimSpace(req.AWGMLogin),
			AWGMPassword: req.AWGMPassword,
			AWGMAPIKey:   strings.TrimSpace(req.AWGMAPIKey),
			Version:      miniappAgentVersionOrServer(req.Version),
			Existing:     existing,
		})
		if serr != nil {
			if d.Logger != nil {
				// Только код: текст ядра несёт сетевые подробности.
				d.Logger.Warn("miniapp provision refused", "nickname", nickname, "code", serr.Code, "by", adminID)
			}
			writeMiniappStartError(w, serr)
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp provision: install started", "nickname", nickname, "job_id", jobID, "version", version,
				"credentials", miniappCredentialKinds(req.RootPassword, req.AWGMLogin, req.AWGMPassword, req.AWGMAPIKey), "by", adminID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(miniappProvisionStartResp{JobID: jobID, Nickname: nickname})
	}
}

// miniappManualInstallCommand -- команда установки руками с суммами, которые
// сервер проверил подписью выпуска. Не получилось -- пусто: токен выдан, а
// скрипт без проверенных сумм экран не показывает.
func miniappManualInstallCommand(ctx context.Context, d Deps, backendURL string, enrollment wizardEnrollmentResp) string {
	tag, err := releaseorigin.ValidateReleaseTag(serverVersion)
	if err != nil {
		return ""
	}
	sums, err := provisionChecksums(d)(ctx, releaseDownloadBase, tag)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("miniapp provision: register without install command", "nickname", enrollment.Nickname, "reason", "checksums_failed")
		}
		return ""
	}
	return buildManualInstallCommand(backendURL, enrollment.Nickname, enrollment.RawToken, tag, sums)
}
