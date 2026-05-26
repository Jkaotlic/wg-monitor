package alerts

import (
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
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
	NDMSName     string // Keenetic interface name, e.g. "Wireguard3"
	Interface    string // "nwg1"
	Enabled      bool
	HasEnabled   bool
	Status       string
	HasHandshake bool
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
	Details   map[string]any
}

// UpdateAvailable is one row in the optional "🟡 Доступны обновления:"
// section appended to the smart-reply body. Populated by the router from
// the upstream version cache + the latest VersionAudit for this user.
type UpdateAvailable struct {
	Name      string // "KeeneticOS" | "awg-manager" | "HydraRoute-Neo"
	Installed string
	Available string
	Hint      string
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
	smartReplyOfflineThreshold = 5 * time.Minute
	// smartReplyDegradedHandshakeMinSec must match the "норма до N" value
	// printed in the StateDegraded message body (see line below). The original
	// value of 60 conflicted with the rendered "норма до 180" text — it
	// flagged AWG tunnels without PersistentKeepalive (handshake naturally
	// >60s when idle) as "есть подозрения" while telling the user it was
	// within norm. Now aligned with the FSM HARD threshold so degraded only
	// fires when something is actually about to break.
	smartReplyDegradedHandshakeMinSec = 180
)

// ClassifyState applies the spec §5.2 decision tree:
//  1. report age > 5 min                                          → Offline
//  2. ≥1 active hard incident                                      → Hard
//  3. any tunnel handshake_age ≥ smartReplyDegradedHandshakeMinSec → Degraded
//  4. any tunnel pingCheck has fail_count > 0 (below thresh)       → Degraded
//  5. else                                                         → OK
func ClassifyState(a SmartReplyArgs) SmartReplyState {
	if a.LastReportAge > smartReplyOfflineThreshold {
		return StateOffline
	}
	if len(activeIncidentsForDisplay(a)) > 0 {
		return StateHard
	}
	for _, t := range a.Tunnels {
		if tunnelNeedsAttention(t) {
			return StateDegraded
		}
	}
	return StateOK
}

func tunnelNeedsAttention(t TunnelView) bool {
	if !tunnelEnabled(t) {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(t.Status))
	if status != "" && status != "running" {
		return true
	}
	if !tunnelHasHandshake(t) && (t.HasEnabled || status != "") {
		return true
	}
	if t.HandshakeAge >= smartReplyDegradedHandshakeMinSec {
		return true
	}
	return t.FailCount > 0 && (t.FailThresh == 0 || t.FailCount < t.FailThresh)
}

func tunnelHasHandshake(t TunnelView) bool {
	return t.HasHandshake || t.HandshakeAge > 0
}

func tunnelEnabled(t TunnelView) bool {
	return !t.HasEnabled || t.Enabled
}

func degradedTunnels(a SmartReplyArgs) []TunnelView {
	out := make([]TunnelView, 0, len(a.Tunnels))
	for _, t := range a.Tunnels {
		if tunnelNeedsAttention(t) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return a.Tunnels
	}
	return out
}

func activeIncidentsForDisplay(a SmartReplyArgs) []IncidentView {
	if len(a.ActiveIncidents) == 0 {
		return nil
	}
	out := make([]IncidentView, 0, len(a.ActiveIncidents))
	for _, inc := range a.ActiveIncidents {
		if isLegacyPingCheckDisabledFalseIncident(inc, a.Tunnels) {
			continue
		}
		out = append(out, inc)
	}
	return out
}

func isLegacyPingCheckDisabledFalseIncident(inc IncidentView, tunnels []TunnelView) bool {
	if !strings.HasPrefix(inc.CheckName, "tunnel_") {
		return false
	}
	for _, t := range tunnels {
		if t.CheckName != inc.CheckName {
			continue
		}
		pc := strings.ToLower(strings.TrimSpace(t.PingStatus))
		return t.HandshakeAge > 0 &&
			t.HandshakeAge < smartReplyDegradedHandshakeMinSec &&
			t.FailCount == 0 &&
			(pc == "disabled" || pc == "off" || pc == "inactive")
	}
	return false
}

