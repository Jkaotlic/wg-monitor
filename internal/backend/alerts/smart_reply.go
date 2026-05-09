package alerts

import (
	"fmt"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

// SmartReplyState classifies a user's current state for the [📊 Что
// происходит?] smart reply (spec §5.2). Order is OK < DEGRADED < HARD <
// OFFLINE so callers can compare with `>` if desired.
type SmartReplyState int

const (
	StateOK SmartReplyState = iota
	StateDegraded
	StateHard
	StateOffline
)

func (s SmartReplyState) String() string {
	switch s {
	case StateOK:
		return "ok"
	case StateDegraded:
		return "degraded"
	case StateHard:
		return "hard"
	case StateOffline:
		return "offline"
	}
	return "?"
}

// TunnelView is the per-tunnel projection consumed by smart-reply formatting.
// Built by callbacks.Router from events.LatestEventsByPrefix(uid, "tunnel_").
type TunnelView struct {
	Name         string // pretty (e.g. "amnezia")
	CheckName    string // FSM key (e.g. "tunnel_awg11")
	Interface    string // "nwg1"
	HandshakeAge int    // seconds, 0 if unknown
	PingStatus   string // "ok"|"degraded"|"dead"|""
	Latency      int    // last latency ms (0 if unknown)
	FailCount    int    // ping_check fails right now
	FailThresh   int    // ping_check failure threshold
}

// IncidentView is the projection of an active hard incident, used to drive
// the HARD template.
type IncidentView struct {
	CheckName string
	HardSince time.Time
	FailCount int // consecutive_fails at HARD time
}

// UpdateAvailable is one row in the optional "🟡 Доступны обновления:"
// section appended to the smart-reply body. Populated by the router from
// the upstream version cache + the latest VersionAudit for this user.
type UpdateAvailable struct {
	Name      string // "KeeneticOS" | "awg-manager" | "HydraRoute-Neo"
	Installed string
	Available string
}

// SmartReplyArgs is everything FormatSmartReply needs to render a message.
// Built by callbacks.Router.dispatchSmartReply (Task 12).
type SmartReplyArgs struct {
	Nickname        string
	Tunnels         []TunnelView
	ActiveIncidents []IncidentView
	LastReportAge   time.Duration
	IsMobile        bool
	UserID          int64 // needed for callback_data on inline buttons
	// Updates is the optional list of outdated components surfaced as a soft
	// warning. Empty slice (or nil) → section is hidden.
	Updates []UpdateAvailable
}

const (
	smartReplyOfflineThreshold        = 5 * time.Minute
	smartReplyDegradedHandshakeMinSec = 60
)

// ClassifyState applies the spec §5.2 decision tree:
//  1. report age > 5 min                                    → Offline
//  2. ≥1 active hard incident                                → Hard
//  3. any tunnel handshake_age ≥ 60 s                        → Degraded
//  4. any tunnel pingCheck has fail_count > 0 (below thresh) → Degraded
//  5. else                                                   → OK
//
// Rule 3's upper bound is intentionally open — the FSM converts age ≥ 180 s
// into a HARD only after fail_threshold consecutive observations, so during
// the gap we still want "Degraded" rather than misleading "OK".
func ClassifyState(a SmartReplyArgs) SmartReplyState {
	if a.LastReportAge > smartReplyOfflineThreshold {
		return StateOffline
	}
	if len(a.ActiveIncidents) > 0 {
		return StateHard
	}
	for _, t := range a.Tunnels {
		if t.HandshakeAge >= smartReplyDegradedHandshakeMinSec {
			return StateDegraded
		}
		if t.FailCount > 0 && (t.FailThresh == 0 || t.FailCount < t.FailThresh) {
			return StateDegraded
		}
	}
	return StateOK
}

// FormatSmartReply renders the [📊 Что происходит?] response per spec §5.2.
// Returns body text plus the inline-button keyboard appropriate to the
// computed state. The inline keyboard is empty (no rows) only when caller
// explicitly chose to omit one — every state currently emits at least
// "📋 Подробнее" or "📋 Последний отчёт".
func FormatSmartReply(a SmartReplyArgs) (string, tg.InlineKeyboardMarkup) {
	state := ClassifyState(a)
	plainCD := func(action, cn string) string { return fmt.Sprintf("%s:%d:%s", action, a.UserID, cn) }
	silenceCD := func(cn, ttl string) string { return fmt.Sprintf("silence:%d:%s:%s", a.UserID, cn, ttl) }

	var b strings.Builder
	switch state {
	case StateOK:
		fmt.Fprintf(&b, "✅ %s — всё работает.\n\n", a.Nickname)
		for _, t := range a.Tunnels {
			line := fmt.Sprintf("Туннель %s: handshake %s назад", t.Name, humanAgeSec(t.HandshakeAge))
			if t.PingStatus != "" {
				line += fmt.Sprintf(", ping %s", t.PingStatus)
			}
			if t.Latency > 0 {
				line += fmt.Sprintf(" (%d ms)", t.Latency)
			}
			b.WriteString(line + ".\n")
		}
		fmt.Fprintf(&b, "Роутер последний раз отчитывался: %s назад.", humanAgeDur(a.LastReportAge))
		appendUpdatesSection(&b, a.Updates)
		return b.String(), tg.InlineKeyboardMarkup{}

	case StateDegraded:
		fmt.Fprintf(&b, "⚠️ %s — есть подозрения.\n\n", a.Nickname)
		for _, t := range a.Tunnels {
			fmt.Fprintf(&b, "Туннель %s: handshake %d сек назад (норма до 180).\n", t.Name, t.HandshakeAge)
			if t.FailCount > 0 {
				fmt.Fprintf(&b, "Ping: %d неудачи подряд из %d.\n", t.FailCount, t.FailThresh)
			}
		}
		b.WriteString("Роутер пока не считает это сбоем, но подозрительно.\n\nДействия:")
		var rows [][]tg.InlineKeyboardButton
		for _, t := range a.Tunnels {
			label := "🔁 Перезапустить туннель"
			if len(a.Tunnels) > 1 {
				label = "🔁 Перезапуск " + t.Name
			}
			rows = append(rows, []tg.InlineKeyboardButton{
				{Text: label, CallbackData: plainCD("restart_tunnel", t.CheckName)},
				{Text: "▶ Проверить связь", CallbackData: plainCD("pingcheck_now", t.CheckName)},
			})
		}
		appendUpdatesSection(&b, a.Updates)
		return b.String(), tg.InlineKeyboardMarkup{InlineKeyboard: rows}

	case StateHard:
		fmt.Fprintf(&b, "🔴 %s — есть проблема.\n\n", a.Nickname)
		for _, inc := range a.ActiveIncidents {
			age := time.Since(inc.HardSince).Round(time.Minute)
			fmt.Fprintf(&b, "%s не отвечает уже %s.\n", inc.CheckName, durFmt(age))
		}
		b.WriteString("\nЧто можно сделать:")
		var rows [][]tg.InlineKeyboardButton
		seen := map[string]bool{}
		// Buttons per active incident (carries silence button)
		for _, inc := range a.ActiveIncidents {
			if !strings.HasPrefix(inc.CheckName, "tunnel_") {
				continue
			}
			rows = append(rows, []tg.InlineKeyboardButton{
				{Text: "🔁 Перезапустить туннель", CallbackData: plainCD("restart_tunnel", inc.CheckName)},
				{Text: "📊 Запустить диагностику", CallbackData: plainCD("diag_now", inc.CheckName)},
			})
			rows = append(rows, []tg.InlineKeyboardButton{
				{Text: "⏸ Замолчать на час", CallbackData: silenceCD(inc.CheckName, "1h")},
			})
			seen[inc.CheckName] = true
		}
		// Restart-only row for any tunnel that isn't already covered by an incident
		// (multi-tunnel case where some tunnels are degraded but no HARD yet,
		// while at least one other has a HARD — the user expects a restart
		// button for ALL tunnels they can see).
		for _, t := range a.Tunnels {
			if seen[t.CheckName] || t.CheckName == "" {
				continue
			}
			rows = append(rows, []tg.InlineKeyboardButton{
				{Text: "🔁 Перезапуск " + t.Name, CallbackData: plainCD("restart_tunnel", t.CheckName)},
			})
		}
		appendUpdatesSection(&b, a.Updates)
		return b.String(), tg.InlineKeyboardMarkup{InlineKeyboard: rows}

	case StateOffline:
		fmt.Fprintf(&b, "📵 %s — роутер не на связи.\n\n", a.Nickname)
		mins := int(a.LastReportAge.Minutes())
		fmt.Fprintf(&b, "Последний отчёт: %d минут назад.\n", mins)
		b.WriteString("Возможные причины: роутер выключен, нет интернета, агент упал.\n\n")
		b.WriteString("Действия ограничены пока агент не появится.")
		appendUpdatesSection(&b, a.Updates)
		return b.String(), tg.InlineKeyboardMarkup{}
	}
	return "", tg.InlineKeyboardMarkup{}
}

// humanAgeDur is the time.Duration counterpart to humanAgeSec.
func humanAgeDur(d time.Duration) string {
	if d <= 0 {
		return "0с"
	}
	return humanAgeSec(int(d.Seconds()))
}

// appendUpdatesSection writes a soft-warning block listing outdated software
// to b. No-op when updates is empty.
func appendUpdatesSection(b *strings.Builder, updates []UpdateAvailable) {
	if len(updates) == 0 {
		return
	}
	b.WriteString("\n\n🟡 Доступны обновления:\n")
	for _, u := range updates {
		fmt.Fprintf(b, "  • %s %s → %s\n", u.Name, u.Installed, u.Available)
	}
}
