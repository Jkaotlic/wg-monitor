package alerts

import (
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const tgMaxMessageBytes = 4096

// FormatCommandResult renders a wire.CommandResult as one or more TG message
// bodies (chunks). Caller sends each chunk in sequence, each replying to the
// previous. maxChars is a hint for the per-chunk soft cap (raw TG limit is
// 4096; default callers pass 3500-4000 to leave headroom for code-fence and
// chunk-prefix markup).
//
// Per spec §6.5:
//   - diag_now      → wraps Output in code-fence, label "📊 Диагностика"
//   - opkg_upgrade  → label "⬆ Обновление пакетов", paginated (1/N) prefix
//   - pingcheck_now → label "▶ Тест связи", one-liner with duration
//   - restart_tunnel→ label "🔁 Перезапуск туннеля"
//   - other actions → action name as label (defensive)
//
// Status ∈ {err, locked, timeout} prepends "❌ Не удалось:" before the body.
func FormatCommandResult(action string, r wire.CommandResult, maxChars int) []string {
	if maxChars <= 0 || maxChars > tgMaxMessageBytes {
		maxChars = tgMaxMessageBytes - 200
	}
	label := commandLabelHuman(action)
	header := label
	body := r.Output
	switch r.Status {
	case "err":
		body = "❌ Не удалось: " + body
	case "locked":
		body = "❌ Не удалось: locked (другая операция держит lock-файл)"
	case "timeout":
		body = "❌ Не удалось: timeout (агент не уложился в лимит)"
	}

	switch action {
	case "pingcheck_now":
		return []string{fmt.Sprintf("%s: %s (за %dмс)", header, strings.TrimSpace(body), r.DurationMs)}
	case "restart_tunnel", "tunnel_enable", "tunnel_disable":
		return []string{fmt.Sprintf("%s: %s", header, strings.TrimSpace(body))}
	case "check_via_tunnel", "check_direct":
		// Body is already a human-readable multi-line report from
		// actions.CheckViaTunnel/CheckDirect. Don't double-prefix or
		// wrap in code fences — just pass it through.
		return []string{strings.TrimSpace(body)}
	case "diag_now":
		full := fmt.Sprintf("%s:\n\n```\n%s\n```", header, body)
		if len(full) <= maxChars {
			return []string{full}
		}
		// Diag too large — paginate the raw body (without code fences per chunk).
		return paginate(header+":", body, maxChars)
	case "opkg_upgrade":
		// SmartUpgrade returns a human-readable multi-line summary; no
		// code-fence wrapping needed. Just paginate if it overflows.
		full := fmt.Sprintf("%s:\n\n%s", header, body)
		if len(full) <= maxChars {
			return []string{full}
		}
		return paginate(header+":", body, maxChars)
	}
	full := fmt.Sprintf("%s: %s", header, body)
	if len(full) <= maxChars {
		return []string{full}
	}
	return paginate(header+":", body, maxChars)
}

// paginate splits body into chunks each prefixed with "(K/N) <header>".
// The header is repeated on every chunk for context. K and N are 1-based.
// Each rendered chunk is hard-capped at tgMaxMessageBytes (4096) regardless
// of maxChars: a defensive truncation runs after rendering to guarantee TG
// will never reject the message for length, even if the caller passed an
// over-aggressive maxChars (or if a multi-byte UTF-8 header consumed more
// of the budget than expected).
func paginate(header, body string, maxChars int) []string {
	per := maxChars
	if per < 100 {
		per = 100
	}
	// Rune-aware split: byte-indexing раньше резало multi-byte UTF-8 (русский
	// в opkg_upgrade output / hostname с кириллицей) на половине символа,
	// и Telegram возвращал не валидный текст (BUG-09). Шагаем по rune'ам,
	// конвертируем в []byte у границ.
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
			// Trim by RUNES (не bytes) until под лимитом — тот же UTF-8
			// safety contract на уровне defensive truncation.
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
