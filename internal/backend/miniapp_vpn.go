package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// VPNCabinet — кабинет провайдера (Amnezia Premium, HideMy.name), из которого
// выпускается конфиг туннеля. Реализуется callbacks.Router: ключи кабинетов
// и клиенты к их API живут там, и второй копии им заводиться незачем.
//
// Главное свойство контракта: СОДЕРЖИМОЕ КОНФИГА НАРУЖУ НЕ ХОДИТ. Клиент
// присылает только выбор (провайдер и страна), сервер сам скачивает конфиг и
// сам кладёт его в команду агенту. Поэтому tunnel_import и остаётся вне
// allowlist мини-аппа: конфиг попадает на роутер только сервером -- выпуском
// из кабинета (здесь) или файлом, который админ или владелец загрузил сам,
// после предпросмотра и только новым VPN-туннелем (miniapp_tunnel_import.go).
type VPNCabinet interface {
	Account(ctx context.Context, routerID int64, provider string) (VPNAccount, error)
	IssueConfig(ctx context.Context, routerID int64, provider, optionID string) (VPNIssuedConfig, error)
}

// VPNAccount — то, что видно про кабинет: подписка, устройства и список того,
// что можно выпустить. Ключа кабинета здесь нет и быть не может.
type VPNAccount struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	// Connected — сохранён ли ключ этого кабинета для этого роутера. False —
	// это не ошибка, а состояние: кабинет просто не подключён.
	Connected   bool        `json:"connected"`
	Status      string      `json:"status,omitempty"`
	EndsAt      string      `json:"ends_at,omitempty"`
	DevicesUsed int         `json:"devices_used,omitempty"`
	DevicesMax  int         `json:"devices_max,omitempty"`
	Options     []VPNOption `json:"options,omitempty"`
	// Note — почему список пуст, если он пуст. Пустой список без объяснения
	// читается как поломка приложения.
	Note string `json:"note,omitempty"`
}

type VPNOption struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Issued bool   `json:"issued,omitempty"`
}

// VPNIssuedConfig — результат выпуска. Conf уходит ТОЛЬКО в команду агенту.
type VPNIssuedConfig struct {
	TunnelName string
	Conf       []byte
	Backend    string
}

type miniappVPNResp struct {
	Accounts []VPNAccount `json:"accounts"`
}

// miniappVPNProviders — кабинеты, которые приложение умеет показывать. Список
// закреплён здесь, а не спрашивается у реализации: клиент не должен уметь
// назвать провайдера, о котором сервер не знает.
var miniappVPNProviders = []string{"amnezia", "hidemyname"}

func miniappVPNAccountsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if d.VPNCabinet == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "vpn cabinets are not configured")
			return
		}
		resp := miniappVPNResp{Accounts: []VPNAccount{}}
		for _, provider := range miniappVPNProviders {
			acc, err := d.VPNCabinet.Account(r.Context(), routerID, provider)
			if err != nil {
				// Один недоступный кабинет не отменяет второй: экран покажет
				// то, что удалось, и скажет про то, что нет.
				resp.Accounts = append(resp.Accounts, VPNAccount{Provider: provider, Note: err.Error()})
				continue
			}
			resp.Accounts = append(resp.Accounts, acc)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

type miniappVPNIssueReq struct {
	Provider string `json:"provider"`
	OptionID string `json:"option_id"`
	// InstanceID -- свой сервер при provider "selfhosted" (только админ).
	InstanceID string `json:"instance_id"`
}

type miniappVPNIssueResp struct {
	CmdID      string `json:"cmd_id"`
	TunnelName string `json:"tunnel_name"`
}

// miniappVPNIssueHandler выпускает конфиг и кладёт его в команду агенту.
//
// Доступно всем, у кого есть доступ к роутеру, — владельцу и оператору. Это
// решение спеки рабочего места (2026-08-02, §4.2), и причина в ней названа:
// оператора добавляют именно для того, чтобы он чинил, а дробить его права
// дальше значит объяснять человеку, почему он видит кнопку, которая ему
// запрещена. Замена конфига — как раз починка: старый туннель остаётся на
// месте, новый обратим выключением.
//
// Установка прошивки — другое дело: она меняет само устройство и необратима,
// поэтому её держит не роль, а набор имени роутера (miniappConfirmRequired) —
// с цикла 1 её тоже ставят и владелец, и оператор (решение оператора 14.09).
//
// Исключение -- свой сервер (provider "selfhosted", цикл 3): он общий на весь
// парк, и каждый выпуск создаёт на нём клиента, поэтому только админ
// (miniappVPNIssueSelfHosted).
func miniappVPNIssueHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if d.CommandSink == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "vpn cabinets are not configured")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req miniappVPNIssueReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		provider := strings.ToLower(strings.TrimSpace(req.Provider))
		// Свой сервер -- другая роль (только админ), а провайдер виден лишь
		// из тела; поэтому сужение прав -- здесь, сразу после разбора.
		if provider == "selfhosted" {
			miniappVPNIssueSelfHosted(d, w, r, telegramUserID, routerID, req)
			return
		}
		if d.VPNCabinet == nil {
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "vpn cabinets are not configured")
			return
		}
		known := false
		for _, p := range miniappVPNProviders {
			if p == provider {
				known = true
				break
			}
		}
		if !known {
			writeJSONError(w, http.StatusBadRequest, "unknown_provider", "provider is not one this app knows")
			return
		}
		optionID := strings.TrimSpace(req.OptionID)
		if optionID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_option", "option_id is required")
			return
		}
		u, err := d.DB.Users().GetByID(routerID)
		if errors.Is(err, db.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "router lookup failed")
			return
		}
		issued, err := d.VPNCabinet.IssueConfig(r.Context(), routerID, provider, optionID)
		if errors.Is(err, ErrVPNSlotBusy) {
			writeMiniappCabinetError(w, http.StatusConflict, "slot_busy")
			return
		}
		if err != nil {
			// Текст ошибки кабинета -- только в журнал (реализация кабинета
			// уже убрала из него ключ и код); человеку -- слова из таблицы.
			miniappCabinetLogger(d).Warn("выпуск: кабинет не выдал конфиг", "router_id", routerID, "provider", provider, "option", optionID, "err", err)
			writeMiniappCabinetError(w, http.StatusBadGateway, "cabinet_failed")
			return
		}
		if len(issued.Conf) == 0 {
			miniappCabinetLogger(d).Warn("выпуск: кабинет вернул пустой конфиг", "router_id", routerID, "provider", provider, "option", optionID)
			writeMiniappCabinetError(w, http.StatusBadGateway, "cabinet_failed")
			return
		}
		cmdID, err := miniappEnqueueTunnelImport(d, u.ID, issued.Conf, issued.TunnelName, issued.Backend)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp vpn config issued",
				"nickname", u.Nickname, "user_id", u.ID, "provider", provider, "option", optionID, "cmd_id", cmdID)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(miniappVPNIssueResp{CmdID: cmdID, TunnelName: issued.TunnelName})
	}
}

