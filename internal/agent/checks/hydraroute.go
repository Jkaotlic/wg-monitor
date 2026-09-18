package checks

import (
	"context"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// HydraRouteCheck queries /api/system/hydraroute-status and interprets it
// through the configured routing mechanisms. A stopped HydraRoute is a problem
// only when enabled HR-Neo/HydraRoute rules need it.
type HydraRouteCheck struct {
	Client *awgmgr.Client
	// Policies читает сводку политик доступа (actions.PolicyBriefs, проводка в
	// cmd/agent): checks не может импортировать actions -- тот импортирует
	// checks. Получает уже прочитанный список DNS-правил. nil -- сводки нет.
	Policies func(ctx context.Context, dns []awgmgr.DNSRoute) ([]wire.PolicyBrief, error)
}

func (h HydraRouteCheck) Name() string { return "hydraroute" }

func (h HydraRouteCheck) Run(ctx context.Context, _ Deps) wire.Check {
	start := time.Now()
	st, err := h.Client.HydraRouteStatus(ctx)
	if err != nil {
		return Fail("hydraroute", start, err.Error(), nil)
	}
	mechs, mechErr := h.detectRouteMechanisms(ctx)
	details := map[string]any{
		"installed": st.Installed,
		"running":   st.Running,
	}
	mechs.addDetails(details)
	if mechErr != nil {
		details["mechanism_probe_error"] = mechErr.Error()
	}
	// On an active sing-box router, HR-Neo/HydraRoute is bypassed — sing-box is
	// the real router (deviceMode routes per its own policy via the AWG tunnels)
	// — so a stopped or absent HydraRoute is not an incident, even when stale
	// enabled hydraroute DNS rules still linger in awg-manager config.
	if mechs.SingboxRouterActive {
		details["ignored_singbox_router"] = true
		return OK("hydraroute", start, details)
	}
	h.addPolicies(ctx, mechs, details)
	if !st.Installed {
		if mechs.hrneoRequired() {
			return Fail("hydraroute", start, "required by active HR-Neo routes but not installed", details)
		}
		return OK("hydraroute", start, details)
	}
	if !st.Running {
		if mechs.hrneoRequired() {
			return Fail("hydraroute", start, "installed but not running; HR-Neo routes are active", details)
		}
		if mechErr == nil {
			details["ignored_not_running"] = true
			return OK("hydraroute", start, details)
		}
		return Fail("hydraroute", start, "installed but not running", details)
	}
	return OK("hydraroute", start, details)
}

type routeMechanisms struct {
	// dns -- прочитанный список DNS-правил; nil, если чтение не удалось.
	// Нужен сводке политик, чтобы не читать его второй раз.
	dns                 []awgmgr.DNSRoute
	dnsRead             bool
	HRNeoRoutes         int
	NDMSRoutes          int
	StaticRoutes        int
	SingboxInstalled    bool
	SingboxRouterActive bool
	SingboxVersion      string
	ActiveBackend       string
	SystemInfoError     string
}

func (h HydraRouteCheck) detectRouteMechanisms(ctx context.Context) (routeMechanisms, error) {
	var out routeMechanisms
	dns, err := h.Client.ListDNSRoutes(ctx)
	if err != nil {
		return out, err
	}
	out.dns, out.dnsRead = dns, true
	for _, route := range dns {
		if !route.Enabled {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(route.Backend)) {
		case "hydraroute":
			out.HRNeoRoutes++
		case "ndms":
			out.NDMSRoutes++
		}
	}
	statics, err := h.Client.ListStaticRoutes(ctx)
	if err != nil {
		return out, err
	}
	for _, route := range statics {
		if route.Enabled {
			out.StaticRoutes++
		}
	}
	// Best-effort: when the sing-box router is active it is the authoritative
	// routing method, so HR-Neo/HydraRoute is inert. Older awg-manager builds
	// 404 /api/settings/get (Settings errors) → SingboxRouterActive stays false
	// and the mechanism-count logic below decides.
	if settings, err := h.Client.Settings(ctx); err == nil {
		out.SingboxRouterActive = settings.SingboxRouterActive()
	}
	info, err := h.Client.SystemInfo(ctx)
	if err != nil {
		out.SystemInfoError = err.Error()
		return out, nil
	}
	out.ActiveBackend = strings.TrimSpace(info.ActiveBackend)
	out.SingboxInstalled = info.Singbox.Installed || strings.Contains(strings.ToLower(out.ActiveBackend), "sing")
	out.SingboxVersion = strings.TrimSpace(info.Singbox.Version)
	return out, nil
}

// addPolicies кладёт в details сводку политик: кто несёт обход сейчас и что
// в запасе (details["policies"]). Экран без неё угадывал несущий туннель и
// красил живой обход тревогой запасного (workrouter, 18.09.2026). Ошибка
// чтения -- policies_error, проверка от неё не падает: HydraRoute не сломан.
// Сборка без политик -- поля нет вовсе. Без прочитанных правил сводку не
// строим: её счётчики были бы нулями от незнания.
func (h HydraRouteCheck) addPolicies(ctx context.Context, m routeMechanisms, details map[string]any) {
	if h.Policies == nil || !m.dnsRead {
		return
	}
	briefs, err := h.Policies(ctx, m.dns)
	if err != nil {
		details["policies_error"] = err.Error()
		return
	}
	if briefs != nil {
		details["policies"] = briefs
	}
}

func (m routeMechanisms) hrneoRequired() bool {
	return m.HRNeoRoutes > 0
}

func (m routeMechanisms) addDetails(details map[string]any) {
	details["routes_hrneo"] = m.HRNeoRoutes
	details["routes_ndms"] = m.NDMSRoutes
	details["routes_static"] = m.StaticRoutes
	details["hrneo_required"] = m.hrneoRequired()
	if m.ActiveBackend != "" {
		details["active_backend"] = m.ActiveBackend
	}
	details["singbox_installed"] = m.SingboxInstalled
	details["singbox_router_active"] = m.SingboxRouterActive
	if m.SingboxVersion != "" {
		details["singbox_version"] = m.SingboxVersion
	}
	if m.SystemInfoError != "" {
		details["system_info_error"] = m.SystemInfoError
	}
}
