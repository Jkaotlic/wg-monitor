package actions

import (
	"reflect"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
)

// Ручной сброс обязан брать набор из dnsref, а не держать свою копию. Копия
// уже разошлась однажды: здесь лежали три ру-зоны и Google, тогда как сторож
// знал семь зон и Google не держал — то есть «починить DNS» кнопкой и
// «сторожить DNS» ставили на роутер разное.
func TestReferenceSetComesFromSingleSource(t *testing.T) {
	want := dnsref.ReferenceDoTLines()
	if !reflect.DeepEqual(dnsReferenceUpstreams, want) {
		t.Fatalf("набор сброса разошёлся с источником правды:\n сброс: %v\n dnsref: %v", dnsReferenceUpstreams, want)
	}
}
