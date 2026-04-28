package alerts

import (
	"fmt"
	"time"
)

type HardArgs struct {
	Nickname    string
	CheckName   string
	ConsecFails int
	HardSince   time.Time
	Detail      string
}

type RecoveryArgs struct {
	Nickname    string
	CheckName   string
	HardSince   time.Time
	RecoveredAt time.Time
}

func FormatHard(a HardArgs) string {
	return fmt.Sprintf(
		"🔴 [%s] %s — DOWN\nFails: %d подряд\nHard since: %s\n%s",
		a.Nickname, a.CheckName, a.ConsecFails,
		a.HardSince.In(mscLoc()).Format("2006-01-02 15:04:05 МСК"),
		a.Detail,
	)
}

func FormatRecovery(a RecoveryArgs) string {
	d := a.RecoveredAt.Sub(a.HardSince).Round(time.Minute)
	return fmt.Sprintf(
		"✅ [%s] %s — RECOVERED\nDowntime: %s",
		a.Nickname, a.CheckName, durFmt(d),
	)
}

func FormatRouterOffline(nickname string, since time.Duration) string {
	return fmt.Sprintf("🔴 [%s] ROUTER OFFLINE — нет heartbeat'ов %s", nickname, durFmt(since.Round(time.Minute)))
}

func durFmt(d time.Duration) string {
	if d < time.Minute {
		return "< 1m"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func mscLoc() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("МСК", 3*3600)
	}
	return loc
}

type RealertArgs struct {
	Nickname     string
	CheckName    string
	HardSince    time.Time
	RealertCount int
}

func FormatRealert(args RealertArgs) string {
	age := time.Since(args.HardSince).Round(time.Minute)
	return fmt.Sprintf(
		"🔁 [%s] %s — STILL DOWN\nHard since: %s (%s ago)\nRe-alert #%d (every 6h)",
		args.Nickname,
		args.CheckName,
		args.HardSince.UTC().Format("2006-01-02 15:04 MST"),
		durFmt(age),
		args.RealertCount,
	)
}
