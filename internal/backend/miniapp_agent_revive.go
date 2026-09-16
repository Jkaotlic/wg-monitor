package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// Оживление агента из мини-аппа (цикл 2б). Только админ, отказ 404, как у
// обновления агента: радиус -- переустановка агента на чужом роутере.
//
// Решение оператора: «Полный автомат: пароль root или вход в панель
// awg-manager вводится один раз при постановке, хранится на Pi зашифрованным
// до успеха, отмены или срока, затем стирается.» Этот файл -- единственная
// дверь, через которую пароль входит в систему. Поэтому: тело запроса не
// логируется, текст ошибки сервиса не логируется (только код), ни один
// ответ пароля не отражает.

const (
	miniappAgentReviveMaxBody = 8192
	miniappReviveDefaultDays  = 30
	miniappReviveMaxDays      = 30
)

// ReviveAPI -- то, что маршрутам мини-аппа нужно от сервиса оживления.
// Интерфейс, а не *revive.Service: тесты и песочница подставляют фейк.
type ReviveAPI interface {
	Enabled() bool
	Schedule(ctx context.Context, routerID int64, req revive.ScheduleRequest) (revive.Intent, error)
	Cancel(ctx context.Context, routerID int64) (bool, error)
	StatusFor(routerID int64) (*revive.IntentView, error)
}

var _ ReviveAPI = (*revive.Service)(nil)

// miniappRevive -- сервис для маршрутов или nil. Отдельная ветка на
// d.Revive != nil нужна, чтобы nil-указатель не превратился в не-nil
// интерфейс.
func miniappRevive(d Deps) ReviveAPI {
	if d.ReviveOverride != nil {
		return d.ReviveOverride
	}
	if d.Revive != nil {
		return d.Revive
	}
	return nil
}

func miniappReviveEnabled(d Deps) bool {
	svc := miniappRevive(d)
	return svc != nil && svc.Enabled()
}

func reviveErrorCode(err error) string {
	var re *revive.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

type miniappReviveRefusal struct {
	status int
	text   string
}

// Тексты -- для админа, без внутренних имён. «Панель роутера» -- так её
// называет остальной мини-апп (agentConfig.js).
var miniappReviveRefusals = map[string]miniappReviveRefusal{
	"revive_disabled":      {http.StatusServiceUnavailable, "Оживление агента не настроено на сервере."},
	"no_awgm_url":          {http.StatusBadRequest, "У роутера не записан адрес панели — укажите его."},
	"invalid_awgm_url":     {http.StatusBadRequest, "Нужен внешний адрес панели с https — например, имя KeenDNS. Локальные адреса не подходят."},
	"no_credentials":       {http.StatusBadRequest, "Нужен пароль root роутера."},
	"revive_running":       {http.StatusConflict, "Оживление уже идёт — дождитесь итога."},
	"router_not_found":     {http.StatusNotFound, "Роутер не найден."},
	"awgm_url_already_set": {http.StatusConflict, "Адрес панели у роутера уже записан. Поменять его можно в веб-дашборде."},
	"agent_alive":          {http.StatusConflict, "Агент на роутере отвечает — оживлять нечего."},
}

// writeMiniappReviveError -- отказ по коду сервиса. Неизвестный код -- 500 с
// общим текстом маршрута: текст самой ошибки наружу не идёт никогда.
func writeMiniappReviveError(w http.ResponseWriter, code, fallback string) {
	if ref, ok := miniappReviveRefusals[code]; ok {
		writeMiniappDeployError(w, ref.status, code, ref.text)
		return
	}
	writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, fallback)
}

type miniappAgentReviveReq struct {
	RootPassword string `json:"root_password"`
	AWGMLogin    string `json:"awgm_login"`
	AWGMPassword string `json:"awgm_password"`
	AWGMAPIKey   string `json:"awgm_api_key"`
	AWGMURL      string `json:"awgm_url"`
	ExpiresDays  int    `json:"expires_days"`
	Confirm      string `json:"confirm"`
}

// Заглушки печати: если кто-нибудь когда-нибудь положит запрос в журнал или
// в fmt.Errorf, секрет туда не попадёт.
func (miniappAgentReviveReq) String() string   { return "miniappAgentReviveReq{скрыто}" }
func (miniappAgentReviveReq) GoString() string { return "miniappAgentReviveReq{скрыто}" }
func (miniappAgentReviveReq) LogValue() slog.Value {
	return slog.StringValue("скрыто")
}

