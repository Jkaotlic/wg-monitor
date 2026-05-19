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
	b.WriteString("Mobile fleet\n\n")
	count := 0
	for _, r := range rows {
		if r.Kind != "mobile" {
			continue
		}
		count++
		state := "never"
		if r.LastSeenAt != nil {
			age := now.Sub(*r.LastSeenAt)
			if age <= 5*time.Minute {
				state = "awake"
			} else {
				state = "sleeping " + shortAge(age)
			}
		}
		ring := r.Ring
		if ring == "" {
			ring = "stable"
		}
		version := r.LastDeployedVersion
		if version == "" {
			version = "unknown"
		}
		fmt.Fprintf(&b, "- %s: %s, ring %s, agent %s", r.Nickname, state, ring, version)
		if r.PendingVersion != "" {
			fmt.Fprintf(&b, ", pending %s", r.PendingVersion)
		}
		b.WriteByte('\n')
	}
	if count == 0 {
		b.WriteString("No mobile routers yet.\n")
	}
	return b.String()
}

func shortAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
