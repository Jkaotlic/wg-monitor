package actions

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// tunnelPowerSettle -- сколько tunnel_power ждёт, пока туннель дойдёт до
// нужного состояния (AG-02), и шаг перечитывания. Внутри срока действия по
// умолчанию (45 с): два-три вызова awg-manager по 5 с и 15 с ожидания.
// Переменные -- для теста.
var (
	tunnelPowerSettle = 15 * time.Second
	tunnelPowerStep   = time.Second
)

// tunnelPower включает или выключает туннель по id и отвечает по факту.
//
// awg-manager: /api/control/stop ставит enabled=false, а /api/control/start
// выключенный туннель не поднимает (живая сверка 11.09.2026). Поэтому on --
// прочитать туннель, выключенный включить (toggle-enabled -- переключатель:
// зовётся только по свежему enabled=false), затем start. После -- перечитывать
// до tunnelPowerSettle: «запущен» -- только увидев running, «остановлен» --
// только увидев остановленный. Не дошёл -- partial с настоящим состоянием.
func (r *Runner) tunnelPower(ctx context.Context, tunnelID string, on bool) (status, output string) {
	// name -- имя туннеля для ответа владельцу (M-2): мини-апп показывает
	// «не подтвердилось» дословно, id и текст ошибки там не к месту.
	var name string
	if on {
		// Чтение не вышло -- запускаем как раньше: правду скажет перечитывание.
		cur, err := getTunnel(ctx, r.AwgClient, tunnelID)
		if err == nil {
			name = cur.Name
		}
		if err == nil && !cur.Enabled {
			if err := r.AwgClient.ToggleEnabled(ctx, tunnelID); err != nil {
				return "err", fmt.Sprintf("tunnel_power %s: enable: %v", tunnelID, err)
			}
		}
		if err := r.AwgClient.StartTunnel(ctx, tunnelID); err != nil {
			return "err", fmt.Sprintf("tunnel_power %s: %v", tunnelID, err)
		}
	} else if err := r.AwgClient.StopTunnel(ctx, tunnelID); err != nil {
		return "err", fmt.Sprintf("tunnel_power %s: %v", tunnelID, err)
	}

	reached := func(t *awgmgr.Tunnel) bool {
		st := tunnelRuntimeState(t)
		if on {
			return st == "running"
		}
		switch st {
		case "stopped", "disabled", "not_created":
			return true
		}
		return false
	}
	var (
		last    *awgmgr.Tunnel
		lastErr error
	)
	// Срок ожидания -- по часам: чтение у awg-manager до 5 с, и счёт
	// попыток растянул бы ожидание за срок действия (45 с), а итог стал бы
	// timeout вместо настоящего состояния. Счёт шагов -- страховка для
	// подменённого Sleep.
	sctx, cancel := context.WithTimeout(ctx, tunnelPowerSettle)
	defer cancel()
	for waited := time.Duration(0); sctx.Err() == nil; waited += tunnelPowerStep {
		t, err := getTunnel(sctx, r.AwgClient, tunnelID)
		if err == nil {
			last, lastErr = t, nil
			if reached(t) {
				if on {
					return "ok", fmt.Sprintf("туннель %s запущен", tunnelID)
				}
				return "ok", fmt.Sprintf("туннель %s остановлен", tunnelID)
			}
		} else {
			lastErr = err
		}
		if waited+tunnelPowerStep > tunnelPowerSettle {
			break
		}
		if err := r.sleep(sctx, tunnelPowerStep); err != nil {
			break
		}
	}
	verb := "не остановился"
	if on {
		verb = "не запустился"
	}
	secs := int(tunnelPowerSettle / time.Second)
	if last == nil {
		slog.Warn("tunnel_power: состояние после команды не прочиталось", "tunnel_id", tunnelID, "on", on, "err", lastErr)
		return "partial", fmt.Sprintf("%s: команду роутер принял, но состояние прочитать не удалось — проверьте экран туннелей", tunnelWord(name))
	}
	if last.Name != "" {
		name = last.Name
	}
	return "partial", fmt.Sprintf("%s %s за %d с: сейчас %s", tunnelWord(name), verb, secs, tunnelStateWords(last))
}

// tunnelWord -- «туннель «имя»», без имени -- просто «туннель».
func tunnelWord(name string) string {
	if name = strings.TrimSpace(name); name == "" {
		return "туннель"
	}
	return "туннель «" + name + "»"
}

// tunnelStateWords -- состояние туннеля словами для ответа владельцу.
func tunnelStateWords(t *awgmgr.Tunnel) string {
	st := tunnelRuntimeState(t)
	words := map[string]string{
		"running":     "работает",
		"starting":    "запускается",
		"stopping":    "останавливается",
		"stopped":     "остановлен",
		"disabled":    "выключен",
		"broken":      "сломан",
		"needs_start": "ждёт запуска",
		"needs_stop":  "ждёт остановки",
		"not_created": "не создан",
	}
	w, ok := words[st]
	if !ok {
		w = "в состоянии «" + st + "»"
		if st == "" {
			w = "в неизвестном состоянии"
		}
	}
	if !t.Enabled && st != "disabled" {
		w += ", выключен в настройках"
	}
	return w
}
