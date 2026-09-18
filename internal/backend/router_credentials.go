package backend

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// Сохранённый пароль root (v0.45, задача B, п. 1). Решение оператора 18.09:
// «пароль root хранится зашифрованным и используется для авто-оживления».
// Пишется, когда админ вводит его: добавление роутера, переустановка,
// оживление. Наружу не отдаётся никогда: в парке -- только признак
// root_password_saved и «Забыть пароль» (TestStoredRouterCredentialsNeverLeak).

// CredentialSaver -- сохранение пароля для авто-оживления. *revive.Service
// подходит; без ключа оживления его нет, и хранить нечем.
type CredentialSaver interface {
	SaveCredentials(routerID int64, c revive.StoredCredentials) (bool, error)
}

var _ CredentialSaver = (*revive.Service)(nil)

func routerCredentialSaver(d Deps) CredentialSaver {
	svc := miniappRevive(d)
	if svc == nil || !svc.Enabled() {
		return nil
	}
	saver, _ := svc.(CredentialSaver)
	return saver
}

// rememberRouterCredentials -- после принятого запроса админа. Отказ
// сохранения запрос не роняет: установка уже идёт, а пароль можно ввести
// снова. В журнал -- только факт и виды входа, без значений.
func rememberRouterCredentials(d Deps, routerID int64, c revive.StoredCredentials, source string, by int64) {
	if strings.TrimSpace(c.RootPassword) == "" {
		return
	}
	saver := routerCredentialSaver(d)
	if saver == nil {
		return
	}
	saved, err := saver.SaveCredentials(routerID, c)
	if d.Logger == nil {
		return
	}
	if err != nil {
		d.Logger.Warn("router credentials not saved", "router_id", routerID, "source", source, "code", reviveErrorCode(err), "by", by)
		return
	}
	if saved {
		d.Logger.Info("router credentials saved", "router_id", routerID, "source", source,
			"credentials", miniappCredentialKinds(c.RootPassword, c.AWGMLogin, c.AWGMPassword, c.AWGMAPIKey), "by", by)
	}
}

type miniappForgetCredentialsResp struct {
	Cleared bool `json:"cleared"`
	// ReviveCancelled -- снято ждущее авто-оживление: оно поставлено из
	// этого же пароля и держало его копию.
	ReviveCancelled bool `json:"revive_cancelled"`
}

// miniappForgetCredentialsHandler -- DELETE /v1/miniapp/routers/{id}/credentials.
// Только админ (остальным 404). Ключ оживления не нужен: удаление не
// расшифровывает ничего.
func miniappForgetCredentialsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID, ok := miniappAdminOrNotFound(d, w, r)
		if !ok {
			return
		}
		u, ok := miniappLoadRouterForAdmin(d, w, r)
		if !ok {
			return
		}
		cleared, err := d.DB.RouterCredentials().Delete(u.ID)
		if err != nil {
			if d.Logger != nil {
				d.Logger.Warn("router credentials forget failed", "router_id", u.ID, "by", adminID)
			}
			writeMiniappDeployError(w, http.StatusInternalServerError, errCodeInternal, "Не удалось стереть пароль.")
			return
		}
		resp := miniappForgetCredentialsResp{Cleared: cleared}
		if svc := miniappRevive(d); svc != nil && svc.Enabled() {
			// Ждущее авто-оживление несёт копию стёртого пароля -- снимаем.
			// Ручное (админ ввёл пароль сам) -- его, у него своя отмена;
			// идущее отменить нельзя вовсе (Service.Cancel).
			if view, err := svc.StatusFor(u.ID); err == nil && view != nil && view.Auto && view.Status == revive.StatusWaiting {
				if ok, err := svc.Cancel(r.Context(), u.ID); err == nil {
					resp.ReviveCancelled = ok
				}
			}
		}
		if d.Logger != nil {
			d.Logger.Info("router credentials forgotten", "router_id", u.ID, "nickname", u.Nickname,
				"cleared", resp.Cleared, "revive_cancelled", resp.ReviveCancelled, "by", adminID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
