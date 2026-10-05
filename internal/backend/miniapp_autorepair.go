package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Автопочинка VPN-туннеля со стороны мини-аппа (v0.54): настройка на
// туннель, подсказка источника и метки для вкладки «VPN-туннели».
//
// Права -- те же, что у прочих действий, меняющих роутер: владелец,
// операторы и админ (miniappRouterAllowed). Источник «свой сервер» -- только
// тем, кому разрешена выдача с этой панели (miniappCanIssueAwg3).

type miniappAutorepairResp struct {
	Enabled       bool                   `json:"enabled"`
	Provider      string                 `json:"provider"`
	Option        string                 `json:"option"`
	AllowRelocate bool                   `json:"allow_relocate"`
	Suggested     *miniappAutorepairPick `json:"suggested,omitempty"`
	Sources       []miniappAutorepairSrc `json:"sources"`
	// Blocked -- почему автопочинка включена, но стоит (лимит попыток,
	// провал до человека). Пусто -- не стоит.
	Blocked string `json:"blocked,omitempty"`
	// HasBackup -- есть ли у VPN-туннеля запасной в общем наборе правил.
	// Снимок наборов правил живёт на роутере и бэкендом не хранится, а
	// спрашивать роутер из GET нельзя -- поэтому пока всегда nil: «не знаем».
	HasBackup *bool `json:"has_backup,omitempty"`
	CanEdit   bool  `json:"can_edit"`
}

type miniappAutorepairPick struct {
	Provider string `json:"provider"`
	Option   string `json:"option"`
	Why      string `json:"why"`
}

type miniappAutorepairOpt struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type miniappAutorepairSrc struct {
	Provider string                 `json:"provider"`
	Label    string                 `json:"label"`
	Options  []miniappAutorepairOpt `json:"options"`
	OK       bool                   `json:"ok"`
	Note     string                 `json:"note,omitempty"`
}

type miniappAutorepairReq struct {
	Enabled       bool   `json:"enabled"`
	Provider      string `json:"provider"`
	Option        string `json:"option"`
	AllowRelocate bool   `json:"allow_relocate"`
}

// Состояния меток вкладки «VPN-туннели». Выключенных в ответе нет.
const (
	autorepairStateOn      = "on"
	autorepairStateLimited = "limited"
	autorepairStateBlocked = "blocked"
)

// miniappAutorepairCabinets -- кабинеты-источники и их имена на случай, если
// кабинет не ответил и подписи от него нет.
var miniappAutorepairCabinets = []struct{ provider, label string }{
	{RepairProviderAmnezia, "Amnezia Premium"},
	{RepairProviderHideMy, "HideMy.name"},
}

// miniappAutorepairTunnelID -- id VPN-туннеля из пути. awg-manager называет
// туннели короткими латинскими id («awg12»); всё прочее -- не туннель, и
// писать под таким ключом настройку незачем.
func miniappAutorepairTunnelID(r *http.Request) (string, bool) {
	tid := strings.TrimSpace(r.PathValue("tunnel_id"))
	if tid == "" || len(tid) > 64 {
		return "", false
	}
	for _, c := range tid {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
		if !ok {
			return "", false
		}
	}
	return tid, true
}

// miniappAutorepairRouter -- общий вход трёх маршрутов: роутер виден этому
// человеку, иначе 404 (чужой роутер не виден даже для отказа).
func miniappAutorepairRouter(d Deps, w http.ResponseWriter, r *http.Request) (int64, int64, *db.User, bool) {
	tg, _ := miniappUserFromContext(r.Context())
	routerID, ok := parseMiniappRouterID(r)
	if !ok || !miniappRouterAllowed(d, tg, routerID) {
		writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
		return 0, 0, nil, false
	}
	u, err := d.DB.Users().GetByID(routerID)
	if errors.Is(err, db.ErrUserNotFound) || (err == nil && u == nil) {
		writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
		return 0, 0, nil, false
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "router lookup failed")
		return 0, 0, nil, false
	}
	return tg, routerID, u, true
}

