package tg

import (
	"fmt"
	"strings"
	"time"
)

type MobileFleetRow struct {
	Nickname            string
	Kind                string
	LastSeenAt          *time.Time
	LastDeployedVersion string
	Ring                string
	PendingVersion      string
}

func MobileFleetPanelText(rows []MobileFleetRow, now time.Time) string {
	var b strings.Builder
	b.WriteString("📱 Мобильные роутеры\n\n")
	count := 0
	for _, r := range rows {
		if r.Kind != "mobile" {
			continue
		}
		count++
		state := "ещё не отчитывался"
		if r.LastSeenAt != nil {
			age := now.Sub(*r.LastSeenAt)
			if age < -2*time.Minute {
				state = "clock-skew " + shortAge(-age)
			} else if age <= 5*time.Minute {
				state = "в сети"
			} else {
				state = "спит " + shortAge(age)
			}
		}
		ring := r.Ring
		if ring == "" {
			ring = "stable"
		}
		version := r.LastDeployedVersion
		if version == "" {
			version = "неизвестно"
		}
		fmt.Fprintf(&b, "  • %s: %s, канал %s, агент %s", r.Nickname, state, ring, version)
		if r.PendingVersion != "" {
			fmt.Fprintf(&b, ", ждёт обновление %s", r.PendingVersion)
		}
		b.WriteByte('\n')
	}
	if count == 0 {
		b.WriteString("Мобильных роутеров пока нет.\n")
	}
	return b.String()
}

func shortAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%dс", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dм", int(d.Minutes()))
	}
	return fmt.Sprintf("%dч", int(d.Hours()))
}
