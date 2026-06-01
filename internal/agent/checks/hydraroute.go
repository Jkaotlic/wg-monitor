package checks

import (
	"context"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// HydraRouteCheck queries /api/system/hydraroute-status and interprets it
// through the configured routing mechanisms. A stopped HydraRoute is a problem
// only when enabled HR-Neo/HydraRoute rules need it.
type HydraRouteCheck struct {
	Client *awgmgr.Client
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
	HRNeoRoutes      int
	NDMSRoutes       int
	StaticRoutes     int
	SingboxInstalled bool
	SingboxVersion   string
	ActiveBackend    string
	SystemInfoError  string
}

func (h HydraRouteCheck) detectRouteMechanisms(ctx context.Context) (routeMechanisms, error) {
	var out routeMechanisms
	dns, err := h.Client.ListDNSRoutes(ctx)
	if err != nil {
		return out, err
	}
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
	if m.SingboxVersion != "" {
		details["singbox_version"] = m.SingboxVersion
	}
	if m.SystemInfoError != "" {
		details["system_info_error"] = m.SystemInfoError
	}
}