// miniappAutorepairBlocked -- причина стопа автопочинки; пусто -- не стоит
// или движка нет.
func miniappAutorepairBlocked(d Deps, nickname, tunnelID string) string {
	if d.LinkRepair == nil || d.LinkRepair.Attempts.KV == nil {
		return ""
	}
	if blocked, why := d.LinkRepair.Attempts.Blocked(nickname, miniappTunnelPrefix+tunnelID); blocked {
		return why
	}
	return ""
}

// miniappAutorepairAwg3 -- панели своих серверов, с которых этот человек
// может выпускать на этот роутер, тем же отбором, что экран выпуска.
func miniappAutorepairAwg3(ctx context.Context, d Deps, tg, routerID int64) []awg3panel.IssuablePanel {
	if d.Awg3Panels == nil {
		return nil
	}
	list, err := d.Awg3Panels.IssuablePanels(ctx, tg, miniappIsAdmin(tg, d.TelegramAdminUserID))
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("autorepair: панели своих серверов не прочитались", "err", err)
		}
		return nil
	}
	out := make([]awg3panel.IssuablePanel, 0, len(list))
	for _, p := range list {
		if miniappCanIssueAwg3(d, tg, routerID, p.ID) {
			out = append(out, p)
		}
	}
	return out
}

// miniappAutorepairSources -- из чего можно выпускать конфиг для этого
// VPN-туннеля: кабинеты роутера и разрешённые панели своих серверов.
func miniappAutorepairSources(ctx context.Context, d Deps, tg, routerID int64, panels []awg3panel.IssuablePanel) []miniappAutorepairSrc {
	out := []miniappAutorepairSrc{}
	for _, c := range miniappAutorepairCabinets {
		src := miniappAutorepairSrc{Provider: c.provider, Label: c.label, Options: []miniappAutorepairOpt{}}
		switch {
		case d.VPNCabinet == nil:
			src.Note = "кабинеты провайдеров не подключены к бэкенду"
		default:
			acc, err := d.VPNCabinet.Account(ctx, routerID, c.provider)
			switch {
			case err != nil:
				if d.Logger != nil {
					d.Logger.Warn("autorepair: кабинет не ответил", "provider", c.provider, "err", err)
				}
				src.Note = "кабинет не ответил — попробуйте позже"
			case !acc.Connected:
				src.Note = strings.TrimSpace(acc.Note)
				if src.Note == "" {
					src.Note = "кабинет не подключён"
				}
			default:
				src.OK = true
				if l := strings.TrimSpace(acc.Label); l != "" {
					src.Label = l
				}
				for _, o := range acc.Options {
					if id := strings.TrimSpace(o.ID); id != "" {
						src.Options = append(src.Options, miniappAutorepairOpt{ID: id, Label: o.Label})
					}
				}
			}
		}
		out = append(out, src)
	}
	// Свой сервер -- только если хоть одна панель разрешена: показывать
	// источник, которым человек воспользоваться не может, -- обещать лишнее.
	if len(panels) > 0 {
		src := miniappAutorepairSrc{Provider: RepairProviderAwg3, Label: "Свой сервер", Options: []miniappAutorepairOpt{}}
		var down []string
		for _, p := range panels {
			if p.Unavailable {
				down = append(down, "«"+p.Label+"»")
				continue
			}
			for _, i := range p.Ifaces {
				title := strings.TrimSpace(i.Title)
				if title == "" {
					title = i.ID
				}
				src.Options = append(src.Options, miniappAutorepairOpt{
					ID:    p.ID + "/" + i.ID,
					Label: "«" + p.Label + "» · «" + title + "»",
				})
			}
		}
		src.OK = len(src.Options) > 0
		if len(down) > 0 {
			src.Note = "не отвечает: " + strings.Join(down, ", ")
		}
		out = append(out, src)
	}
	return out
}

