package alerts

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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

	if action == "router_doctor" && r.Status != "ok" && strings.TrimSpace(r.Output) != "" {
		return formatPlainCommandOutput(label, "есть проблемы", r.Output, maxChars)
	}

	if r.Status != "ok" {
		var summary, hint string
		if alertButtonActions[action] {
			summary, hint = ownerCommandFailure(r.Status, r.Output)
		} else {
			token := strings.ToUpper(r.Status) // "ERR" / "LOCKED" / "TIMEOUT"
			hintInput := token
			if r.Status == "err" {
				hintInput = r.Output
			}
			summary, hint = HintFor(action, hintInput)
		}
		card := Card{Badge: "❌", Label: label, Summary: summary, Hint: hint}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	}

	switch action {
	case "diag_now":
		return formatDiagSuccess(label, r.Output, maxChars)
	case "pingcheck_now":
		summary := humanPingcheckResult(r.Output)
		card := Card{
			Badge:   "",
			Label:   label,
			Summary: fmt.Sprintf("%s (за %dмс)", summary, r.DurationMs),
		}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "force_recheck":
		// Агент отвечает «agent report kicked»: отчёт уже в пути, а слова --
		// для лога.
		card := Card{Badge: "", Label: label, Summary: "роутер пришлёт свежий отчёт в ближайшие секунды"}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "tunnel_enable", "tunnel_disable":
		// Agent emits "interface <ndms> -> <up|down>\n<ndmc stdout>" — ndmc
		// stdout is noisy (NDMS::* internal logs) and the "interface X -> up"
		// line is engineer-speak. Lift only the ndms name and render a clean
		// Russian one-liner; drop the rest.
		ndms := parseInterfaceLine(r.Output)
		verb := "включён"
		if action == "tunnel_disable" {
			verb = "выключен"
		}
		summary := verb
		if ndms != "" {
			summary = ndms + " → " + verb
		}
		card := Card{Badge: "", Label: label, Summary: summary}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "tunnel_delete":
		tunnelID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.Output), "tunnel "))
		tunnelID = strings.TrimSuffix(tunnelID, " deleted")
		summary := "туннель удалён"
		if tunnelID != "" && tunnelID != r.Output {
			summary = tunnelID + " → удалён"
		}
		card := Card{Badge: "", Label: label, Summary: summary}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "restart_tunnel", "tunnel_restart":
		card := Card{Badge: "", Label: label, Summary: humanRestartResult(r.Output)}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "tunnel_import":
		summary := "готово"
		if r.DurationMs > 0 {
			summary = "готово за " + commandResultDuration(r.DurationMs)
		}
		card := Card{
			Badge:   "",
			Label:   label,
			Summary: summary,
			Details: strings.TrimSpace(r.Output),
			Hint:    "Если это новый туннель и на старом были правила, открой 🛣 Маршруты, чтобы перенести правила на новый конфиг.",
		}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "check_via_tunnel", "check_direct", "router_doctor":
		return formatPlainCommandOutput(label, "готово", r.Output, maxChars)
	case "opkg_upgrade":
		full := Card{
			Badge:   "",
			Label:   label,
			Summary: "готово",
			Details: strings.TrimSpace(r.Output),
		}.Render(CardOpts{})
		if len(full) <= maxChars {
			return []string{full}
		}
		return paginate(label+": лог", r.Output, maxChars)
	}
	full := Card{
		Badge:   "",
		Label:   label,
		Summary: strings.TrimSpace(r.Output),
	}.Render(CardOpts{})
	if len(full) <= maxChars {
		return []string{full}
	}
	return paginate(label+":", r.Output, maxChars)
}

func humanPingcheckResult(output string) string {
	out := strings.TrimSpace(output)
	low := strings.ToLower(out)
	switch {
	case strings.HasPrefix(low, "alive "):
		lat := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(low, "alive"), " "))
		lat = strings.ReplaceAll(lat, " ms", " мс")
		return "связь живая, " + lat
	case low == "alive":
		return "связь живая"
	case strings.Contains(low, "triggered"):
		// Нынешний агент не ждёт результата: проверку он только запускает
		// («pingcheck-now triggered»), а итог приходит с отчётом.
		return "проверка связи запущена — результат придёт со следующим отчётом роутера"
	case strings.HasPrefix(low, "dead"):
		return "связь не проходит"
	case out == "":
		return "проверка завершилась без подробностей"
	default:
		return out
	}
}

func humanRestartResult(output string) string {
	out := strings.TrimSpace(output)
	low := strings.ToLower(out)
	if strings.HasPrefix(low, "restarted ") {
		target := strings.TrimSpace(out[len("restarted "):])
		if i := strings.IndexByte(target, '\n'); i >= 0 {
			target = strings.TrimSpace(target[:i])
		}
		return "VPN-туннель перезапущен: " + target
	}
	if out == "" {
		return "команда выполнена"
	}
	if low == "все туннели перезапущены" {
		return "все VPN-туннели перезапущены"
	}
	return out
}

// alertButtonActions -- действия кнопок под тревогой и отчётом о
// пробуждении. Ответ уходит в чат, где нажали, то есть владельцу в личку,
// поэтому их ошибки говорят без советов админской панели (ssh, пути на
// роутере, lock-файлы). Админ получает ту же фразу -- подробности у него в
// логе агента.
var alertButtonActions = map[string]bool{
	"restart_tunnel": true,
	"diag_now":       true,
	"pingcheck_now":  true,
	"force_recheck":  true,
}

