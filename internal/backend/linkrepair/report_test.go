package linkrepair

import (
	"strings"
	"testing"
)

// Все тексты, которые лесенка шлёт людям, -- под сторожами словаря: имена в
// ёлочках, «VPN-туннель» полной формой, латиница только в ёлочках, ни одного
// совета, куда владелец пойти не может (ssh, opkg, имена команд).
func TestReport_TextsSpeakOwnerVocabulary(t *testing.T) {
	withBackup := lineNames{broken: "Дача", backup: "Работа"}
	alone := lineNames{broken: "Дача"}
	log := []string{textMovedTo("Работа"), textRestartNoHelp, textReissueNoHelp}
	texts := map[string]string{
		"textRestarted":       textRestarted,
		"textRestartNoHelp":   textRestartNoHelp,
		"textReissueNoHelp":   textReissueNoHelp,
		"textMovedTo":         textMovedTo("Работа"),
		"progress":            progressText(withBackup, log, "пересоздаю"),
		"progress пустой лог": progressText(alone, nil, "перезапускаю"),
		"done с резервом":     doneText(withBackup, append(log, textRecreated("amnezia", "de"))),
		"done без резерва":    doneText(alone, []string{textRestarted}),
		"fail с резервом":     failText(withBackup, log),
		"fail без резерва":    failText(alone, []string{textRestartNoHelp}),
		"fail пустой лог":     failText(alone, nil),
		"fail нет снимка":     failText(lineNames{broken: "Дача", noSnapshot: true}, nil),
		"fail вне наборов":    failText(lineNames{broken: "Дача", notInSet: true}, nil),
		"failback не вышел":   doneText(lineNames{broken: "Дача", backup: "Работа", failbackFailed: true}, []string{textRestarted}),
	}
	for _, p := range []string{"amnezia", "hidemyname", "awg3"} {
		texts["sourceLabel "+p] = sourceLabel(p)
		texts["textReissued "+p] = textReissued(p)
		texts["textRecreated "+p] = textRecreated(p, "de")
	}
	for _, a := range []string{ActAmneziaKey, ActHideMyCode, ActNoSource, ActServerDead, ActTooOften, ActAgentOld, ActCabinetKeyLocked, ActVPSPanel("vps1")} {
		texts["действие "+a] = a
	}
	for name, text := range texts {
		if strings.TrimSpace(text) == "" {
			t.Errorf("%s: пустой текст", name)
			continue
		}
		if words := latinOutsideQuotes(text); len(words) > 0 {
			t.Errorf("%s: латиница вне ёлочек %v: %q", name, words, text)
		}
		low := strings.ToLower(text)
		for _, bad := range []string{"ssh", "opkg", "tunnel_", "лини"} {
			if strings.Contains(low, bad) {
				t.Errorf("%s: %q в тексте владельцу: %q", name, bad, text)
			}
		}
		// Каждое «туннель» -- в полной форме «VPN-туннель».
		if n, full := strings.Count(low, "туннел"), strings.Count(low, "vpn-туннел"); n != full {
			t.Errorf("%s: «туннель» без «VPN-»: %q", name, text)
		}
	}
	if !strings.Contains(texts["done с резервом"], "VPN-туннель «Дача» снова работает") {
		t.Errorf("итог удачи не называет VPN-туннель: %q", texts["done с резервом"])
	}
	if !strings.Contains(texts["fail с резервом"], "запасной VPN-туннель «Работа»") {
		t.Errorf("итог провала не говорит, где трафик: %q", texts["fail с резервом"])
	}
	if !strings.Contains(texts["fail без резерва"], "Заблокированное сейчас не открывается") {
		t.Errorf("итог провала без резерва: %q", texts["fail без резерва"])
	}
	if sourceLabel("amnezia") != "«Amnezia Premium»" || sourceLabel("hidemyname") != "«HideMy.name»" {
		t.Errorf("кабинеты называются в ёлочках: %q %q", sourceLabel("amnezia"), sourceLabel("hidemyname"))
	}
}
