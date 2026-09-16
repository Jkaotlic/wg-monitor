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

func noticeRevived(nick, version string) string {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" {
		return fmt.Sprintf("✅ Роутер «%s»: агент ожил. Пароль стёрт с сервера.", nick)
	}
	return fmt.Sprintf("✅ Роутер «%s»: агент ожил, версия «%s». Пароль стёрт с сервера.", nick, v)
}

func noticeAliveItself(nick string) string {
	return fmt.Sprintf("✅ Роутер «%s»: агент снова на связи сам, переустанавливать не пришлось. Оживление снято, пароль стёрт с сервера.", nick)
}

func noticeAuthFailed(nick string) string {
	return fmt.Sprintf("⚠️ Роутер «%s»: пароль не подошёл — поставьте оживление заново в приложении, экран «Парк». Введённый пароль стёрт с сервера.", nick)
}

func noticeFailed(nick, reason string) string {
	return fmt.Sprintf("⚠️ Роутер «%s»: оживить агент не вышло — %s. Пароль стёрт с сервера; %s.", nick, reason, noticeAgainHint)
}

func noticeGaveUp(nick string, attempts int, reason string) string {
	return fmt.Sprintf("⚠️ Роутер «%s»: оживить агент не вышло (попыток: %d) — %s. Пароль стёрт с сервера; %s.",
		nick, attempts, reason, noticeAgainHint)
}

func noticeExpired(nick string, expiresAt time.Time) string {
	return fmt.Sprintf("⌛ Роутер «%s» так и не появился до %s — оживление снято, пароль стёрт с сервера.",
		nick, expiresAt.UTC().Format("02.01.2006"))
}
