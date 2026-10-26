package backup

import "testing"

func TestSealedStoresNote(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  string
	}{
		{nil, ""},
		{[]string{"hidemyname.json", "awg3-panels.json", "amnezia-selfhosted.json", "amnezia-premium.json"},
			"без прежнего revive.key не откроются: ключи кабинетов, панелей VPS, своих VPS, кодов HideMy"},
		{[]string{"awg3-panels.json"}, "без прежнего revive.key не откроются: ключи панелей VPS"},
		{[]string{"amnezia-premium.json", "future-store.json"}, "без прежнего revive.key не откроются: ключи кабинетов, future-store.json"},
	} {
		if got := SealedStoresNote(c.names); got != c.want {
			t.Errorf("SealedStoresNote(%v) = %q, ждали %q", c.names, got, c.want)
		}
	}
}
