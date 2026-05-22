package alerts

import (
	"strings"
	"testing"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRenderWakeReport_AllOk_SingleLineCard(t *testing.T) {
	checks := []wire.Check{
		{Name: "tunnels", Status: "ok"},
		{Name: "dns_via_tunnel", Status: "ok"},
		{Name: "agent_heartbeat", Status: "ok"},
	}
	card := RenderWakeReport("carvan", checks)
	if card.Badge != "🚗" {
		t.Errorf("badge: want 🚗, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "carvan") {
		t.Errorf("summary missing nick: %q", card.Summary)
	}
	if !strings.Contains(card.Summary, "всё ок") {
		t.Errorf("summary missing OK marker: %q", card.Summary)
	}
	if card.Details != "" {
		t.Errorf("all-ok must omit details, got %q", card.Details)
	}
}

func TestRenderWakeReport_WithFailures_BulletDetails(t *testing.T) {
	checks := []wire.Check{
		{Name: "tunnels", Status: "fail"},
		{Name: "dns_via_tunnel", Status: "fail"},
		{Name: "external_reach", Status: "ok"},
		{Name: "agent_heartbeat", Status: "ok"},
	}
	card := RenderWakeReport("carvan", checks)
	if card.Badge != "🚗⚠" {
		t.Errorf("badge: want 🚗⚠, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "проблемы") {
		t.Errorf("summary must mention проблемы, got %q", card.Summary)
	}
	if !strings.Contains(card.Details, "список туннелей не читается") || !strings.Contains(card.Details, "DNS не отвечает") {
		t.Errorf("details must list failing checks, got %q", card.Details)
	}
	if strings.Contains(card.Details, "external_reach") {
		t.Errorf("details must NOT list ok checks, got %q", card.Details)
	}
}

func TestRenderWakeReport_SkipsAgentHeartbeat(t *testing.T) {
	checks := []wire.Check{
		{Name: "agent_heartbeat", Status: "fail"}, // pathological but should be ignored
	}
	card := RenderWakeReport("carvan", checks)
	if card.Badge != "🚗" {
		t.Errorf("agent_heartbeat fail must not flip badge; got %q", card.Badge)
	}
	if card.Details != "" {
		t.Errorf("agent_heartbeat fail must not enter details; got %q", card.Details)
	}
}
