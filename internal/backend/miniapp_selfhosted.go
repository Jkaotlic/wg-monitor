package backend

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
)

// Свои VPN-серверы в мини-аппе (цикл 3, решения 1, 3, 8): только админ.
// Сервер общий на весь парк, выпуск создаёт на нём клиента -- поэтому ни
// владельцу, ни оператору эти маршруты не открываются. Пароль SSH приходит
// только в теле формы и наружу не отдаётся: в ответе -- password_set.

type miniappSelfHostedInstance struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	Enabled             bool     `json:"enabled"`
	EndpointHost        string   `json:"endpoint_host"`
	EndpointPort        int      `json:"endpoint_port"`
	Container           string   `json:"container"`
	Interface           string   `json:"interface"`
	ConfigPath          string   `json:"config_path"`
	ClientsPath         string   `json:"clients_path"`
	ServerPublicKeyPath string   `json:"server_public_key_path"`
	PresharedKeyPath    string   `json:"preshared_key_path"`
	DNS                 []string `json:"dns"`
	SSHHost             string   `json:"ssh_host"`
	SSHPort             int      `json:"ssh_port"`
	SSHUser             string   `json:"ssh_user"`
	PasswordSet         bool     `json:"password_set"`
}

type miniappSelfHostedDefaults struct {
	Container           string   `json:"container"`
	Interface           string   `json:"interface"`
	ConfigPath          string   `json:"config_path"`
	ClientsPath         string   `json:"clients_path"`
	ServerPublicKeyPath string   `json:"server_public_key_path"`
	PresharedKeyPath    string   `json:"preshared_key_path"`
	DNS                 []string `json:"dns"`
	SSHPort             int      `json:"ssh_port"`
	SSHUser             string   `json:"ssh_user"`
}

type miniappSelfHostedListResp struct {
	Instances []miniappSelfHostedInstance `json:"instances"`
	Defaults  miniappSelfHostedDefaults   `json:"defaults"`
}

func miniappSelfHostedView(inst selfhostedamnezia.Instance) miniappSelfHostedInstance {
	return miniappSelfHostedInstance{
		ID: inst.ID, Label: inst.Label, Enabled: inst.Enabled,
		EndpointHost: inst.EndpointHost, EndpointPort: inst.EndpointPort,
		Container: inst.Container, Interface: inst.Interface,
		ConfigPath: inst.ConfigPath, ClientsPath: inst.ClientsPath,
		ServerPublicKeyPath: inst.ServerPubPath, PresharedKeyPath: inst.PSKPath,
		DNS:     append([]string{}, inst.DNS...),
		SSHHost: inst.SSHHost, SSHPort: inst.SSHPort, SSHUser: inst.SSHUser,
		PasswordSet: inst.SSHPassword != "",
	}
}

// miniappSelfHostedReq -- форма своего сервера. Несёт пароль SSH: печать
// структуры любым способом даёт «[скрыто]».
type miniappSelfHostedReq struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	Enabled             *bool    `json:"enabled"`
	EndpointHost        string   `json:"endpoint_host"`
	EndpointPort        int      `json:"endpoint_port"`
	Container           string   `json:"container"`
	Interface           string   `json:"interface"`
	ConfigPath          string   `json:"config_path"`
	ClientsPath         string   `json:"clients_path"`
	ServerPublicKeyPath string   `json:"server_public_key_path"`
	PresharedKeyPath    string   `json:"preshared_key_path"`
	DNS                 []string `json:"dns"`
	SSHHost             string   `json:"ssh_host"`
	SSHPort             int      `json:"ssh_port"`
	SSHUser             string   `json:"ssh_user"`
	SSHPassword         string   `json:"ssh_password"`
}

func (miniappSelfHostedReq) String() string       { return miniappHiddenValue }
func (miniappSelfHostedReq) GoString() string     { return miniappHiddenValue }
func (miniappSelfHostedReq) LogValue() slog.Value { return slog.StringValue(miniappHiddenValue) }

