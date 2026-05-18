package actions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// RouterDoctor runs a read-only health snapshot from inside the router.
// It intentionally avoids mutating ndmc/awg-manager state: the command is
// safe to expose as a Telegram "is everything alive?" button.
func RouterDoctor(ctx context.Context, awg *awgmgr.Client, exec ExecFunc) (status, output string) {
	d := routerDoctor{ctx: ctx, awg: awg, exec: exec}
	return d.run()
}

type routerDoctor struct {
	ctx  context.Context
	awg  *awgmgr.Client
	exec ExecFunc

	lines    []string
	failures int
}

func (d *routerDoctor) run() (string, string) {
	d.lines = append(d.lines, "🩺 Проверка роутера")
	d.probeAwgManager()
	d.probeTunnels()
	d.probePingCheck()
	d.probeProcess("wg-monitor agent", "wg-monitor")
	d.probeProcess("awg-manager daemon", "awg-manager")
	d.probeRoute()

	status := "ok"
	if d.failures > 0 {
		status = "err"
	}
	return status, strings.Join(d.lines, "\n")
}

func (d *routerDoctor) probeAwgManager() {
	if d.awg == nil {
		d.fail("awg-manager API", "client not configured")
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 6*time.Second)
	defer cancel()
	sys, err := d.awg.SystemInfo(ctx)
	if err != nil {
		d.fail("awg-manager API", err.Error())
		return
	}
	var parts []string
	if sys.Version != "" {
		parts = append(parts, "v"+sys.Version)
	}
	if sys.ActiveBackend != "" {
		parts = append(parts, "backend "+sys.ActiveBackend)
	}
	if sys.RouterIP != "" {
		parts = append(parts, sys.RouterIP)
	}
	if sys.FirmwareVersion != "" {
		parts = append(parts, "FW "+sys.FirmwareVersion)
	}
	if sys.TotalMemoryMB > 0 {
		parts = append(parts, fmt.Sprintf("%dMB RAM", sys.TotalMemoryMB))
	}
	d.ok("awg-manager API", strings.Join(parts, ", "))
	if sys.IsLowMemory {
		d.warn("memory", "router is marked low-memory by awg-manager")
	}
	if sys.Singbox.Installed {
		d.ok("sing-box", versionDetail(sys.Singbox.Version))
	}
}

func (d *routerDoctor) probeTunnels() {
	if d.awg == nil {
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 6*time.Second)
	defer cancel()
	ta, err := d.awg.TunnelsAll(ctx)
	if err != nil {
		d.fail("tunnels", err.Error())
		return
	}
	total := len(ta.Tunnels)
	running := 0
	var defaults []string
	for _, t := range ta.Tunnels {
		if t.Status == "running" {
			running++
		}
		if t.DefaultRoute {
			defaults = append(defaults, tunnelLabel(t))
		}
	}
	detail := fmt.Sprintf("%d/%d running", running, total)
	if len(defaults) > 0 {
		detail += "; default: " + strings.Join(defaults, ", ")
	}
	if total == 0 {
		d.warn("tunnels", "no managed tunnels")
		return
	}
	if running == 0 {
		d.fail("tunnels", detail)
		return
	}
	d.ok("tunnels", detail)
}

func (d *routerDoctor) probePingCheck() {
	if d.awg == nil {
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 6*time.Second)
	defer cancel()
	pc, err := d.awg.PingCheckStatus(ctx)
	if err != nil {
		d.warn("pingcheck", err.Error())
		return
	}
	if !pc.Enabled {
		d.warn("pingcheck", "disabled")
		return
	}
	total := len(pc.Tunnels)
	alive := 0
	var bad []string
	for _, t := range pc.Tunnels {
		if strings.EqualFold(t.Status, "alive") || strings.EqualFold(t.Status, "ok") {
			alive++
			continue
		}
		bad = append(bad, fmt.Sprintf("%s=%s", nonEmpty(t.TunnelName, t.TunnelID), nonEmpty(t.Status, "unknown")))
	}
	detail := fmt.Sprintf("enabled, %d/%d alive", alive, total)
	if len(bad) > 0 {
		detail += "; " + strings.Join(bad, ", ")
		d.fail("pingcheck", detail)
		return
	}
	d.ok("pingcheck", detail)
}

func (d *routerDoctor) probeProcess(label, proc string) {
	if d.exec == nil {
		d.warn(label, "exec not configured")
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	defer cancel()
	out, err := d.exec(ctx, "pidof", proc)
	pids := strings.Fields(string(out))
	if err != nil || len(pids) == 0 {
		d.fail(label, "not running")
		return
	}
	d.ok(label, "pid "+strings.Join(pids, ","))
}

func (d *routerDoctor) probeRoute() {
	if d.exec == nil {
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	defer cancel()
	out, err := d.exec(ctx, "ip", "route", "get", "1.1.1.1")
	if err != nil {
		d.warn("default route", err.Error())
		return
	}
	line := firstLine(string(out))
	if line == "" {
		d.warn("default route", "empty output")
		return
	}
	d.ok("default route", line)
}

func (d *routerDoctor) ok(name, detail string) {
	d.lines = append(d.lines, "✅ "+name+formatDetail(detail))
}

func (d *routerDoctor) warn(name, detail string) {
	d.lines = append(d.lines, "⚠️ "+name+formatDetail(detail))
}

func (d *routerDoctor) fail(name, detail string) {
	d.failures++
	d.lines = append(d.lines, "❌ "+name+formatDetail(detail))
}

func formatDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	return ": " + trimForTG(detail, 240)
}

func tunnelLabel(t awgmgr.Tunnel) string {
	label := nonEmpty(t.Name, t.ID)
	if t.InterfaceName != "" {
		label += " (" + t.InterfaceName + ")"
	}
	return label
}

func versionDetail(v string) string {
	if strings.TrimSpace(v) == "" {
		return "installed"
	}
	return "v" + strings.TrimSpace(v)
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func trimForTG(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
