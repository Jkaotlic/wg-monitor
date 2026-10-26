package backup

import "strings"

// sealedStoreKeys -- хранилища ключей, словами владельца, в порядке вывода
// («ключи кабинетов, панелей VPS, своих VPS»). Имена -- как в архиве
// (cfg.StoreFiles).
var sealedStoreKeys = []struct{ name, words string }{
	{"amnezia-premium.json", "кабинетов"},
	{"awg3-panels.json", "панелей VPS"},
	{"amnezia-selfhosted.json", "своих VPS"},
}

const hideMyStore = "hidemyname.json"

// SealedStoresNote -- честная строка для манифеста и тревоги: что не
// откроется без прежнего revive.key (ключ шифрования в архив не кладётся).
// names -- зашифрованные хранилища архива; пусто -- пустая строка.
// Незнакомое имя идёт как есть, в конце.
func SealedStoresNote(names []string) string {
	if len(names) == 0 {
		return ""
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	var items []string
	for _, s := range sealedStoreKeys {
		if have[s.name] {
			w := s.words
			if len(items) == 0 {
				w = "ключи " + w
			}
			items = append(items, w)
			delete(have, s.name)
		}
	}
	if have[hideMyStore] {
		items = append(items, "коды HideMy")
		delete(have, hideMyStore)
	}
	for _, n := range names {
		if have[n] {
			items = append(items, n)
			delete(have, n)
		}
	}
	list := items[len(items)-1]
	if len(items) > 1 {
		list = strings.Join(items[:len(items)-1], ", ") + " и " + list
	}
	return "без прежнего revive.key не откроются: " + list
}
