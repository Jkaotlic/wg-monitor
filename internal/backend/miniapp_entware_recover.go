package backend

import (
	"encoding/json"
	"github.com/Jkaotlic/wg-monitor/internal/awgmrelay"
	"net/http"
)

func miniappEntwareRecoverHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := miniappAdminOrNotFound(d, w, r); !ok {
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		var in struct {
			Confirm bool `json:"confirm"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || !in.Confirm {
			writeMiniappDeployError(w, 400, "confirmation_required", "Подтвердите восстановление выбранного роутера.")
			return
		}
		if d.EntwareRecovery == nil {
			writeMiniappDeployError(w, 503, "recovery_not_configured", "Восстановление не настроено.")
			return
		}
		id, err := d.EntwareRecovery.Start(u.ID)
		if err != nil {
			writeMiniappDeployError(w, 409, "recovery_unavailable", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"job_id": id})
	}
}

func miniappEntwareScriptHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := miniappAdminOrNotFound(d, w, r); !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"filename": "entware-recover.run", "content": string(awgmrelay.EntwareRecoveryScript), "instructions": "В awg-manager откройте Expert → Файлы, загрузите файл в /tmp, разрешите выполнение (0700) и запустите. Через SSH: /opt/bin/busybox sh /tmp/entware-recover.run. Не запускайте во время установки пакетов. Проверьте итог RECOVERY_CONFIRMED; hold или часть ссылок могли сохраниться даже при ошибке."})
	}
}
