package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/premiumfiles"
)

// PremiumFiles is called only by explicit owner actions, never background repair.
type PremiumFiles interface {
	OpenPremiumFiles(context.Context, int64, string, string) (premiumfiles.Account, string, error)
	DownloadPremiumFile(context.Context, int64, string, string, string) ([]byte, error)
}

type premiumFileRequest struct {
	VPNKey          string `json:"vpn_key"`
	KeyID           string `json:"key_id"`
	Country         string `json:"country"`
	DeclaredCountry string `json:"declared_country"`
	Confirm         bool   `json:"confirm"`
}

func (premiumFileRequest) String() string   { return "[скрыто]" }
func (premiumFileRequest) GoString() string { return "[скрыто]" }

func premiumFileFailure(w http.ResponseWriter, err error, download bool) {
	switch {
	case errors.Is(err, ErrCabinetSecretNotFound):
		writeMiniappCabinetError(w, http.StatusNotFound, "secret_not_found")
	case errors.Is(err, premiumfiles.ErrRejected):
		writeMiniappCabinetError(w, http.StatusBadRequest, "cabinet_rejected")
	case errors.Is(err, premiumfiles.ErrBusy):
		writeMiniappDeployError(w, http.StatusConflict, "premium_busy", premiumfiles.ErrBusy.Error())
	case errors.Is(err, premiumfiles.ErrSlotsFull):
		writeMiniappDeployError(w, http.StatusConflict, "slot_busy", "Все места подписки заняты. Выберите уже выпущенную страну, чтобы скачать её конфиг.")
	default:
		if download {
			writeMiniappDeployError(w, http.StatusBadGateway, "premium_file_uncertain", "Ответ о выпуске не получен. Кабинет мог уже занять место: откройте подписку заново и проверьте страну перед повтором.")
			return
		}
		writeMiniappCabinetError(w, http.StatusBadGateway, "cabinet_failed")
	}
}

func miniappPremiumFilesHandler(d Deps, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetOwner)
		if !ok {
			return
		}
		files, ok := d.VPNCabinetKeys.(PremiumFiles)
		if !ok {
			writeMiniappCabinetError(w, http.StatusServiceUnavailable, "cabinets_not_configured")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, miniappCabinetMaxBody)
		var body premiumFileRequest
		if !decodeMiniappCabinetBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		if action == "account" {
			account, keyID, err := files.OpenPremiumFiles(ctx, u.ID, body.VPNKey, body.KeyID)
			if err != nil {
				premiumFileFailure(w, err, action != "account")
				return
			}
			writeMiniappCabinetJSON(w, http.StatusOK, map[string]any{"account": account, "key_id": keyID})
			return
		}
		if !body.Confirm || body.KeyID == "" || len(body.Country) != 2 || (body.DeclaredCountry != "ru" && body.DeclaredCountry != "ag") {
			writeMiniappCabinetError(w, http.StatusBadRequest, "invalid_field")
			return
		}
		if action == "install" && d.CommandSink == nil {
			writeMiniappCabinetError(w, http.StatusServiceUnavailable, "commands_not_configured")
			return
		}
		// Only exact subscription/country names are considered. Never ask the
		// agent to replace a tunnel by a coincidental private-address match.
		digest := sha256.Sum256([]byte(body.KeyID))
		name := fmt.Sprintf("amnezia_%s_%x", body.Country, digest[:6])
		if action == "install" && miniappTunnelNameTaken(d, u.ID, name) {
			writeMiniappDeployError(w, http.StatusConflict, "premium_already_installed", "Туннель этой подписки и страны уже есть на роутере. Откройте VPN-туннели и проверьте его состояние.")
			return
		}
		conf, err := files.DownloadPremiumFile(ctx, u.ID, body.KeyID, body.Country, body.DeclaredCountry)
		if err != nil {
			premiumFileFailure(w, err, action != "account")
			return
		}
		if action == "install" {
			routerCopy, valid := premiumRouterCopy(conf)
			if !valid {
				writeMiniappDeployError(w, http.StatusBadRequest, "premium_config_unsupported", "Файл получен, но его формат нельзя безопасно установить. Скачайте исходный .conf и проверьте его; повторный выпуск автоматически не выполняется.")
				return
			}
			cmdID, err := miniappEnqueueTunnelImportMode(d, u.ID, routerCopy, name, "nativewg", false)
			if err != nil {
				writeMiniappDeployError(w, http.StatusServiceUnavailable, "premium_import_not_queued", "Конфиг получен, но команда установки не сохранена. Проверьте страну в подписке перед повтором.")
				return
			}
			writeMiniappCabinetJSON(w, http.StatusOK, map[string]string{"cmd_id": cmdID, "tunnel_name": name})
			return
		}
		writeMiniappCabinetJSON(w, http.StatusOK, map[string]string{"filename": "amnezia_" + body.Country + ".conf", "conf": string(conf)})
	}
}

// premiumRouterCopy applies the owner's explicitly approved compatibility step
// only to the router copy. Retained lines, including CRLF, stay byte-identical.
func premiumRouterCopy(original []byte) ([]byte, bool) {
	var copy []byte
	for _, line := range bytes.SplitAfter(original, []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if strings.HasPrefix(trimmed, "# VPN Key:") && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(trimmed, "# VPN Key:")), "vpn://") {
			continue
		}
		copy = append(copy, line...)
	}
	if strings.Contains(strings.ToLower(string(copy)), "vpn://") {
		return nil, false
	}
	if _, ok := parseMiniappImportConf(copy); !ok {
		return nil, false
	}
	return copy, true
}
