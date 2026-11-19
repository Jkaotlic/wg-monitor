package backend

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/premiumfiles"
)

// PremiumFiles is an owner-triggered file download. Router provisioning and
// background repair do not call this interface.
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

func miniappPremiumFilesHandler(d Deps, download bool) http.HandlerFunc {
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
		if !download {
			account, keyID, err := files.OpenPremiumFiles(ctx, u.ID, body.VPNKey, body.KeyID)
			if err != nil {
				premiumFileFailure(w, err, download)
				return
			}
			writeMiniappCabinetJSON(w, http.StatusOK, map[string]any{"account": account, "key_id": keyID})
			return
		}
		if !body.Confirm || body.KeyID == "" || len(body.Country) != 2 || (body.DeclaredCountry != "ru" && body.DeclaredCountry != "ag") {
			writeMiniappCabinetError(w, http.StatusBadRequest, "invalid_field")
			return
		}
		conf, err := files.DownloadPremiumFile(ctx, u.ID, body.KeyID, body.Country, body.DeclaredCountry)
		if err != nil {
			premiumFileFailure(w, err, download)
			return
		}
		writeMiniappCabinetJSON(w, http.StatusOK, map[string]string{"filename": "amnezia_" + body.Country + ".conf", "conf": string(conf)})
	}
}
