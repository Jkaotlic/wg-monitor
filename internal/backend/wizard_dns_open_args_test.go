package backend

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

// У dns_open своя ветка санитайзера: наружу уходит ровно имя сайта, всё
// остальное клиентское не доезжает. Проверка имени -- та же строгая, что у
// route_lookup: заводить вторую такую валидацию значило бы разводить копии,
// которые потом разойдутся.
func TestSanitizeDNSOpenKeepsOnlyDomain(t *testing.T) {
	w := httptest.NewRecorder()
	got, ok := sanitizeWizardCommandArgs(w, "dns_open", map[string]any{
		"domain":    "GosUslugi.Example.COM.",
		"ndms_name": "Wireguard0",
		"url":       "https://example.com/evil",
	})
	if !ok {
		t.Fatalf("санитайзер отверг годное имя: %d %s", w.Code, w.Body.String())
	}
	want := map[string]any{"domain": "gosuslugi.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("аргументы = %#v, хотим %#v", got, want)
	}
}

// Не сайт -- отказ. Адрес с путём или портом, имя без точки и пустое значение
// до очереди команд доезжать не должны.
func TestSanitizeDNSOpenRejectsNonDomains(t *testing.T) {
	for _, bad := range []any{
		"", "localhost", "example.com/path", "example.com:443",
		"два слова", 42, true, nil,
	} {
		w := httptest.NewRecorder()
		got, ok := sanitizeWizardCommandArgs(w, "dns_open", map[string]any{"domain": bad})
		if ok {
			t.Errorf("принято негодное имя %#v -> %#v", bad, got)
			continue
		}
		if w.Code != 400 {
			t.Errorf("имя %#v: код %d, хотим 400", bad, w.Code)
		}
	}
}
