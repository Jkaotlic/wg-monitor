package alerts

import (
	"strings"
)

// HintFor maps a raw error string + action name to a user-friendly
// (summary, hint) pair, both in Russian. statusOrRaw is either:
//   - a CommandResult.Status token: "TIMEOUT" / "LOCKED"
//   - the agent's typed error prefix: "NO_REPORT" / "DIAG_TIMEOUT" /
//     "HTTP_NNN" / "HTTP_REFUSED"
//   - a raw error message (DB / SSH / network) for admin paths
//
// Match order: substring tests, most-specific first; the default
// branch is the safety net. Typed tokens are uppercase by contract.
func HintFor(action, statusOrRaw string) (summary, hint string) {
	s := statusOrRaw

	// PingCheck-specific hints (action-aware)
	if action == "pingcheck_status" {
		switch {
		case strings.Contains(s, "HTTP_REFUSED"):
			return "awg-manager недоступен",
				"Проверь что сервис awg-manager работает: `/opt/etc/init.d/S99awg-manager status`"
		case strings.Contains(s, "HTTP_5"):
			return "awg-manager отдал серверную ошибку",
				"Логи: `/opt/var/log/awg-manager.log`"
		case s == "":
			return "не удалось прочитать состояние PingCheck",
				"Попробуй ещё раз через 5 сек."
		}
	}
	if action == "pingcheck_toggle" {
		switch {
		case strings.Contains(s, "HTTP_REFUSED"):
			return "awg-manager и ndmc недоступны",
				"Проверь сервис awg-manager и доступ к ndmc."
		case strings.Contains(s, "interface unknown"):
			return "NDMS не знает интерфейс",
				"NDMS не знает интерфейс. Проверь имя интерфейса в awg-mgr → `ndmc -c \"show interface\"`."
		case s == "":
			return "переключение не применилось",
				"Посмотри текст ошибки выше или повтори действие после обновления панели."
		}
	}

	// Generic hints (action-independent)
	switch {
	case strings.Contains(s, "NO_REPORT") || strings.Contains(s, "no report available"):
		return "отчёт ещё не сформирован",
			"Запусти ещё раз — awg-manager не успел подготовить отчёт."
	case strings.Contains(s, "DIAG_TIMEOUT"):
		return "диагностика не уложилась в 36с",
			"awg-manager запустил отчёт, но не успел его собрать за 36с. " +
				"Попробуй ещё раз — обычно это занимает 30–60с."
	case strings.Contains(s, "HTTP_502") || strings.Contains(s, "HTTP_503"):
		return "awg-manager недоступен",
			"Зайди по SSH и выполни: `/opt/etc/init.d/S99awg-manager status`. " +
				"Если упал — `/opt/etc/init.d/S99awg-manager restart`."
	case strings.Contains(s, "HTTP_401") || strings.Contains(s, "HTTP_403"):
		return "awg-manager не пускает агент",
			"Токен агента устарел или права изменились. В wizard: " +
				"«📦 Установить агент» переустановит токен на роутере."
	case strings.Contains(s, "connection refused") ||
		strings.Contains(s, "HTTP_REFUSED") ||
		strings.Contains(s, "dial tcp"):
		return "агент не достучался до awg-manager",
			"awg-manager не слушает порт 2222. `netstat -tln | grep 2222` " +
				"на роутере покажет, поднят ли он."
	case s == "TIMEOUT" || strings.Contains(s, "timeout (агент"):
		return "агент не уложился в лимит",
			"Роутер занят (CPU/диск). Подожди минуту; если повторится — " +
				"`top` + `logread` диагностируют причину."
	case s == "LOCKED" || strings.Contains(s, "locked ("):
		return "другая операция держит lock",
			"Подожди ~30с — параллельная команда ещё не отпустила lock-файл. " +
				"Если зависло > 2 минут — попроси админа: `rm /opt/var/run/wg-monitor.lock`."
	case strings.Contains(s, "database is locked"):
		return "SQLite занят",
			"Это transient. Подожди 1–2 секунды и повтори."
	}
	return "что-то пошло не так",
		"Деталь: `" + sanitizeRaw(s, 200) + "`. Покажи админу или попробуй ещё раз через минуту."
}

// sanitizeRaw cuts raw error text to first line, runes ≤ maxLen, no
// triple-backticks (so the hint can safely sit inside a Card without
// breaking caller-side markdown fences).
func sanitizeRaw(raw string, maxLen int) string {
	if i := strings.IndexByte(raw, '\n'); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.ReplaceAll(raw, "```", "'''")
	runes := []rune(raw)
	if len(runes) > maxLen {
		runes = runes[:maxLen]
	}
	return strings.TrimSpace(string(runes))
}
