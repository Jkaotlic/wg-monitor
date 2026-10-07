package backend

import (
	"net/http"
	"regexp"
)

// porthopIfaceRe -- имя интерфейса, которое сторожит смена порта: латиница в
// нижнем регистре, цифры, «_» и «-», с буквы, до 16 знаков (предел имени
// интерфейса в Linux). Агент кладёт имена в conf и в аргументы init-скрипта,
// поэтому ни пробела, ни кавычки, ни «$» до него доезжать не должно.
var porthopIfaceRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,15}$`)

// porthopMaxIfaces -- сколько интерфейсов можно перечислить явно. VPN-туннелей
// на роутере единицы; больше восьми -- не список, а мусор.
const porthopMaxIfaces = 8

// sanitizePorthopInstallArgs собирает аргументы porthop_install заново: до
// агента доезжают ровно ifaces (пустой список -- режим auto, ключ тогда не
// передаётся вовсе) и replace_legacy. Всё прочее клиентское отброшено.
//
// Неверный тип не приводится ни к чему: replace_legacy=false на месте
// присланной строки «true» оставил бы ручную копию оператора драться с
// новой, а true на месте мусора заменил бы её без спроса.
func sanitizePorthopInstallArgs(w http.ResponseWriter, args map[string]any) (map[string]any, bool) {
	out := map[string]any{"replace_legacy": false}
	if raw, present := args["replace_legacy"]; present {
		v, ok := raw.(bool)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "invalid_replace_legacy", "replace_legacy must be a boolean")
			return nil, false
		}
		out["replace_legacy"] = v
	}
	raw, present := args["ifaces"]
	if !present || raw == nil {
		return out, true
	}
	list, ok := raw.([]any)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid_ifaces", "ifaces must be a list of interface names")
		return nil, false
	}
	seen := map[string]bool{}
	ifaces := make([]string, 0, len(list))
	for _, item := range list {
		name, isString := item.(string)
		if !isString || !porthopIfaceRe.MatchString(name) {
			writeJSONError(w, http.StatusBadRequest, "invalid_ifaces", "ifaces must be interface names like opkgtun10")
			return nil, false
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		ifaces = append(ifaces, name)
	}
	if len(ifaces) > porthopMaxIfaces {
		writeJSONError(w, http.StatusBadRequest, "invalid_ifaces", "at most 8 interface names")
		return nil, false
	}
	if len(ifaces) > 0 {
		out["ifaces"] = ifaces
	}
	return out, true
}