// miniappAutorepairSuggest -- откуда, скорее всего, выпущен этот VPN-туннель.
// Порядок -- спека §2: происхождение, имя своего сервера, имя кабинета.
// Подсказка режим не включает: подтверждает человек.
func miniappAutorepairSuggest(d Deps, routerID int64, tunnelID string, panels []awg3panel.IssuablePanel, sources []miniappAutorepairSrc) *miniappAutorepairPick {
	if o, ok, err := d.DB.TunnelOrigins().Get(routerID, tunnelID); err == nil && ok {
		switch o.Provider {
		case RepairProviderAmnezia, RepairProviderHideMy, RepairProviderAwg3:
			if strings.TrimSpace(o.Variant) != "" {
				return &miniappAutorepairPick{Provider: o.Provider, Option: o.Variant, Why: "так он был выпущен"}
			}
		}
	}
	name := miniappTunnelNameForCheck(d, routerID, miniappTunnelPrefix+tunnelID)
	if name == "" {
		return nil
	}
	for _, p := range panels {
		for _, i := range p.Ifaces {
			if awg3panel.TunnelName(p.ID, i.ID) == name {
				return &miniappAutorepairPick{Provider: RepairProviderAwg3, Option: p.ID + "/" + i.ID,
					Why: "так называются VPN-туннели со своего сервера «" + p.Label + "»"}
			}
		}
	}
	connected := func(provider string) bool {
		for _, s := range sources {
			if s.Provider == provider {
				return s.OK
			}
		}
		return false
	}
	for _, c := range []struct{ prefix, provider string }{
		{"amnezia_", RepairProviderAmnezia},
		{"hidemy_", RepairProviderHideMy},
	} {
		if rest, ok := strings.CutPrefix(name, c.prefix); ok && rest != "" && connected(c.provider) {
			return &miniappAutorepairPick{Provider: c.provider, Option: rest,
				Why: "так называются VPN-туннели из кабинета"}
		}
	}
	return nil
}

func miniappAutorepairBuild(ctx context.Context, d Deps, tg int64, u *db.User, tunnelID string) (miniappAutorepairResp, error) {
	s, found, err := d.DB.TunnelRepairSettings().Get(u.ID, tunnelID)
	if err != nil {
		return miniappAutorepairResp{}, err
	}
	resp := miniappAutorepairResp{CanEdit: true}
	if found {
		resp.Enabled, resp.Provider, resp.Option, resp.AllowRelocate = s.Enabled, s.Provider, s.Option, s.AllowRelocate
	}
	if resp.Enabled {
		resp.Blocked = miniappAutorepairBlocked(d, u.Nickname, tunnelID)
	}
	panels := miniappAutorepairAwg3(ctx, d, tg, u.ID)
	resp.Sources = miniappAutorepairSources(ctx, d, tg, u.ID, panels)
	resp.Suggested = miniappAutorepairSuggest(d, u.ID, tunnelID, panels, resp.Sources)
	return resp, nil
}

func writeMiniappAutorepair(w http.ResponseWriter, resp miniappAutorepairResp) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// miniappAutorepairGetHandler -- настройка автопочинки VPN-туннеля, источники
// и подсказка, для экрана туннеля и листа включения.
func miniappAutorepairGetHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("part") != "autorepair" {
			http.NotFound(w, r)
			return
		}
		tg, _, u, ok := miniappAutorepairRouter(d, w, r)
		if !ok {
			return
		}
		tid, ok := miniappAutorepairTunnelID(r)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "bad_tunnel", "bad tunnel_id")
			return
		}
		resp, err := miniappAutorepairBuild(r.Context(), d, tg, u, tid)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "settings not read")
			return
		}
		writeMiniappAutorepair(w, resp)
	}
}

