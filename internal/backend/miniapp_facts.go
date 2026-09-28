package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// miniappSignalsMinAgent -- с какой версии агент шлёт факты и знает команды v0.47.
const miniappSignalsMinAgent = "v0.47.0"

// Факт старше трёх своих периодов показывается «на момент последнего
// отчёта»: адрес выхода меряется раз в 20 минут на VPN-туннель, остальное
// подтверждается раз в 10 минут.
const (
	miniappExitStaleAfter = 60 * time.Minute
	miniappFactStaleAfter = 30 * time.Minute
)

// miniappFactsResp -- БЕЛЫЙ СПИСОК, а не пересказ блока агента: имена
// интерфейсов, источник замера и текст ошибки -- только админу (та же
// граница, что у miniapp_tunnels.go).
type miniappFactsResp struct {
	AgentVersion string                 `json:"agent_version"`
	Supported    bool                   `json:"supported"`
	Exit         *miniappExitFacts      `json:"exit,omitempty"`
	WAN          *miniappWANFacts       `json:"wan,omitempty"`
	NativeDNS    *miniappNativeDNSFacts `json:"native_dns,omitempty"`
	Hooks        *miniappHookFacts      `json:"hooks,omitempty"`
	PingFails24h map[string]int         `json:"ping_fails_24h"`
}

type miniappExitFacts struct {
	Stale   bool                        `json:"stale"`
	Tunnels map[string]miniappExitProbe `json:"tunnels"`
}

type miniappExitProbe struct {
	VPNIP    string `json:"vpn_ip,omitempty"`
	DirectIP string `json:"direct_ip,omitempty"`
	Changed  *bool  `json:"changed,omitempty"`
	At       string `json:"at"`
	Failed   bool   `json:"failed,omitempty"`
	Source   string `json:"source,omitempty"`
	Err      string `json:"err,omitempty"`
}

type miniappWANFacts struct {
	Stale       bool             `json:"stale"`
	Unsupported bool             `json:"unsupported,omitempty"`
	Links       []miniappWANLink `json:"links"`
}

type miniappWANLink struct {
	Label     string `json:"label"`
	Role      string `json:"role"`
	Up        bool   `json:"up"`
	PingCheck string `json:"pingcheck"`
	Name      string `json:"name,omitempty"`
}

type miniappNativeDNSFacts struct {
	Stale            bool                   `json:"stale"`
	UnverifiedReason string                 `json:"unverified_reason,omitempty"`
	Lists            []miniappNativeDNSList `json:"lists"`
}

type miniappNativeDNSList struct {
	Name     string `json:"name"`
	Domains  int    `json:"domains"`
	TunnelID string `json:"tunnel_id,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Owner    string `json:"owner"`
	Issue    string `json:"issue,omitempty"`
	Target   string `json:"target,omitempty"`
}

type miniappHookFacts struct {
	Stale        bool   `json:"stale"`
	State        string `json:"state"`
	LastWakeAt   string `json:"last_wake_at,omitempty"`
	Wakes1h      int    `json:"wakes_1h"`
	Suppressed1h int    `json:"suppressed_1h"`
}

func pingCheckWord(p *string) string {
	switch {
	case p == nil:
		return ""
	case *p == "":
		return "unset"
	default:
		return "set"
	}
}

func buildMiniappFacts(all map[string]db.RouterFact, admin bool, now time.Time) miniappFactsResp {
	var resp miniappFactsResp
	stale := func(f db.RouterFact, after time.Duration) bool { return now.Sub(f.ReceivedAt) > after }
	if f, ok := all[db.FactExit]; ok {
		var src wire.ExitFacts
		if json.Unmarshal(f.Body, &src) == nil {
			out := &miniappExitFacts{Stale: stale(f, miniappExitStaleAfter), Tunnels: map[string]miniappExitProbe{}}
			for id, p := range src.Tunnels {
				mp := miniappExitProbe{VPNIP: p.VPNIP, DirectIP: p.DirectIP, Changed: p.Changed,
					At: p.At.UTC().Format(time.RFC3339), Failed: p.Changed == nil}
				if admin {
					mp.Source, mp.Err = p.Source, p.Err
				}
				out.Tunnels[id] = mp
			}
			resp.Exit = out
		}
	}
	if f, ok := all[db.FactWAN]; ok {
		var src wire.WANFacts
		if json.Unmarshal(f.Body, &src) == nil {
			out := &miniappWANFacts{Stale: stale(f, miniappFactStaleAfter), Unsupported: src.Unsupported, Links: []miniappWANLink{}}
			for _, l := range src.Links {
				ml := miniappWANLink{Label: l.Label, Role: l.Role, Up: l.Up, PingCheck: pingCheckWord(l.PingCheck)}
				if admin {
					ml.Name = l.Name
				}
				out.Links = append(out.Links, ml)
			}
			resp.WAN = out
		}
	}
	if f, ok := all[db.FactNativeDNS]; ok {
		var src wire.NativeDNSFacts
		if json.Unmarshal(f.Body, &src) == nil {
			out := &miniappNativeDNSFacts{Stale: stale(f, miniappFactStaleAfter), UnverifiedReason: src.UnverifiedReason, Lists: []miniappNativeDNSList{}}
			for _, l := range src.Lists {
				ml := miniappNativeDNSList{Name: l.Name, Domains: l.Domains, TunnelID: l.TunnelID, Mode: l.Mode, Owner: l.Owner, Issue: l.Issue}
				if admin {
					ml.Target = l.Target
				}
				out.Lists = append(out.Lists, ml)
			}
			resp.NativeDNS = out
		}
	}
	if f, ok := all[db.FactHooks]; ok {
		var src wire.HookFacts
		if json.Unmarshal(f.Body, &src) == nil {
			out := &miniappHookFacts{Stale: stale(f, miniappFactStaleAfter), State: src.State, Wakes1h: src.Wakes1h, Suppressed1h: src.Suppressed1h}
			if src.LastWakeAt != nil {
				out.LastWakeAt = src.LastWakeAt.UTC().Format(time.RFC3339)
			}
			resp.Hooks = out
		}
	}
	return resp
}

// miniappRouterFactsHandler -- GET /v1/miniapp/routers/{id}/facts. Видят все,
// у кого есть доступ к роутеру; топология -- только админу.
func miniappRouterFactsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
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
		all, err := d.DB.RouterFacts().All(routerID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "facts lookup failed")
			return
		}
		now := time.Now().UTC()
		resp := buildMiniappFacts(all, miniappIsAdmin(telegramUserID, d.TelegramAdminUserID), now)
		if u.LastDeployedVersion != nil {
			resp.AgentVersion = *u.LastDeployedVersion
		}
		resp.Supported = agentAtLeast(resp.AgentVersion, miniappSignalsMinAgent)
		resp.PingFails24h = map[string]int{}
		if runs, err := d.DB.PingRuns().Since(routerID, now.Add(-24*time.Hour)); err == nil {
			for _, run := range runs {
				resp.PingFails24h[run.TunnelID] += run.Fails
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
