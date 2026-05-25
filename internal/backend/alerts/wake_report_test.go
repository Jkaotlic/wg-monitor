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
		{Name: "awg_handshake", Status: "fail"},
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
	if !strings.Contains(card.Details, "список туннелей не читается") || !strings.Contains(card.Details, "DNS не отвечает") || !strings.Contains(card.Details, "awg_handshake") {
		t.Errorf("details must list failing checks, got %q", card.Details)
	}
	if strings.Contains(card.Details, "external_reach") {
		t.Errorf("details must NOT list ok checks, got %q", card.Details)
	}
}

func TestRenderWakeReport_StartupFailuresAreWarmup(t *testing.T) {
	checks := []wire.Check{
		{Name: "tunnels", Status: "fail"},
		{Name: "hydraroute", Status: "fail"},
		{Name: "tunnel_awg13", Status: "fail"},
		{Name: "agent_heartbeat", Status: "ok"},
	}
	card := RenderWakeReport("carvan", checks)
	if card.Badge != "🚗⏳" {
		t.Fatalf("startup failures should be warmup badge, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "сервисы ещё поднимаются") {
		t.Fatalf("warmup summary missing: %q", card.Summary)
	}
	if !strings.Contains(card.Hint, "минут") || !strings.Contains(card.Hint, "Повторить проверку") {
		t.Fatalf("warmup hint should explain what to do next: %q", card.Hint)
	}
}

func TestRenderWakeReport_SkipsAgentHeartbeat(t *testing.T) {
	checks := []wire.Check{
		{Name: "agent_heartbeat", Status: "fail"}, // pathological but should be ignored
	}
	card := RenderWakeReport("carvan", checks)
	if card.Badge != "🚗⏳" {
		t.Errorf("agent_heartbeat-only report should wait for health checks; got %q", card.Badge)
	}
	if card.Details != "" {
		t.Errorf("agent_heartbeat fail must not enter details; got %q", card.Details)
	}
	if !strings.Contains(card.Summary, "жду проверки сервисов") {
		t.Errorf("heartbeat-only summary should not claim all-ok: %q", card.Summary)
	}
}