// miniappAutorepairPutHandler включает и выключает автопочинку VPN-туннеля.
//
// Включение проверяет источник: кабинет подключён, свой сервер разрешён этому
// человеку; и снимает стоп после провала (D1) -- человек посмотрел и решил
// попробовать снова. Выключение проверок не требует и источник не трогает:
// повторное включение откроет лист с тем же выбором.
func miniappAutorepairPutHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tg, routerID, u, ok := miniappAutorepairRouter(d, w, r)
		if !ok {
			return
		}
		tid, ok := miniappAutorepairTunnelID(r)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "bad_tunnel", "bad tunnel_id")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		var req miniappAutorepairReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		req.Provider = strings.TrimSpace(req.Provider)
		req.Option = strings.TrimSpace(req.Option)
		switch req.Provider {
		case "", RepairProviderAmnezia, RepairProviderHideMy, RepairProviderAwg3:
		default:
			writeJSONError(w, http.StatusBadRequest, "bad_provider", "неизвестный источник")
			return
		}
		repo := d.DB.TunnelRepairSettings()
		cur, found, err := repo.Get(routerID, tid)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "settings not read")
			return
		}
		if !req.Enabled {
			if found && cur.Enabled {
				cur.Enabled = false
				cur.UpdatedBy = tg
				if err := repo.Put(cur); err != nil {
					writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "settings not saved")
					return
				}
			}
		} else {
			if !miniappAutorepairCheckSource(r.Context(), d, w, tg, routerID, req) {
				return
			}
			if err := repo.Put(db.TunnelRepairSetting{
				UserID: routerID, TunnelID: tid, Enabled: true,
				Provider: req.Provider, Option: req.Option, AllowRelocate: req.AllowRelocate,
				UpdatedBy: tg,
			}); err != nil {
				writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "settings not saved")
				return
			}
			if d.LinkRepair != nil && d.LinkRepair.Attempts.KV != nil {
				if err := d.LinkRepair.Attempts.Clear(u.Nickname, miniappTunnelPrefix+tid); err != nil && d.Logger != nil {
					d.Logger.Warn("autorepair: стоп не снялся", "nickname", u.Nickname, "tunnel_id", tid, "err", err)
				}
			}
		}
		if d.Logger != nil {
			d.Logger.Info("miniapp autorepair set", "nickname", u.Nickname, "tunnel_id", tid,
				"enabled", req.Enabled, "provider", req.Provider, "by", tg)
		}
		resp, err := miniappAutorepairBuild(r.Context(), d, tg, u, tid)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "settings not read")
			return
		}
		writeMiniappAutorepair(w, resp)
	}
}

// miniappAutorepairCheckSource -- источник при включении доступен. Пустой
// источник -- урезанный режим (только перезапуск), его проверять не на чем.
func miniappAutorepairCheckSource(ctx context.Context, d Deps, w http.ResponseWriter, tg, routerID int64, req miniappAutorepairReq) bool {
	switch req.Provider {
	case "":
		return true
	case RepairProviderAwg3:
		panel, iface, ok := strings.Cut(req.Option, "/")
		panel, iface = strings.ToLower(strings.TrimSpace(panel)), strings.TrimSpace(iface)
		if !ok || panel == "" || iface == "" {
			writeJSONError(w, http.StatusBadRequest, "bad_option", "выберите панель и интерфейс своего сервера")
			return false
		}
		if !miniappCanIssueAwg3(d, tg, routerID, panel) {
			writeJSONError(w, http.StatusForbidden, "source_forbidden", "выпуск с этого сервера вам не разрешён")
			return false
		}
		return true
	default:
		if req.Option == "" {
			writeJSONError(w, http.StatusBadRequest, "bad_option", "выберите страну или сервер")
			return false
		}
		if d.VPNCabinet == nil {
			writeJSONError(w, http.StatusConflict, "source_not_connected", "кабинет не подключён")
			return false
		}
		acc, err := d.VPNCabinet.Account(ctx, routerID, req.Provider)
		if err != nil || !acc.Connected {
			writeJSONError(w, http.StatusConflict, "source_not_connected", "кабинет не подключён")
			return false
		}
		return true
	}
}

// miniappAutorepairListHandler -- метки вкладки «VPN-туннели» одним запросом.
// Снимок роутера не трогается: он приходит от агента, а настройка живёт на
// бэкенде.
func miniappAutorepairListHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, routerID, u, ok := miniappAutorepairRouter(d, w, r)
		if !ok {
			return
		}
		list, err := d.DB.TunnelRepairSettings().List(routerID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "settings not read")
			return
		}
		resp := struct {
			Tunnels map[string]string `json:"tunnels"`
		}{Tunnels: map[string]string{}}
		for _, s := range list {
			if !s.Enabled {
				continue
			}
			state := autorepairStateOn
			switch {
			case miniappAutorepairBlocked(d, u.Nickname, s.TunnelID) != "":
				state = autorepairStateBlocked
			case s.Provider == "":
				state = autorepairStateLimited
			}
			resp.Tunnels[s.TunnelID] = state
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
