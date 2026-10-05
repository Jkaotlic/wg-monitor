package linkrepair

import "strings"

// Тексты лесенки для людей. Всё здесь читает владелец (и операторы) --
// в личке бота и на экране починки, поэтому правила те же, что у тревог:
// «VPN-туннель» полной формой, имена в ёлочках, латиница -- только в ёлочках
// (названия кабинетов тоже), никаких имён команд и советов, куда человек
// пойти не может. Сторожит report_test.go.

const (
	textRestarted     = "перезапустил"
	textRestartNoHelp = "перезапуск не помог"
	textReissueNoHelp = "конфиг заново не помог"
)

func textMovedTo(backup string) string {
	return "увёл трафик на запасной VPN-туннель «" + backup + "»"
}

// sourceLabel -- откуда выпускается конфиг, в родительном падеже после «из».
func sourceLabel(provider string) string {
	switch provider {
	case "amnezia":
		return "«Amnezia Premium»"
	case "hidemyname":
		return "«HideMy.name»"
	case "awg3":
		return "своего сервера"
	default:
		return "источника"
	}
}

func textReissued(provider string) string {
	return "выпустил конфиг заново из " + sourceLabel(provider)
}

// textRecreated -- что сделала ступень «пересоздать»: на своём сервере --
// новое подключение, у кабинета -- другая локация.
func textRecreated(provider, option string) string {
	if provider == "awg3" {
		return "завёл новое подключение на своём сервере"
	}
	return "сменил локацию на «" + option + "»"
}

func joinLog(log []string) string { return strings.Join(log, " · ") }

// progressText -- правка по ходу: что уже сделано и что сейчас.
// «Чиню: увёл трафик на запасной VPN-туннель «B» · перезапуск не помог ·
// сейчас: выпускаю конфиг заново из «Amnezia Premium»…»
func progressText(_ lineNames, log []string, now string) string {
	if len(log) == 0 {
		return "Чиню: " + now + "…"
	}
	return "Чиню: " + joinLog(log) + " · сейчас: " + now + "…"
}

// doneText -- итог удачи.
func doneText(n lineNames, log []string) string {
	text := "Починил"
	if len(log) > 0 {
		text += ": " + joinLog(log)
	}
	text += ". VPN-туннель «" + n.broken + "» снова работает"
	switch {
	case n.backup != "" && n.failbackFailed:
		text += ", но вернуть на него трафик не вышло — он идёт через запасной VPN-туннель «" + n.backup + "»."
	case n.backup != "":
		text += ", трафик вернул на него."
	default:
		text += "."
	}
	return text
}

// failText -- итог провала: что пробовал и где сейчас идёт трафик.
func failText(n lineNames, log []string) string {
	if n.notInSet {
		// Через такой VPN-туннель правила не идут: пугать владельца
		// «заблокированное не открывается» -- врать.
		return "VPN-туннель «" + n.broken + "» упал, но чинить нечего: он не входит ни в один общий набор правил, и через него ничего не идёт."
	}
	text := "Починить VPN-туннель «" + n.broken + "» сам не смог"
	if len(log) > 0 {
		text += " (" + joinLog(log) + ")"
	}
	text += "."
	switch {
	case n.noSnapshot:
		// Снимка нет -- неизвестно, подхватил ли трафик запасной VPN-туннель.
		text += " Подхватил ли трафик запасной VPN-туннель, роутер не сообщил — это видно в приложении."
	case n.backup != "":
		text += " Обход блокировок работает через запасной VPN-туннель «" + n.backup + "»."
	default:
		text += " Заблокированное сейчас не открывается."
	}
	return text
}
