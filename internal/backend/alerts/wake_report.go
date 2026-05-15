package alerts

import (
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/pkg/wire"
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
		fmt.Fprintf(&b, "• %s", name)
	}
	return Card{
		Badge:   "🚗⚠",
		Summary: fmt.Sprintf("%s в сети, есть проблемы", nickname),
		Details: b.String(),
		Hint:    "Открой /panel или нажми 📊 Что происходит?",
	}
}
