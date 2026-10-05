package backend

import (
	"errors"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
)

// Отзыв выданного подключения на своём сервере (v0.55, B3): кнопка админа в
// карточке сервера, без автоматики. Список -- записи таблицы клиентов сервера;
// подключение, которым сейчас живёт VPN-туннель роутера, помечено, чтобы лист
// отзыва назвал роутер и туннель до подтверждения. Панель awg3 отзыва не
// умеет (в её API нет удаления пира) -- здесь только свой сервер.

type miniappSelfHostedInUse struct {
	Router string `json:"router"`
	Tunnel string `json:"tunnel"`
}

type miniappSelfHostedClient struct {
	// ID -- открытый ключ подключения: по нему отзыв (в теле, не в пути).
	ID        string                  `json:"id"`
	Name      string                  `json:"name"`
	Address   string                  `json:"address"`
	CreatedAt string                  `json:"created_at,omitempty"`
	InUse     *miniappSelfHostedInUse `json:"in_use"`
}

// miniappSelfHostedLiveTunnels -- для каждого подключения: какой роутер живёт
// им прямо сейчас. Подключение выдано роутеру, если его имя «wgmon-<ник>-…»
// (ClientNameBelongsTo), а у роутера есть VPN-туннель с именем
// TunnelName(сервер, ник); из подключений роутера живёт последнее по времени --
// каждая выдача заменяет туннель того же имени. Происхождения конфига для
// своих серверов система не пишет, поэтому по имени, а не по таблице
// происхождения. Роутеры без подходящих подключений не опрашиваются.
func miniappSelfHostedLiveTunnels(d Deps, instID string, clients []selfhostedamnezia.Client) map[string]miniappSelfHostedInUse {
	out := map[string]miniappSelfHostedInUse{}
	users, err := d.DB.Users().GetAll()
	if err != nil {
		return out
	}
	for _, u := range users {
		newest := -1
		for i, c := range clients {
			if !selfhostedamnezia.ClientNameBelongsTo(c.Name, u.Nickname) {
				continue
			}
			if newest < 0 || !c.CreatedAt.Before(clients[newest].CreatedAt) {
				newest = i
			}
		}
		if newest < 0 {
			continue
		}
		want := selfhostedamnezia.TunnelName(instID, u.Nickname)
		rows, err := d.DB.Events().LatestEventsByPrefixSince(u.ID, "", time.Now().UTC().Add(-miniappEventsWindow))
		if err != nil {
			continue
		}
		for _, row := range miniappCurrentRows(d, u.ID, rows) {
			if tu, ok := miniappTunnelFromEvent(row); ok && tu.Name == want {
				out[clients[newest].PublicKey] = miniappSelfHostedInUse{Router: u.Nickname, Tunnel: want}
				break
			}
		}
	}
	return out
}

func miniappSelfHostedClientsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappSelfHostedGate(d, w, r) {
			return
		}
		id, ok := miniappSelfHostedPathID(w, r)
		if !ok {
			return
		}
		clients, inst, err := d.SelfHosted.Clients(r.Context(), id)
		if err != nil {
			miniappSelfHostedClientsError(d, w, "список подключений", "selfhosted_clients_failed", err)
			return
		}
		inUse := miniappSelfHostedLiveTunnels(d, inst.ID, clients)
		resp := struct {
			Clients []miniappSelfHostedClient `json:"clients"`
		}{Clients: make([]miniappSelfHostedClient, 0, len(clients))}
		for _, c := range clients {
			v := miniappSelfHostedClient{ID: c.PublicKey, Name: c.Name, Address: c.Address}
			if !c.CreatedAt.IsZero() {
				v.CreatedAt = c.CreatedAt.Format(time.RFC3339)
			}
			if u, ok := inUse[c.PublicKey]; ok {
				u := u
				v.InUse = &u
			}
			resp.Clients = append(resp.Clients, v)
		}
		writeMiniappCabinetJSON(w, http.StatusOK, resp)
	}
}

func miniappSelfHostedRevokeHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappSelfHostedGate(d, w, r) {
			return
		}
		id, ok := miniappSelfHostedPathID(w, r)
		if !ok {
			return
		}
		var body struct {
			ClientID string `json:"client_id"`
			Confirm  string `json:"confirm"`
		}
		if !decodeMiniappCabinetBody(w, r, &body) {
			return
		}
		// Имя сервера сверяется здесь, а не только на экране.
		if !miniappSelfHostedConfirm(d, w, id, body.Confirm, "отзыв подключения") {
			return
		}
		c, _, err := d.SelfHosted.Revoke(r.Context(), id, body.ClientID)
		if err != nil {
			miniappSelfHostedClientsError(d, w, "отзыв подключения", "selfhosted_revoke_failed", err)
			return
		}
		// Ключ подключения в журнал не пишется: только имя и адрес.
		miniappCabinetLogger(d).Info("свой сервер: подключение отозвано", "instance", id, "client", c.Name, "address", c.Address)
		w.WriteHeader(http.StatusNoContent)
	}
}

// miniappSelfHostedClientsError -- ошибки списка и отзыва: известные -- словами,
// прочее (SSH, контейнер) -- 502 с кодом failCode, текст сбоя наружу не идёт.
func miniappSelfHostedClientsError(d Deps, w http.ResponseWriter, op, failCode string, err error) {
	var hk *selfhostedamnezia.HostKeyChangedError
	switch {
	case errors.As(err, &hk):
		miniappCabinetLogger(d).Warn("свой сервер: ключ хоста сменился, "+op+" отказан", "instance_label", hk.Label)
		writeMiniappDeployError(w, http.StatusConflict, "selfhosted_host_key_changed", hk.Error())
	case errors.Is(err, selfhostedamnezia.ErrClientNotFound):
		writeMiniappCabinetError(w, http.StatusNotFound, "client_not_found")
	case errors.Is(err, selfhostedamnezia.ErrInstanceNotFound), errors.Is(err, selfhostedamnezia.ErrInstanceDisabled), errors.Is(err, selfhostedamnezia.ErrInstanceNotReady):
		writeMiniappSelfHostedError(d, w, op, err)
	default:
		miniappCabinetLogger(d).Warn("свой сервер: "+op+" не удался", "err", err)
		writeMiniappCabinetError(w, http.StatusBadGateway, failCode)
	}
}
