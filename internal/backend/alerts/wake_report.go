package alerts

import (
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// RenderWakeReport produces the adaptive wake-card for a mobile router that
// just rejoined (Report.Resumed=true). All checks green → one-line "🚗 в
// сети — всё ок". Any failures → "🚗⚠ есть проблемы" with bullet list of
// failing check names. agent_heartbeat is always excluded from the failure
// tally — it's a transport check, not a router-health signal.
func RenderWakeReport(nickname string, checks []wire.Check) Card {
	var failed []string
	for _, c := range checks {
		if c.Name == "agent_heartbeat" {
			continue
		}
		if c.Status != "ok" {
			failed = append(failed, c.Name)
		}
	}
	if len(failed) == 0 {
		return Card{
			Badge:   "🚗",
			Summary: fmt.Sprintf("%s в сети — всё ок", nickname),
		}
	}
	var b strings.Builder
	for i, name := range failed {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "• %s", wakeCheckLabel(name))
	}
	return Card{
		Badge:   "🚗⚠",
		Summary: fmt.Sprintf("%s в сети, есть проблемы", nickname),
		Details: b.String(),
		Hint:    "Нажми 📊 Что происходит? или открой /panel — там будет видно, что именно упало и какие кнопки ремонта доступны.",
	}
}

func wakeCheckLabel(name string) string {
	switch name {
	case "tunnels":
		return "список туннелей не читается"
	case "dns_via_tunnel":
		return "DNS не отвечает"
	}
	switch checkCategory(name) {
	case "tunnel":
		return "туннель не на связи"
	case "dns":
		return "DNS не отвечает"
	case "hydraroute":
		return "HydraRoute не работает"
	case "awg_manager", "awgmgr_api":
		return "awg-manager не отвечает"
	case "external_reach":
		return "внешние сервисы не открываются через туннель"
	default:
		return name
	}
}
