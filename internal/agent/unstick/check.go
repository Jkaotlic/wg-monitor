package unstick

import (
	"context"
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
//	fail: {reason:"gave_up", tunnels:[{tunnel_id,name,status,details,steps,since}]}
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
		tl = append(tl, map[string]any{
			"tunnel_id": g.TunnelID,
			"name":      g.Name,
			"status":    g.Status,
			"details":   g.Details,
			"steps":     g.Steps,
			"since":     g.Since.UTC().Format(time.RFC3339),
		})
	}
	return checks.Fail(CheckName, start, "awg-manager tunnel stays stuck after restart and awg-manager restart",
		map[string]any{"reason": DetailReasonGaveUp, "tunnels": tl})
}