// ownerCommandFailure -- отказ кнопки словами владельца: что случилось и
// что он может сделать сам. Смысл, который владельцу полезен (отчёт ещё не
// готов, диагностика не уложилась), сохраняется; инженерные подробности --
// нет.
func ownerCommandFailure(status, output string) (summary, hint string) {
	const retry = "Повторите через минуту. Если не поможет — откройте приложение: там видно, что с роутером."
	low := strings.ToLower(output)
	switch {
	case strings.Contains(output, "NO_REPORT") || strings.Contains(low, "no report available"):
		return "отчёт ещё не готов", "Повторите через минуту — роутер не успел его подготовить."
	case strings.Contains(output, "DIAG_TIMEOUT"):
		return "диагностика не уложилась в 36 с",
			"Роутер начал собирать отчёт, но не успел. Повторите — обычно это занимает от полуминуты до минуты."
	case strings.Contains(output, "DIAG_STREAM_ERROR"):
		return "роутер не довёл проверку до конца", "Попробуйте ещё раз через минуту."
	case status == "timeout":
		return "роутер не уложился в отведённое время", retry
	case status == "locked":
		return "на роутере идёт другая операция", "Подождите минуту и повторите."
	case strings.Contains(output, "HTTP_401") || strings.Contains(output, "HTTP_403") || strings.Contains(low, "unauthorized"):
		return "роутер не пускает агента", "Напишите тому, кто настраивал роутер: агенту нужно заново выдать доступ."
	case strings.Contains(output, "HTTP_5") || strings.Contains(output, "HTTP_REFUSED") ||
		strings.Contains(low, "connection refused") || strings.Contains(low, "dial tcp") || strings.Contains(low, "not configured"):
		return "панель роутера не отвечает", retry
	default:
		return "роутер ответил ошибкой", retry
	}
}

func formatPlainCommandOutput(label, summary, output string, maxChars int) []string {
	output = strings.TrimSpace(output)
	card := Card{
		Badge:   "",
		Label:   label,
		Summary: summary,
		Details: output,
	}
	full := card.Render(CardOpts{})
	if len(full) <= maxChars {
		return []string{full}
	}
	return paginate(label+":", output, maxChars)
}

// formatDiagSuccess parses the awg-manager JSON report into a Card.
// On parse failure, falls back to code-fenced raw dump with pagination.
func formatDiagSuccess(label, body string, maxChars int) []string {
	summary, bullets, fallback := ParseDiagReport(body)
	if fallback {
		// Sanitize: raw body could itself contain literal triple-backticks
		// (awg-manager error messages occasionally embed code snippets). If
		// we wrap such a body in our own fence verbatim, TG MarkdownV2 sees
		// the inner ``` as a fence-terminator and the rest leaks as plain
		// text — partial code-snippet formatting + broken safety wrapping.
		safe := strings.ReplaceAll(body, "```", "'''")
		full := fmt.Sprintf("%s:\n\n```\n%s\n```", label, safe)
		if len(full) <= maxChars {
			return []string{full}
		}
		return paginate(label+":", safe, maxChars)
	}
	card := Card{
		Badge:   "",
		Label:   label,
		Summary: summary,
		Details: strings.Join(bullets, "\n"),
		Hint:    "Подробности по каждой проблеме — кнопками ниже. Полный отчёт — для того, кто настраивал роутер.",
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

// parseInterfaceLine extracts the ndms name from the agent's tunnel toggle
// output. The first line follows the shape "interface <ndms> -> <state>" —
// anything else (blank, ndmc-only) yields "". Defensive: we never panic on
// unexpected agent output, we just degrade to a verb-only summary.
func parseInterfaceLine(output string) string {
	first := output
	if i := strings.IndexByte(output, '\n'); i >= 0 {
		first = output[:i]
	}
	first = strings.TrimSpace(first)
	const prefix = "interface "
	if !strings.HasPrefix(first, prefix) {
		if first != "" {
			slog.Debug("parseInterfaceLine: unexpected agent output shape; degrading to verb-only summary", "first_line", first)
		}
		return ""
	}
	rest := first[len(prefix):]
	if i := strings.Index(rest, " -> "); i >= 0 {
		return strings.TrimSpace(rest[:i])
	}
	slog.Debug("parseInterfaceLine: prefix matched but separator missing", "first_line", first)
	return ""
}

func commandLabelHuman(action string) string {
	switch action {
	case "diag_now":
		return "📊 Диагностика"
	case "pingcheck_now":
		return "▶ Тест связи"
	case "restart_tunnel":
		return "🔁 Перезапуск VPN-туннелей"
	case "tunnel_restart":
		return "🔁 Перезапуск туннеля"
	case "opkg_upgrade":
		return "⬆ Обновление пакетов"
	case "force_recheck":
		return "🔄 Запрос отчёта"
	case "check_via_tunnel":
		return "🌍 Через туннель"
	case "check_direct":
		return "🇷🇺 Напрямую"
	case "router_doctor":
		return "🩺 Проверка роутера"
	case "tunnel_enable":
		return "▶ Включить туннель"
	case "tunnel_disable":
		return "⏸ Выключить туннель"
	case "tunnel_delete":
		return "🗑 Удалить туннель"
	case "tunnel_import":
		return "📁 Импорт конфига"
	}
	return action
}

func commandResultDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dмс", ms)
	}
	sec := (ms + 500) / 1000
	if sec < 60 {
		return fmt.Sprintf("%dс", sec)
	}
	return fmt.Sprintf("%dм %02dс", sec/60, sec%60)
}
