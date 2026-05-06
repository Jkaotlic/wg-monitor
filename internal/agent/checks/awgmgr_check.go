package checks

import (
	"context"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// AwgManagerCheck is a single check that probes /api/system/info — proves
// that the local awg-manager daemon is reachable. It also stashes system
// info in Details so backend can render firmware/version/singbox in alerts
// without a separate diag round-trip.
type AwgManagerCheck struct {
	Client *awgmgr.Client
}

func (a AwgManagerCheck) Name() string { return "awg_manager" }

func (a AwgManagerCheck) Run(ctx context.Context, _ Deps) wire.Check {
	start := time.Now()
	info, err := a.Client.SystemInfo(ctx)
	if err != nil {
		return Fail("awg_manager", start, err.Error(), map[string]any{
			"base_url": a.Client.BaseURL,
		})
	}
	return OK("awg_manager", start, map[string]any{
		"version":               info.Version,
		"firmware":              info.FirmwareVersion,
		"keenetic_os":           info.KeeneticOS,
		"active_backend":        info.ActiveBackend,
		"kernel_module_loaded":  info.KernelModuleLoaded,
		"kernel_module_model":   info.KernelModuleModel,
		"kernel_module_version": info.KernelModuleVersion,
		"singbox_installed":     info.Singbox.Installed,
		"singbox_version":       info.Singbox.Version,
		"router_ip":             info.RouterIP,
		"total_memory_mb":       info.TotalMemoryMB,
		"is_low_memory":         info.IsLowMemory,
	})
}
