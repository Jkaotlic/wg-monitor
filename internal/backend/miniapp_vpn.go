package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// VPNCabinet — кабинет провайдера (Amnezia Premium, HideMy.name), из которого
// выпускается конфиг туннеля. Реализуется callbacks.Router: ключи кабинетов
// и клиенты к их API живут там, и второй копии им заводиться незачем.
//
// Главное свойство контракта: СОДЕРЖИМОЕ КОНФИГА НАРУЖУ НЕ ХОДИТ. Клиент
// присылает только выбор (провайдер и страна), сервер сам скачивает конфиг и
// сам кладёт его в команду агенту. Поэтому tunnel_import и остаётся вне
// allowlist мини-аппа: единственный путь конфига на роутер -- этот, а не
// «клиент прислал файл».
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
}

type miniappVPNIssueResp struct {
	CmdID      string `json:"cmd_id"`
	TunnelName string `json:"tunnel_name"`
}

// miniappVPNIssueHandler выпускает конфиг и кладёт его в команду агенту.
//
// Только владельцу: выпуск занимает устройство в платной подписке и заводит
// на роутере новый туннель. Оператору дали смотреть и чинить, а не тратить
// чужую подписку.
func miniappVPNIssueHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if !miniappIsOwner(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusForbidden, "owner_only",
				"issuing a config spends a device slot of the subscription and is available to the router's owner only")
			return
		}
		if d.VPNCabinet == nil || d.CommandSink == nil {
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
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "cabinet_failed", err.Error())
			return
		}
		if len(issued.Conf) == 0 {
			writeJSONError(w, http.StatusBadGateway, "cabinet_failed", "cabinet returned an empty config")
			return
		}
		backend := issued.Backend
		if backend == "" {
			backend = "nativewg"
		}
		cmdID, err := newCmdID()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "id gen: "+err.Error())
			return
		}
		// replace: true повторяет поведение бота -- выпуск той же страны
		// заменяет прежний туннель с тем же именем, а не плодит второй.
		cmd := wire.Command{
			ID:     cmdID,
			Action: "tunnel_import",
			Args: map[string]any{
				"conf":    base64.StdEncoding.EncodeToString(issued.Conf),
				"name":    issued.TunnelName,
				"replace": true,
				"backend": backend,
			},
			IssuedAt: time.Now().UTC(),
		}
		if err := d.CommandSink.Enqueue(u.ID, cmd); err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "enqueue: "+err.Error())
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
