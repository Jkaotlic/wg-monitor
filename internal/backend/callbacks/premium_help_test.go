package callbacks

import (
	"strings"
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

func TestAmneziaImportQueuedViewOffersNextActions(t *testing.T) {
	text, kb := amneziaImportQueuedView(7, "key1", "\n\nФайл в чат не отправился: boom")
	if !strings.Contains(text, "Импорт туннеля поставлен в очередь") {
		t.Fatalf("queued view should explain import queue, got %q", text)
	}
	if !strings.Contains(text, "Проверить тоннели") {
		t.Fatalf("queued view should suggest tunnel check, got %q", text)
	}
	if !strings.Contains(text, "Маршруты") {
		t.Fatalf("queued view should suggest route transfer, got %q", text)
	}
	if !strings.Contains(text, "Файл в чат не отправился: boom") {
		t.Fatalf("queued view should preserve document warning, got %q", text)
	}

	callbacks := flattenKbCallbacks(&kb)
	for _, want := range []string{
		"tunnels_refresh:7:_panel_",
		"routes_open:7:_panel_",
		"amz_countries:7:_panel_:key1:0",
	} {
		if !containsStr(callbacks, want) {
			t.Fatalf("queued view missing callback %q (have %v)", want, callbacks)
		}
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
