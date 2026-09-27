package revive

import (
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
)

// probeText -- итог последнего опроса словами для экрана «Парк».
func probeText(state string) string {
	switch state {
	case "":
		return ""
	case awgmstate.Reachable:
		return "роутер отвечает"
	case awgmstate.TLSError:
		return "сертификат панели роутера не подошёл"
	case awgmstate.DNSError:
		return "адрес панели роутера не находится"
	case awgmstate.AuthError:
		return "панель роутера отказала во входе"
	case ProbeInvalidURL:
		return "адрес панели неверный"
	default:
		return "роутер не отвечает"
	}
}

// Причины -- то, что лежит в revive_intents.last_error и уходит на экран как
// last_error_text. Русские, без секретов и внутренних имён.
const (
	reasonRevived         = "агент ожил"
	reasonAliveItself     = "агент снова на связи сам"
	reasonAuthFailed      = "пароль не подошёл"
	reasonPanelAuthFailed = "вход в панель роутера не подошёл"
	reasonExpired         = "срок оживления вышел"
	reasonNoStoredEntry   = "пароль на сервере не найден — поставьте оживление заново"
	reasonEntryUnreadable = "пароль на сервере не расшифровывается — поставьте оживление заново"
	reasonNoAWGMURL       = "у роутера нет адреса панели"
	reasonUnsafeAWGMURL   = "адрес панели небезопасный (нужен https и внешнее имя) — поменяйте его в веб-дашборде и поставьте оживление заново"
	reasonLaunchFailed    = "переустановка не запустилась"
	reasonJobLost         = "переустановка прервалась: сервер перезапускался"
	reasonUnknownFailure  = "переустановка не удалась"
)

const noticeAgainHint = "поставить заново можно в приложении, экран «Парк»"

// passwordNote -- что с паролем root на сервере после закрытия намерения
// (REV-01). Секрет намерения стирается всегда, но с v0.45 пароль может
// храниться в router_credentials для авто-оживления -- тогда «стёрт» было бы
// ложью.
func passwordNote(stored bool) string {
	if stored {
		return "Пароль хранится на сервере зашифрованным — для следующего оживления."
	}
	return "Пароль стёрт с сервера."
}

func noticeRevived(nick, version string, stored bool) string {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" {
		return fmt.Sprintf("✅ Роутер «%s»: агент ожил. %s", nick, passwordNote(stored))
	}
	return fmt.Sprintf("✅ Роутер «%s»: агент ожил, версия «%s». %s", nick, v, passwordNote(stored))
}

func noticeAliveItself(nick string, stored bool) string {
	return fmt.Sprintf("✅ Роутер «%s»: агент снова на связи сам, переустанавливать не пришлось. Оживление снято. %s", nick, passwordNote(stored))
}

// noticeAuthFailed -- введённый пароль не подошёл: секрет намерения стёрт, а
// сохранённый пароль, если он тот же, стёрт forgetRejectedCredentials.
func noticeAuthFailed(nick string) string {
	return fmt.Sprintf("⚠️ Роутер «%s»: пароль не подошёл — поставьте оживление заново в приложении, экран «Парк». Введённый пароль стёрт с сервера.", nick)
}

func noticeFailed(nick, reason string, stored bool) string {
	return fmt.Sprintf("⚠️ Роутер «%s»: оживить агент не вышло — %s. %s %s.", nick, reason, passwordNote(stored), capitalize(noticeAgainHint))
}

func noticeGaveUp(nick string, attempts int, reason string, stored bool) string {
	return fmt.Sprintf("⚠️ Роутер «%s»: оживить агент не вышло (попыток: %d) — %s. %s %s.",
		nick, attempts, reason, passwordNote(stored), capitalize(noticeAgainHint))
}

func noticeExpired(nick string, expiresAt time.Time, stored bool) string {
	return fmt.Sprintf("⌛ Роутер «%s» так и не появился до %s — оживление снято. %s",
		nick, expiresAt.UTC().Format("02.01.2006"), passwordNote(stored))
}

func capitalize(s string) string {
	for i, r := range s {
		return strings.ToUpper(string(r)) + s[i+len(string(r)):]
	}
	return s
}