// credentialKinds -- какие виды входа пришли, без значений: для журнала.
func (r miniappAgentReviveReq) credentialKinds() string {
	var kinds []string
	if r.RootPassword != "" {
		kinds = append(kinds, "root")
	}
	if r.AWGMLogin != "" && r.AWGMPassword != "" {
		kinds = append(kinds, "panel_login")
	}
	if r.AWGMAPIKey != "" {
		kinds = append(kinds, "panel_key")
	}
	if len(kinds) == 0 {
		return "none"
	}
	return strings.Join(kinds, "+")
}

type miniappAgentReviveResp struct {
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
}

func miniappReviveAccepted(in revive.Intent) miniappAgentReviveResp {
	return miniappAgentReviveResp{Status: in.Status, ExpiresAt: in.ExpiresAt.UTC().Format(time.RFC3339)}
}

// miniappAgentReviveHandler -- POST /v1/miniapp/routers/{id}/agent/revive.
func miniappAgentReviveHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		svc := miniappRevive(d)
		if svc == nil || !svc.Enabled() {
			writeMiniappReviveError(w, "revive_disabled", "")
			return
		}
		var req miniappAgentReviveReq
		if err := json.NewDecoder(io.LimitReader(r.Body, miniappAgentReviveMaxBody)).Decode(&req); err != nil {
			writeMiniappDeployError(w, http.StatusBadRequest, "bad_request", "Не удалось прочитать запрос.")
			return
		}
		if req.ExpiresDays == 0 {
			req.ExpiresDays = miniappReviveDefaultDays
		}
		if req.ExpiresDays < 1 || req.ExpiresDays > miniappReviveMaxDays {
			writeMiniappDeployError(w, http.StatusBadRequest, "bad_request", "Срок ожидания — от 1 до 30 дней.")
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		if !confirmPhraseMatches(req.Confirm, u.Nickname) {
			writeMiniappDeployError(w, http.StatusBadRequest, "confirm_mismatch", "Имя роутера набрано неверно.")
			return
		}
		intent, err := svc.Schedule(r.Context(), u.ID, revive.ScheduleRequest{
			RootPassword: req.RootPassword,
			AWGMLogin:    strings.TrimSpace(req.AWGMLogin),
			AWGMPassword: req.AWGMPassword,
			AWGMAPIKey:   strings.TrimSpace(req.AWGMAPIKey),
			AWGMURL:      strings.TrimSpace(req.AWGMURL),
			ExpiresDays:  req.ExpiresDays,
			RequestedBy:  adminID,
		})
		if err != nil {
			code := reviveErrorCode(err)
			if d.Logger != nil {
				// Только код: текст ошибки сервиса может нести что угодно.
				d.Logger.Warn("miniapp agent revive refused",
					"router_id", u.ID, "code", code, "by", adminID)
			}
			writeMiniappReviveError(w, code, "Не удалось поставить оживление.")
			return
		}
		resp := miniappReviveAccepted(intent)
		if d.Logger != nil {
			d.Logger.Info("miniapp agent revive scheduled",
				"router_id", u.ID, "nickname", u.Nickname, "status", resp.Status,
				"expires_at", resp.ExpiresAt, "credentials", req.credentialKinds(),
				"panel_address_supplied", strings.TrimSpace(req.AWGMURL) != "", "by", adminID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// miniappAgentReviveCancelHandler -- DELETE /v1/miniapp/routers/{id}/agent/revive.
func miniappAgentReviveCancelHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		svc := miniappRevive(d)
		if svc == nil || !svc.Enabled() {
			writeMiniappReviveError(w, "revive_disabled", "")
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		cleared, err := svc.Cancel(r.Context(), u.ID)
		if err != nil {
			code := reviveErrorCode(err)
			if d.Logger != nil {
				d.Logger.Warn("miniapp agent revive cancel failed", "router_id", u.ID, "code", code, "by", adminID)
			}
			writeMiniappReviveError(w, code, "Не удалось отменить оживление.")
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp agent revive cancelled",
				"router_id", u.ID, "nickname", u.Nickname, "cleared", cleared, "by", adminID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			Cleared bool `json:"cleared"`
		}{Cleared: cleared})
	}
}
