package unstick

import (
	"context"
	"slices"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// CheckName -- проверка сторожа зависаний в отчёте. Не tunnel_* и не dns_*:
// у бэкенда свои пороги и группировки для этих префиксов.
const CheckName = "awgm_unstick"

// DetailReasonGaveUp -- контракт с текстами бэкенда (alerts/format.go).
const DetailReasonGaveUp = "gave_up"

type SnapshotSource interface {
	Snapshot() Snapshot
}

// Check -- тонкая проверка: только читает снимок сторожа.
//
//	ok:   {active:"<id>"|""} | {ready:false} | {disabled:true}
//	fail: {reason:"gave_up", tunnels:[{tunnel_id,name,status,details,steps,since[,flapping,fixes]}]}
type Check struct {
	Source   SnapshotSource
	Disabled bool
}

func (Check) Name() string { return CheckName }

func (c Check) Run(_ context.Context, _ checks.Deps) wire.Check {
	start := time.Now()
	if c.Disabled || c.Source == nil {
		return checks.OK(CheckName, start, map[string]any{"disabled": true})
	}
	s := c.Source.Snapshot()
	if !s.Ready {
		return checks.OK(CheckName, start, map[string]any{"ready": false})
	}
	if len(s.GaveUp) == 0 {
		return checks.OK(CheckName, start, map[string]any{"active": s.Active})
	}
	tl := make([]map[string]any, 0, len(s.GaveUp))
	for _, g := range s.GaveUp {
		item := map[string]any{
			"tunnel_id": g.TunnelID,
			"name":      g.Name,
			"status":    g.Status,
			"details":   g.Details,
			"steps":     g.Steps,
			"since":     g.Since.UTC().Format(time.RFC3339),
		}
		if g.Flapping {
			item["flapping"] = true
			item["fixes"] = g.Fixes
		}
		tl = append(tl, item)
	}
	return checks.Fail(CheckName, start, failText(s.GaveUp),
		map[string]any{"reason": DetailReasonGaveUp, "tunnels": tl})
}

// failText -- по фактическим шагам (AG-07): перезапуск службы называем,
// только если он был; одни «зависает снова и снова» -- о них.
func failText(gs []GaveUpTunnel) string {
	stuck, service := false, false
	for _, g := range gs {
		if g.Flapping {
			continue
		}
		stuck = true
		service = service || slices.Contains(g.Steps, "service_restart")
	}
	switch {
	case !stuck:
		return "awg-manager tunnel keeps getting stuck again and again"
	case service:
		return "awg-manager tunnel stays stuck after restart and awg-manager restart"
	default:
		return "awg-manager tunnel stays stuck after restart"
	}
}