func (req miniappSelfHostedReq) instance() selfhostedamnezia.Instance {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return selfhostedamnezia.Instance{
		ID: req.ID, Label: req.Label, Enabled: enabled,
		EndpointHost: req.EndpointHost, EndpointPort: req.EndpointPort,
		Container: req.Container, Interface: req.Interface,
		ConfigPath: req.ConfigPath, ClientsPath: req.ClientsPath,
		ServerPubPath: req.ServerPublicKeyPath, PSKPath: req.PresharedKeyPath,
		DNS:     req.DNS,
		SSHHost: req.SSHHost, SSHPort: req.SSHPort, SSHUser: req.SSHUser, SSHPassword: req.SSHPassword,
	}
}

// miniappSelfHostedGate -- только админ (404 остальным, до тела), затем 503,
// если свои серверы на сервере не подключены.
func miniappSelfHostedGate(d Deps, w http.ResponseWriter, r *http.Request) bool {
	tgUser, _ := miniappUserFromContext(r.Context())
	if !miniappIsAdmin(tgUser, d.TelegramAdminUserID) {
		writeMiniappDeployError(w, http.StatusNotFound, "not_found", "Не найдено")
		return false
	}
	if d.SelfHosted == nil {
		writeMiniappCabinetError(w, http.StatusServiceUnavailable, "selfhosted_not_configured")
		return false
	}
	return true
}

func miniappSelfHostedPathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("inst")))
	if !selfhostedamnezia.ValidInstanceID(id) {
		writeMiniappCabinetError(w, http.StatusNotFound, "instance_not_found")
		return "", false
	}
	return id, true
}

func writeMiniappSelfHostedError(d Deps, w http.ResponseWriter, op string, err error) {
	var fe *selfhostedamnezia.FieldError
	switch {
	case errors.As(err, &fe):
		msg := fe.Reason
		if msg == "" {
			msg = miniappCabinetErrorText("invalid_field")
		}
		writeMiniappCabinetJSON(w, http.StatusBadRequest, struct {
			Code    string `json:"code"`
			Error   string `json:"error"`
			Message string `json:"message"`
			Field   string `json:"field"`
		}{Code: "invalid_field", Error: "invalid_field", Message: msg, Field: fe.Field})
	case errors.Is(err, selfhostedamnezia.ErrInstanceNotFound):
		writeMiniappCabinetError(w, http.StatusNotFound, "instance_not_found")
	case errors.Is(err, selfhostedamnezia.ErrInstanceExists):
		writeMiniappCabinetError(w, http.StatusConflict, "instance_exists")
	case errors.Is(err, selfhostedamnezia.ErrInstanceDisabled):
		writeMiniappCabinetError(w, http.StatusConflict, "instance_disabled")
	case errors.Is(err, selfhostedamnezia.ErrInstanceNotReady):
		writeMiniappCabinetError(w, http.StatusConflict, "instance_not_ready")
	default:
		miniappCabinetLogger(d).Error("свой сервер: "+op+" не удалось", "err", err)
		writeMiniappCabinetError(w, http.StatusInternalServerError, errCodeInternal)
	}
}

// miniappSelfHostedIssueError -- то же для выпуска: прочий сбой здесь -- SSH
// или контейнер, это 502, а не ошибка нашего сервера. Текст SSH наружу не идёт.
func miniappSelfHostedIssueError(d Deps, w http.ResponseWriter, err error) {
	if errors.Is(err, selfhostedamnezia.ErrInstanceNotFound) || errors.Is(err, selfhostedamnezia.ErrInstanceDisabled) || errors.Is(err, selfhostedamnezia.ErrInstanceNotReady) {
		writeMiniappSelfHostedError(d, w, "выпуск", err)
		return
	}
	miniappCabinetLogger(d).Warn("свой сервер: выпуск не удался", "err", err)
	writeMiniappCabinetError(w, http.StatusBadGateway, "selfhosted_failed")
}

// miniappSelfHostedClientName -- имя клиента на своём сервере, как у бота.
func miniappSelfHostedClientName(nickname string) string {
	return "wgmon-" + nickname + "-" + time.Now().Format("20060102-150405")
}

func miniappSelfHostedListHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappSelfHostedGate(d, w, r) {
			return
		}
		insts, err := d.SelfHosted.List()
		if err != nil {
			writeMiniappSelfHostedError(d, w, "список", err)
			return
		}
		def := d.SelfHosted.Defaults()
		resp := miniappSelfHostedListResp{
			Instances: make([]miniappSelfHostedInstance, 0, len(insts)),
			Defaults: miniappSelfHostedDefaults{
				Container: def.Container, Interface: def.Interface,
				ConfigPath: def.ConfigPath, ClientsPath: def.ClientsPath,
				ServerPublicKeyPath: def.ServerPubPath, PresharedKeyPath: def.PSKPath,
				DNS: append([]string{}, def.DNS...), SSHPort: def.SSHPort, SSHUser: def.SSHUser,
			},
		}
		for _, inst := range insts {
			resp.Instances = append(resp.Instances, miniappSelfHostedView(inst))
		}
		writeMiniappCabinetJSON(w, http.StatusOK, resp)
	}
}

func miniappSelfHostedCreateHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappSelfHostedGate(d, w, r) {
			return
		}
		var req miniappSelfHostedReq
		if !decodeMiniappCabinetBody(w, r, &req) {
			return
		}
		inst := req.instance()
		if err := d.SelfHosted.Create(inst); err != nil {
			writeMiniappSelfHostedError(d, w, "добавление", err)
			return
		}
		id := strings.ToLower(strings.TrimSpace(inst.ID))
		miniappCabinetLogger(d).Info("свой сервер добавлен", "instance", id, "ssh", inst.SSHHost != "")
		writeMiniappCabinetJSON(w, http.StatusCreated, struct {
			ID string `json:"id"`
		}{ID: id})
	}
}

func miniappSelfHostedUpdateHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappSelfHostedGate(d, w, r) {
			return
		}
		id, ok := miniappSelfHostedPathID(w, r)
		if !ok {
			return
		}
		var req miniappSelfHostedReq
		if !decodeMiniappCabinetBody(w, r, &req) {
			return
		}
		if err := d.SelfHosted.Update(id, req.instance()); err != nil {
			writeMiniappSelfHostedError(d, w, "изменение", err)
			return
		}
		miniappCabinetLogger(d).Info("свой сервер изменён", "instance", id, "password_changed", req.SSHPassword != "")
		w.WriteHeader(http.StatusNoContent)
	}
}

func miniappSelfHostedToggleHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappSelfHostedGate(d, w, r) {
			return
		}
		id, ok := miniappSelfHostedPathID(w, r)
		if !ok {
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if !decodeMiniappCabinetBody(w, r, &body) {
			return
		}
		if err := d.SelfHosted.SetEnabled(id, body.Enabled); err != nil {
			writeMiniappSelfHostedError(d, w, "включение", err)
			return
		}
		miniappCabinetLogger(d).Info("свой сервер: включённость", "instance", id, "enabled", body.Enabled)
		w.WriteHeader(http.StatusNoContent)
	}
}

func miniappSelfHostedDeleteHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappSelfHostedGate(d, w, r) {
			return
		}
		id, ok := miniappSelfHostedPathID(w, r)
		if !ok {
			return
		}
		var body struct {
			Confirm string `json:"confirm"`
		}
		if !decodeMiniappCabinetBody(w, r, &body) {
			return
		}
		insts, err := d.SelfHosted.List()
		if err != nil {
			writeMiniappSelfHostedError(d, w, "удаление", err)
			return
		}
		label := ""
		for _, inst := range insts {
			if inst.ID == id {
				label = inst.Label
				if label == "" {
					label = inst.ID
				}
			}
		}
		if label == "" {
			writeMiniappCabinetError(w, http.StatusNotFound, "instance_not_found")
			return
		}
		if !confirmPhraseMatches(body.Confirm, label) {
			writeMiniappCabinetError(w, http.StatusBadRequest, "confirm_mismatch")
			return
		}
		if err := d.SelfHosted.Delete(id); err != nil {
			writeMiniappSelfHostedError(d, w, "удаление", err)
			return
		}
		miniappCabinetLogger(d).Info("свой сервер удалён", "instance", id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func miniappSelfHostedCheckHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappSelfHostedGate(d, w, r) {
			return
		}
		id, ok := miniappSelfHostedPathID(w, r)
		if !ok {
			return
		}
		res, err := d.SelfHosted.Check(r.Context(), id)
		if err != nil {
			writeMiniappSelfHostedError(d, w, "проверка", err)
			return
		}
		miniappCabinetLogger(d).Info("свой сервер: проверка подключения", "instance", id, "ok", res.OK)
		writeMiniappCabinetJSON(w, http.StatusOK, struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}{OK: res.OK, Message: res.Message})
	}
}
