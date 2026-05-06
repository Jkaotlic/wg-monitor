package checks

import (
	"context"
	"time"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// HydraRouteCheck queries /api/system/hydraroute-status. If hrneo is not
// installed at all on this router, the check is a quiet OK with `installed=false`
// — backend can choose to ignore HARD/RECOVERY transitions for non-installed
// services. If installed but not running, it's a fail.
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
	details := map[string]any{
		"installed": st.Installed,
		"running":   st.Running,
	}
	if !st.Installed {
		return OK("hydraroute", start, details)
	}
	if !st.Running {
		return Fail("hydraroute", start, "installed but not running", details)
	}
	return OK("hydraroute", start, details)
}
