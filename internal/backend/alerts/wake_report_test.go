package alerts

import (
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestRenderWakeReport_AllOk_SingleLineCard(t *testing.T) {
	checks := []wire.Check{
		{Name: "tunnels", Status: "ok"},
		{Name: "dns_via_tunnel", Status: "ok"},
		{Name: "agent_heartbeat", Status: "ok"},
	}
	card := RenderWakeReport("client-h", checks)
	if card.Badge != "🚗" {
		t.Errorf("badge: want 🚗, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "client-h") {
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
	card := RenderWakeReport("client-h", checks)
	if card.Badge != "🚗⚠" {
		t.Errorf("badge: want 🚗⚠, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "проблемы") {
		t.Errorf("summary must mention проблемы, got %q", card.Summary)
	}
	if !strings.Contains(card.Details, "список VPN-туннелей не читается") || !strings.Contains(card.Details, "поиск сайтов по имени не отвечает") || !strings.Contains(card.Details, "awg_handshake") {
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
	card := RenderWakeReport("client-h", checks)
	if card.Badge != "🚗⏳" {
		t.Fatalf("startup failures should be warmup badge, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "сервисы ещё поднимаются") {
		t.Fatalf("warmup summary missing: %q", card.Summary)
	}
	if !strings.Contains(card.Hint, "минут") || !strings.Contains(card.Hint, "приложени") {
		t.Fatalf("warmup hint should explain what to do next: %q", card.Hint)
	}
}

// Кнопка «Повторить проверку» под отчётом убрана вместе со всеми командными
// кнопками под тревогами -- подсказки не имеют права ссылаться на кнопку,
// которой больше нет.
func TestRenderWakeReport_HintsDoNotReferenceRemovedButtons(t *testing.T) {
	forbidden := []string{"Повторить проверку", "Диагностика"}
	cases := [][]wire.Check{
		nil,
		{{Name: "tunnels", Status: "fail"}, {Name: "hydraroute", Status: "fail"}},
		{{Name: "dns_via_tunnel", Status: "fail"}, {Name: "awg_manager", Status: "fail"}},
	}
	for _, checks := range cases {
		card := RenderWakeReport("client-h", checks)
		for _, bad := range forbidden {
			if strings.Contains(card.Hint, bad) {
				t.Errorf("подсказка отчёта ссылается на исчезнувшую кнопку %q: %q", bad, card.Hint)
			}
		}
	}
}

func TestRenderWakeReport_SkipsAgentHeartbeat(t *testing.T) {
	checks := []wire.Check{
		{Name: "agent_heartbeat", Status: "fail"}, // pathological but should be ignored
	}
	card := RenderWakeReport("client-h", checks)
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

// Отчёт о пробуждении уходит владельцу в личку. Раньше он отправлял в /panel
// и к «📊 Что происходит?» -- панели, которой у владельца нет, -- говорил на
// «ты» и словами движка: «туннель», «DNS», «awg-manager».
func TestRenderWakeReport_SpeaksToOwner(t *testing.T) {
	states := []struct {
		name   string
		checks []wire.Check
		want   []string
	}{
		{"ждёт проверок", []wire.Check{{Name: "agent_heartbeat", Status: "ok"}}, nil},
		{"поднимается", []wire.Check{
			{Name: "tunnels", Status: "fail"},
			{Name: "hydraroute", Status: "fail"},
			{Name: "tunnel_awg13", Status: "fail", Details: map[string]any{"tunnel_name": "Франкфурт"}},
		}, []string{"VPN-туннель «Франкфурт»", "движок умной раздельной маршрутизации"}},
		{"есть проблемы", []wire.Check{
			{Name: "dns_via_tunnel", Status: "fail"},
			{Name: "awg_manager", Status: "fail"},
			{Name: "external_reach", Status: "fail"},
			{Name: "awg_handshake", Status: "fail"},
		}, []string{"приложени"}},
	}
	// «открой », «нажми », «подожди » -- с пробелом: «откройте» законно.
	forbid := []string{"/panel", "📊", "🩺", "открой ", "нажми ", "подожди ", "dns", "awg-manager"}
	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			text := RenderWakeReport("client-h", st.checks).Render(CardOpts{})
			low := strings.ToLower(text)
			for _, f := range forbid {
				if strings.Contains(low, f) {
					t.Errorf("владелец читает %q:\n%s", f, text)
				}
			}
			for _, w := range st.want {
				if !strings.Contains(text, w) {
					t.Errorf("нет %q:\n%s", w, text)
				}
			}
			assertSaysVPNTunnel(t, st.name, text)
		})
	}
}
