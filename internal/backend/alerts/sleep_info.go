package alerts

import (
	"fmt"
	"time"
)

// RenderSleepInfo produces the one-shot "🌙 router went offline" info-card
// emitted by the heartbeat watcher when a mobile-lifecycle user crosses
// MobileSleepAfter without heartbeats. Local-time HH:MM only — the topic
// already carries the date context for the operator.
func RenderSleepInfo(nickname string, lastSeen time.Time) Card {
	return Card{
		Badge:   "🌙",
		Summary: fmt.Sprintf("%s вышел из сети (последний heartbeat %s)", nickname, lastSeen.Local().Format("15:04")),
	}
}