func incidentDisplayName(inc IncidentView, tunnels []TunnelView) string {
	if !strings.HasPrefix(inc.CheckName, "tunnel_") {
		return categoryHeadline(inc.CheckName, inc.Details)
	}
	for _, t := range tunnels {
		if t.CheckName != inc.CheckName {
			continue
		}
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = strings.TrimSpace(t.Interface)
		}
		if name != "" && t.Interface != "" && name != t.Interface {
			return fmt.Sprintf("%s (%s)", name, t.Interface)
		}
		if name != "" {
			return name
		}
	}
	return inc.CheckName
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
	visibleIncidents := activeIncidentsForDisplay(a)

	var b strings.Builder
	switch state {
	case StateOK:
		fmt.Fprintf(&b, "✅ %s — всё работает.\n\n", a.Nickname)
		for _, t := range a.Tunnels {
			if !tunnelEnabled(t) {
				fmt.Fprintf(&b, "Туннель %s: выключен.\n", t.Name)
				continue
			}
			line := fmt.Sprintf("Туннель %s: последний обмен ключами %s назад", t.Name, humanAgeSec(t.HandshakeAge))
			if t.PingStatus != "" {
				line += fmt.Sprintf(", проверка связи: %s", humanPingStatus(t.PingStatus))
			}
			if t.Latency > 0 {
				line += fmt.Sprintf(" (%d мс)", t.Latency)
			}
			b.WriteString(line + ".\n")
		}
		fmt.Fprintf(&b, "Роутер последний раз отчитывался: %s назад.", humanAgeDur(a.LastReportAge))
		appendUpdatesSection(&b, a.Updates)
		return b.String(), tg.InlineKeyboardMarkup{}

	case StateDegraded:
		fmt.Fprintf(&b, "⚠️ %s — есть подозрения.\n\n", a.Nickname)
		problems := degradedTunnels(a)
		for _, t := range problems {
			fmt.Fprintf(&b, "Туннель %s: ", t.Name)
			status := strings.TrimSpace(t.Status)
			switch {
			case status != "" && !strings.EqualFold(status, "running"):
				fmt.Fprintf(&b, "состояние %s", status)
				if !tunnelHasHandshake(t) {
					b.WriteString(", обмена ключами ещё не было")
				}
				b.WriteString(".\n")
			case !tunnelHasHandshake(t):
				b.WriteString("обмена ключами ещё не было.\n")
			default:
				fmt.Fprintf(&b, "обмена ключами не было %d сек (норма до 180).\n", t.HandshakeAge)
			}
			if t.FailCount > 0 {
				fmt.Fprintf(&b, "Проверка связи: %d неудачи подряд из %d.\n", t.FailCount, t.FailThresh)
			}
		}
		b.WriteString("Сейчас это не показываем как красный алерт, но внимание нужно.\n\nДействия:")
		var rows [][]tg.InlineKeyboardButton
		for _, t := range problems {
			label := "🔁 Перезапустить туннель"
			if len(problems) > 1 {
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
		neighbors := tunnelViewsAsNeighbors(a.Tunnels)
		for _, inc := range visibleIncidents {
			age := time.Since(inc.HardSince).Round(time.Minute)
			if checkCategory(inc.CheckName) == "dns" && len(inc.Details) > 0 {
				fmt.Fprintf(&b, "%s уже %s.\n", categoryHeadline(inc.CheckName, inc.Details), durFmt(age))
				for _, line := range dnsSmartReplyLines(inc.Details, neighbors) {
					b.WriteString(line + "\n")
				}
				continue
			}
			fmt.Fprintf(&b, "%s не отвечает уже %s.\n", incidentDisplayName(inc, a.Tunnels), durFmt(age))
		}
		b.WriteString("\nЧто можно сделать:")
		for _, line := range smartReplyActionHints(visibleIncidents) {
			b.WriteString("\n  • " + line)
		}
		var rows [][]tg.InlineKeyboardButton
		seen := map[string]bool{}
		// Buttons per active incident (carries silence button)
		for _, inc := range visibleIncidents {
			if !strings.HasPrefix(inc.CheckName, "tunnel_") {
				rows = append(rows, []tg.InlineKeyboardButton{
					{Text: "⏸ Тише на час", CallbackData: silenceCD(inc.CheckName, "1h")},
					{Text: "📋 История", CallbackData: plainCD("history", inc.CheckName)},
				})
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

func tunnelViewsAsNeighbors(tunnels []TunnelView) []NeighborSummary {
	out := make([]NeighborSummary, 0, len(tunnels))
	for _, t := range tunnels {
		out = append(out, NeighborSummary{
			CheckName:    t.CheckName,
			TunnelName:   t.Name,
			NDMSName:     t.NDMSName,
			Interface:    t.Interface,
			Status:       t.PingStatus,
			HandshakeAge: t.HandshakeAge,
		})
	}
	return out
}

func dnsSmartReplyLines(d map[string]any, ns []NeighborSummary) []string {
	total, _ := intOrZero(d, "endpoints")
	failed, _ := intOrZero(d, "failed_count")
	var lines []string
	if total > 0 && failed > 0 {
		lines = append(lines, fmt.Sprintf("%d из %d DNS-серверов не отвечают.", failed, total))
	}
	if label := singleFailedDNSTunnelLabel(d, ns); label != "" {
		lines = append(lines, "Падают через: "+label+".")
	}
	rknProbed, _ := intOrZero(d, "rkn_probed")
	rknSus, _ := intOrZero(d, "rkn_suspect")
	if rknProbed > 0 {
		if rknSus == 0 {
			lines = append(lines, "RKN-блокировок не видно.")
		} else {
			lines = append(lines, fmt.Sprintf("RKN-подозрение на %d из %d проверок.", rknSus, rknProbed))
		}
	}
	return lines
}

func smartReplyActionHints(incidents []IncidentView) []string {
	if len(incidents) == 0 {
		return []string{"Запусти 🩺 Проверку, чтобы обновить диагностику перед ручными правками."}
	}
	seen := map[string]bool{}
	var lines []string
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		lines = append(lines, s)
	}
	for _, inc := range incidents {
		switch {
		case checkCategory(inc.CheckName) == "dns":
			add("Открой 🎛 Туннели и проверь живой туннель/интерфейс из строки выше.")
			add("Если туннель выключен или удалён, затем 🛣 Маршруты: перенеси DNS/HR-Neo правила на живой туннель.")
		case strings.HasPrefix(inc.CheckName, "tunnel_"):
			add("Для туннеля ниже можно запустить перезапуск или диагностику; если он больше не нужен — открой 🎛 Туннели.")
		case inc.CheckName == "hydraroute":
			add("Открой 🛣 Маршруты: проверь HR-Neo и правила, которые завязаны на него.")
		default:
			add("Запусти 🩺 Проверку, чтобы обновить диагностику перед ручными правками.")
		}
	}
	return lines
}

func singleFailedDNSTunnelLabel(d map[string]any, ns []NeighborSummary) string {
	var ndms string
	for _, ep := range mapsSlice(d, "endpoints_detail") {
		reachable, _ := ep["reachable"].(bool)
		if reachable {
			continue
		}
		cur, _ := ep["ndms_name"].(string)
		if cur == "" {
			continue
		}
		if ndms == "" {
			ndms = cur
			continue
		}
		if ndms != cur {
			return ""
		}
	}
	if ndms == "" {
		return ""
	}
	return humanTunnelLabelByNDMS(ndms, ns)
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
		if u.Hint != "" {
			fmt.Fprintf(b, "    подсказка: %s\n", u.Hint)
		}
	}
}
