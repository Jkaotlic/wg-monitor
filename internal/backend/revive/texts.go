package revive

import "github.com/Jkaotlic/wg-monitor/internal/awgmstate"

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
	default:
		return "роутер не отвечает"
	}
}
