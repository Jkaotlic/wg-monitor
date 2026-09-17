package backend

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Подключение агента (спека цикла 2, п. 5): адрес и способ входа в панель
// awg-manager, SSH, режим раскатки, архитектура, канал, ожидаемый MAC. Это
// метаданные доступа к роутеру -- поэтому отдельный админский срез, а не поле
// /fleet (TestMiniappFleetNeverLeaksRouterSecrets). Правка -- то же ядро, что у
// дашборда: пустое поле оставляет прежнее значение, очистить нельзя.

type miniappAgentConnection struct {
	AWGMURL     string `json:"awgm_url"`
	AWGMAuth    string `json:"awgm_auth"`
	SSHHost     string `json:"ssh_host"`
	SSHPort     int64  `json:"ssh_port"`
	SSHUser     string `json:"ssh_user"`
	DeployMode  string `json:"deploy_mode"`
	Arch        string `json:"arch"`
	Ring        string `json:"ring"`
	ExpectedMAC string `json:"expected_mac"`
}

// miniappAgentConnectionGetHandler -- GET /v1/miniapp/routers/{id}/agent/connection.
func miniappAgentConnectionGetHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := miniappAdminOrNotFound(d, w, r); !ok {
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(miniappAgentConnection{
			AWGMURL:     stringValue(u.AWGMURL),
			AWGMAuth:    stringValue(u.AWGMAuth),
			SSHHost:     stringValue(u.SSHHost),
			SSHPort:     int64Value(u.SSHPort),
			SSHUser:     stringValue(u.SSHUser),
			DeployMode:  stringValue(u.DeployMode),
			Arch:        stringValue(u.Arch),
			Ring:        stringValue(u.Ring),
			ExpectedMAC: stringValue(u.ExpectedMAC),
		})
	}
}

// miniappAgentConnectionPutHandler -- PUT /v1/miniapp/routers/{id}/agent/connection.
func miniappAgentConnectionPutHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		var body miniappAgentConnection
		if !decodeMiniappOpsBody(w, r, &body) {
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		// Kind и тема здесь не правятся: пустые значения ядро сохраняет.
		edit := dashboardEditAgentReq{
			DeployMode:  body.DeployMode,
			AWGMURL:     body.AWGMURL,
			AWGMAuth:    body.AWGMAuth,
			SSHHost:     body.SSHHost,
			SSHPort:     body.SSHPort,
			SSHUser:     body.SSHUser,
			Arch:        body.Arch,
			Ring:        body.Ring,
			ExpectedMAC: body.ExpectedMAC,
		}
		if serr := validateAgentEdit(&edit); serr != nil {
			writeMiniappStartError(w, serr)
			return
		}
		if err := d.DB.Users().UpdateDeployInfo(u.Nickname, dashboardEditDeployInfo(*u, edit)); err != nil {
			if errors.Is(err, db.ErrUserNotFound) {
				writeMiniappOpsError(w, http.StatusNotFound, "not_found")
				return
			}
			writeMiniappOpsError(w, http.StatusInternalServerError, errCodeInternal)
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp agent connection edited", "router_id", u.ID, "nickname", u.Nickname, "by", adminID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
