package callbacks

import (
	"testing"

	"github.com/anex/wg-monitor/internal/backend/amnezia"
	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestAmneziaPremiumKeyboardsIncludeHelp(t *testing.T) {
	user := &db.User{ID: 7, Nickname: "testkeen"}
	_, emptyKB := amneziaKeyListView(user, amneziaRouterKeys{})
	if !containsStr(flattenKbCallbacks(&emptyKB), "panel:0:help:premium") {
		t.Fatalf("empty amnezia list should expose premium help: %+v", emptyKB)
	}

	_, listKB := amneziaKeyListView(user, amneziaRouterKeys{ActiveID: "key1", Keys: []amneziaStoredKey{{ID: "key1", Label: "Key #1"}}})
	if !containsStr(flattenKbCallbacks(&listKB), "panel:0:help:premium") {
		t.Fatalf("amnezia key list should expose premium help: %+v", listKB)
	}

	accountKB := amneziaKeyboard(user.ID, "key1", &amnezia.AccountInfo{AvailableCountries: []amnezia.Country{{Code: "de", Name: "Germany"}}})
	if !containsStr(flattenKbCallbacks(&accountKB), "panel:0:help:premium") {
		t.Fatalf("amnezia account should expose premium help: %+v", accountKB)
	}

	_, countriesKB := amneziaCountriesView(user, amneziaStoredKey{ID: "key1", Label: "Key #1"}, &amnezia.AccountInfo{AvailableCountries: []amnezia.Country{{Code: "de", Name: "Germany"}}}, 0)
	if !containsStr(flattenKbCallbacks(&countriesKB), "panel:0:help:premium") {
		t.Fatalf("amnezia countries should expose premium help: %+v", countriesKB)
	}
}

func TestHideMyPremiumCodeListIncludesHelp(t *testing.T) {
	user := &db.User{ID: 7, Nickname: "testkeen"}
	_, emptyKB := hideMyCodeListView(user, hideMyCodes{})
	if !containsStr(flattenKbCallbacks(&emptyKB), "panel:0:help:premium") {
		t.Fatalf("empty hidemy list should expose premium help: %+v", emptyKB)
	}

	_, listKB := hideMyCodeListView(user, hideMyCodes{ActiveID: "code1", Codes: []hideMyStoredCode{{ID: "code1", Label: "Code #1"}}})
	if !containsStr(flattenKbCallbacks(&listKB), "panel:0:help:premium") {
		t.Fatalf("hidemy list should expose premium help: %+v", listKB)
	}
}
