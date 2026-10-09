package unstick

import (
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
)

type snap Snapshot

func (s snap) Snapshot() Snapshot { return Snapshot(s) }

func TestCheck_States(t *testing.T) {
	if c := (Check{Disabled: true}).Run(ctx, checks.Deps{}); c.Status != "ok" || c.Details["disabled"] != true {
		t.Errorf("disabled: %+v", c)
	}
	if c := (Check{Source: snap{}}).Run(ctx, checks.Deps{}); c.Status != "ok" || c.Details["ready"] != false {
		t.Errorf("unread: %+v", c)
	}
	if c := (Check{Source: snap{Ready: true, Active: "nwg0"}}).Run(ctx, checks.Deps{}); c.Status != "ok" || c.Details["active"] != "nwg0" {
		t.Errorf("active: %+v", c)
	}
	since := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	c := (Check{Source: snap{Ready: true, GaveUp: []GaveUpTunnel{{
		TunnelID: "nwg0", Name: "hidemy", Status: "broken", Details: "x", Steps: []string{"restart", "service_restart"}, Since: since,
	}}}}).Run(ctx, checks.Deps{})
	if c.Name != CheckName || c.Status != "fail" || c.Details["reason"] != DetailReasonGaveUp {
		t.Fatalf("gave up: %+v", c)
	}
	tl, ok := c.Details["tunnels"].([]map[string]any)
	if !ok || len(tl) != 1 || tl[0]["tunnel_id"] != "nwg0" || tl[0]["name"] != "hidemy" ||
		tl[0]["status"] != "broken" || tl[0]["details"] != "x" || tl[0]["since"] != "2026-10-09T12:00:00Z" {
		t.Errorf("tunnels: %#v", c.Details["tunnels"])
	}
}
