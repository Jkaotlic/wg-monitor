package backend

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// «Прислать .conf в личку» (цикл 3, решение 6). Мини-апп по контракту
// содержимое конфига клиенту не отдаёт (miniapp_vpn.go), поэтому файл едет не
// в браузер, а документом в личку Telegram тому, кто нажал. В веб-управлении
// нажавший -- админ, и файл уходит в его личку.

type miniappSendConfReq struct {
	Provider   string `json:"provider"`
	OptionID   string `json:"option_id"`
	InstanceID string `json:"instance_id"`
}

type miniappSendConfResp struct {
	SentTo string `json:"sent_to"`
}

// miniappSendConfCaption -- подпись под файлом: чей роутер и что внутри.
const miniappSendConfCaption = "\nВ файле приватный ключ — не пересылайте его."

func miniappSendConfHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Самый широкий гейт маршрута -- админ или владелец -- до тела; свой
		// сервер дополнительно сужается до админа ниже.
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetOwner)
		if !ok {
			return
		}
		tgUser, _ := miniappUserFromContext(r.Context())
		if d.MiniappDocs == nil {
			writeMiniappCabinetError(w, http.StatusServiceUnavailable, "dm_not_configured")
			return
		}
		var req miniappSendConfReq
		if !decodeMiniappCabinetBody(w, r, &req) {
			return
		}
		provider := strings.ToLower(strings.TrimSpace(req.Provider))
		option := strings.TrimSpace(req.OptionID)
		switch provider {
		case "amnezia", "hidemyname":
			if d.VPNCabinet == nil {
				writeMiniappCabinetError(w, http.StatusServiceUnavailable, "cabinets_not_configured")
				return
			}
			if option == "" {
				writeMiniappCabinetError(w, http.StatusBadRequest, "missing_option")
				return
			}
			issued, err := d.VPNCabinet.IssueConfig(r.Context(), u.ID, provider, option)
			if errors.Is(err, ErrVPNSlotBusy) {
				writeMiniappCabinetError(w, http.StatusConflict, "slot_busy")
				return
			}
			if err != nil || len(issued.Conf) == 0 {
				miniappCabinetLogger(d).Warn("файл в личку: кабинет не выдал конфиг", "router_id", u.ID, "provider", provider, "option", option, "err", err)
				writeMiniappCabinetError(w, http.StatusBadGateway, "cabinet_failed")
				return
			}
			miniappSendConfDocument(d, w, r, u.ID, tgUser, provider, option, issued.TunnelName+".conf", u.Nickname+" — "+issued.TunnelName, issued.Conf)
		case "selfhosted":
			if !miniappIsAdmin(tgUser, d.TelegramAdminUserID) {
				writeMiniappDeployError(w, http.StatusNotFound, "not_found", "Роутер не найден")
				return
			}
			if d.SelfHosted == nil {
				writeMiniappCabinetError(w, http.StatusServiceUnavailable, "selfhosted_not_configured")
				return
			}
			instID := strings.TrimSpace(req.InstanceID)
			if instID == "" {
				writeMiniappCabinetError(w, http.StatusBadRequest, "missing_instance")
				return
			}
			// Каждый файл -- новый клиент на своём сервере (отзыва пиров пока
			// нет -- бэклог); экран предупреждает об этом до нажатия.
			issued, inst, err := d.SelfHosted.Issue(r.Context(), instID, miniappSelfHostedClientName(u.Nickname))
			if err != nil {
				miniappSelfHostedIssueError(d, w, err)
				return
			}
			name := selfhostedamnezia.TunnelName(inst.ID, u.Nickname)
			miniappSendConfDocument(d, w, r, u.ID, tgUser, provider, inst.ID, name+".conf", u.Nickname+" — свой сервер «"+inst.Label+"»", issued.Config)
		default:
			writeMiniappCabinetError(w, http.StatusBadRequest, "unknown_provider")
		}
	}
}

// miniappSendConfDocument отправляет готовый конфиг документом в личку и
// отвечает клиенту. Содержимое не попадает ни в ответ, ни в журнал.
func miniappSendConfDocument(d Deps, w http.ResponseWriter, r *http.Request, routerID, tgUser int64, provider, option, filename, caption string, conf []byte) {
	_, err := d.MiniappDocs.SendDocument(r.Context(), tgUser, nil, filename, conf, caption+miniappSendConfCaption)
	switch {
	case err == nil:
		miniappCabinetLogger(d).Info("файл в личку отправлен", "router_id", routerID, "provider", provider, "option", option, "tg_user", tgUser)
		writeMiniappCabinetJSON(w, http.StatusAccepted, miniappSendConfResp{SentTo: "dm"})
	case tg.IsUnreachableChat(err):
		writeMiniappCabinetError(w, http.StatusConflict, "dm_unreachable")
	default:
		miniappCabinetLogger(d).Warn("файл в личку: Telegram не принял", "router_id", routerID, "provider", provider, "err", err)
		writeMiniappCabinetError(w, http.StatusBadGateway, "dm_failed")
	}
}
