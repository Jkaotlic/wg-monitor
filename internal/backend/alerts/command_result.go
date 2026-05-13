package alerts

import (
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/pkg/wire"
)

const tgMaxMessageBytes = 4096

// FormatCommandResult renders a wire.CommandResult as one or more TG
// message bodies (chunks). Errors flow through HintFor; the diag_now
// success path goes through ParseDiagReport with a code-fence fallback
// for unrecognised JSON shapes.
func FormatCommandResult(action string, r wire.CommandResult, maxChars int) []string {
	if maxChars <= 0 || maxChars > tgMaxMessageBytes {
		maxChars = tgMaxMessageBytes - 200
	}
	label := commandLabelHuman(action)

	if r.Status != "ok" {
		token := strings.ToUpper(r.Status) // "ERR" / "LOCKED" / "TIMEOUT"
		hintInput := token
		if r.Status == "err" {
			hintInput = r.Output
		}
		summary, hint := HintFor(action, hintInput)
		card := Card{Badge: "❌", Label: label, Summary: summary, Hint: hint}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	}

	switch action {
	case "diag_now":
		return formatDiagSuccess(label, r.Output, maxChars)
	case "pingcheck_now":
		summary := strings.TrimSpace(r.Output)
		card := Card{
			Badge:   "",
			Label:   label,
			Summary: fmt.Sprintf("%s (за %dмс)", summary, r.DurationMs),
		}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "restart_tunnel", "tunnel_enable", "tunnel_disable":
		card := Card{Badge: "", Label: label, Summary: strings.TrimSpace(r.Output)}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "check_via_tunnel", "check_direct":
		return []string{strings.TrimSpace(r.Output)}
	case "opkg_upgrade":
		full := fmt.Sprintf("%s:\n\n%s", label, r.Output)
		if len(full) <= maxChars {
			return []string{full}
		}
		return paginate(label+":", r.Output, maxChars)
	}
	full := fmt.Sprintf("%s: %s", label, r.Output)
	if len(full) <= maxChars {
		return []string{full}
	}
	return paginate(label+":", r.Output, maxChars)
}

// formatDiagSuccess parses the awg-manager JSON report into a Card.
// On parse failure, falls back to code-fenced raw dump with pagination.
func formatDiagSuccess(label, body string, maxChars int) []string {
	summary, bullets, fallback := ParseDiagReport(body)
	if fallback {
		full := fmt.Sprintf("%s:\n\n```\n%s\n```", label, body)
		if len(full) <= maxChars {
			return []string{full}
		}
		return paginate(label+":", body, maxChars)
	}
	card := Card{
		Badge:   "",
		Label:   label,
		Summary: summary,
		Details: strings.Join(bullets, "\n"),
		Hint:    "Полный JSON-отчёт доступен по кнопке ниже.",
	}
	return []string{card.Render(CardOpts{MaxBytes: maxChars})}
}

// paginate splits body into chunks each prefixed with "(K/N) <header>".
// Each rendered chunk is hard-capped at tgMaxMessageBytes (4096) regardless
// of maxChars: defensive truncation after rendering guarantees TG won't
// reject the message for length.
func paginate(header, body string, maxChars int) []string {
	per := maxChars
	if per < 100 {
		per = 100
	}
	runes := []rune(body)
	var chunks []string
	if len(runes) == 0 {
		chunks = []string{""}
	} else {
		for i := 0; i < len(runes); i += per {
			end := i + per
			if end > len(runes) {
				end = len(runes)
			}
			chunks = append(chunks, string(runes[i:end]))
		}
	}
	out := make([]string, len(chunks))
	for i, c := range chunks {
		rendered := fmt.Sprintf("(%d/%d) %s\n%s", i+1, len(chunks), header, c)
		if len(rendered) > tgMaxMessageBytes {
			rr := []rune(rendered)
			for len(string(rr)) > tgMaxMessageBytes && len(rr) > 0 {
				rr = rr[:len(rr)-1]
			}
			rendered = string(rr)
		}
		out[i] = rendered
	}
	return out
}

func commandLabelHuman(action string) string {
	switch action {
	case "diag_now":
		return "📊 Диагностика"
	case "pingcheck_now":
		return "▶ Тест связи"
	case "restart_tunnel":
		return "🔁 Перезапуск туннеля"
	case "opkg_upgrade":
		return "⬆ Обновление пакетов"
	case "force_recheck":
		return "🔁 Force recheck"
	case "check_via_tunnel":
		return "🌍 Через тоннель"
	case "check_direct":
		return "🇷🇺 Напрямую"
	case "tunnel_enable":
		return "▶ Включить туннель"
	case "tunnel_disable":
		return "⏸ Выключить туннель"
	}
	return action
}
