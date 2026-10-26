package backup

import "strings"

// sealedStoreWords -- что лежит в зашифрованном хранилище, словами
// владельца, в порядке вывода. Имена -- как в архиве (cfg.StoreFiles).
var sealedStoreWords = []struct{ name, words string }{
	{"amnezia-premium.json", "кабинетов"},
	{"awg3-panels.json", "панелей VPS"},
	{"amnezia-selfhosted.json", "своих VPS"},
	{"hidemyname.json", "кодов HideMy"},
}

// SealedStoresNote -- честная строка для манифеста и превью восстановления:
// что не откроется без прежнего revive.key (ключ шифрования в архив не
// кладётся). names -- зашифрованные хранилища архива; пусто -- пустая
// строка. Незнакомое имя идёт как есть, после известных.
func SealedStoresNote(names []string) string {
	if len(names) == 0 {
		return ""
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	var parts []string
	for _, s := range sealedStoreWords {
		if have[s.name] {
			parts = append(parts, s.words)
			delete(have, s.name)
		}
	}
	for _, n := range names {
		if have[n] {
			parts = append(parts, n)
			delete(have, n)
		}
	}
	return "без прежнего revive.key не откроются: ключи " + strings.Join(parts, ", ")
}