// miniappVPNIssueSelfHosted -- выпуск на свой сервер (цикл 3, решение 8).
// Сервер общий, каждый выпуск -- новый клиент на нём: только админ.
//
// Происхождение туннеля здесь не пишется намеренно: движок починки
// перевыпускает линию по происхождению, а перевыпуск на своём сервере --
// ещё один клиент. Своего сервера нет и в miniappVPNProviders, поэтому мастер
// замены его тоже не выпускает (TestSelfHostedIsNotAReplaceOrRepairProvider).
func miniappVPNIssueSelfHosted(d Deps, w http.ResponseWriter, r *http.Request, tgUser, routerID int64, req miniappVPNIssueReq) {
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
	u, err := d.DB.Users().GetByID(routerID)
	if errors.Is(err, db.ErrUserNotFound) || (err == nil && u == nil) {
		writeMiniappCabinetError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeMiniappCabinetError(w, http.StatusInternalServerError, errCodeInternal)
		return
	}
	issued, inst, err := d.SelfHosted.Issue(r.Context(), instID, miniappSelfHostedClientName(u.Nickname))
	if err != nil {
		miniappSelfHostedIssueError(d, w, err)
		return
	}
	tunnelName := selfhostedamnezia.TunnelName(inst.ID, u.Nickname)
	cmdID, err := miniappEnqueueTunnelImport(d, u.ID, issued.Config, tunnelName, "nativewg")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
		return
	}
	miniappCabinetLogger(d).Info("miniapp vpn config issued",
		"nickname", u.Nickname, "user_id", u.ID, "provider", "selfhosted", "instance", inst.ID, "address", issued.Address, "cmd_id", cmdID)
	writeMiniappCabinetJSON(w, http.StatusAccepted, miniappVPNIssueResp{CmdID: cmdID, TunnelName: tunnelName})
}

// miniappEnqueueTunnelImport кладёт конфиг в команду агенту. replace:true
// повторяет поведение бота: выпуск того же имени заменяет прежний туннель, а
// не плодит второй.
func miniappEnqueueTunnelImport(d Deps, routerID int64, conf []byte, tunnelName, importBackend string) (string, error) {
	if importBackend == "" {
		importBackend = "nativewg"
	}
	cmdID, err := newCmdID()
	if err != nil {
		return "", fmt.Errorf("id gen: %w", err)
	}
	cmd := wire.Command{
		ID:     cmdID,
		Action: "tunnel_import",
		Args: map[string]any{
			"conf":    base64.StdEncoding.EncodeToString(conf),
			"name":    tunnelName,
			"replace": true,
			"backend": importBackend,
		},
		IssuedAt: time.Now().UTC(),
	}
	if err := d.CommandSink.Enqueue(routerID, cmd); err != nil {
		return "", fmt.Errorf("enqueue: %w", err)
	}
	return cmdID, nil
}
