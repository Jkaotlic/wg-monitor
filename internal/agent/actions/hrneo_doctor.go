package actions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

func HRNeoDoctor(ctx context.Context, awg *awgmgr.Client, exec ExecFunc) (status, output string) {
	d := hrneoDoctor{ctx: ctx, awg: awg, exec: exec}
	return d.run()
}

type hrneoDoctor struct {
	ctx      context.Context
	awg      *awgmgr.Client
	exec     ExecFunc
	lines    []string
	failures int
}

func (d *hrneoDoctor) run() (string, string) {
	d.lines = append(d.lines, "HR-Neo Doctor")
	d.probeAPI()
	d.probeInitStatus()
	d.probeProcess()
	d.probeIptables()
	d.probeIPSet()
	status := "ok"
	if d.failures > 0 {
		status = "err"
	}
	return status, strings.Join(d.lines, "\n")
}

func (d *hrneoDoctor) probeAPI() {
	ctx, cancel := context.WithTimeout(d.ctx, 6*time.Second)
	defer cancel()
	hr, err := d.awg.HydraRouteStatus(ctx)
	if err != nil {
		d.fail("awg-manager hydraroute status", err.Error())
		return
	}
	if hr == nil || !hr.Installed {
		d.fail("status", "not installed")
		return
	}
	state := "installed/stopped"
	if hr.Running {
		state = "installed/running"
	}
	d.ok("status", state)

	dns, err := d.awg.ListDNSRoutes(ctx)
	if err != nil {
		d.warn("rules", err.Error())
		return
	}
	total, enabled, cidr := 0, 0, 0
	for _, r := range dns {
		if r.Backend != "hydraroute" {
			continue
		}
		total++
		if r.Enabled {
			enabled++
		}
		for _, target := range append(append([]string{}, r.Domains...), r.ManualDomains...) {
			if isIPRouteTarget(target) {
				cidr++
			}
		}
	}
	if total == 0 {
		d.warn("rules", "0 HR-Neo DNS routes")
		return
	}
	d.ok("rules", fmt.Sprintf("%d total, %d enabled, %d CIDR/IP targets", total, enabled, cidr))
}

func (d *hrneoDoctor) probeInitStatus() {
	out, err := d.runExec(3*time.Second, "/opt/etc/init.d/S99hrneo", "status")
	if err != nil {
		d.warn("init.d status", strings.TrimSpace(err.Error()+" "+firstLine(string(out))))
		return
	}
	d.ok("init.d status", firstLine(string(out)))
}

func (d *hrneoDoctor) probeProcess() {
	out, err := d.runExec(3*time.Second, "pidof", "hrneo")
	pids := strings.Fields(string(out))
	if err != nil || len(pids) == 0 {
		d.fail("hrneo process", "not running")
		return
	}
	d.ok("hrneo process", "pid "+strings.Join(pids, ","))
}

func (d *hrneoDoctor) probeIptables() {
	out, err := d.runExec(5*time.Second, "iptables-save")
	if err != nil {
		d.warn("iptables-save", err.Error())
		return
	}
	nflog := strings.Count(string(out), "NFLOG")
	if nflog == 0 {
		d.warn("iptables", "NFLOG rules not found")
		return
	}
	d.ok("iptables", fmt.Sprintf("NFLOG rules: %d", nflog))
}

func (d *hrneoDoctor) probeIPSet() {
	out, err := d.runExec(5*time.Second, "ipset", "list", "-name")
	if err != nil {
		d.warn("ipset", err.Error())
		return
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		d.warn("ipset", "no sets listed")
		return
	}
	d.ok("ipset", fmt.Sprintf("%d sets", len(names)))
	if v, err := opkgHrneoVersion(d.ctx, d.exec); err == nil {
		d.ok("hrneo package", "v"+v)
	}
}

func (d *hrneoDoctor) runExec(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()
	return d.exec(ctx, name, args...)
}

func (d *hrneoDoctor) ok(name, detail string) {
	d.lines = append(d.lines, "✅ "+name+formatDetail(detail))
}

func (d *hrneoDoctor) warn(name, detail string) {
	d.lines = append(d.lines, "⚠️ "+name+formatDetail(detail))
}

func (d *hrneoDoctor) fail(name, detail string) {
	d.failures++
	d.lines = append(d.lines, "❌ "+name+formatDetail(detail))
}
