package dnswatch

import (
	"reflect"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
)

// Дефолты сторожа обязаны совпадать с dnsref. ЧЕСТНАЯ ОГОВОРКА: этот тест
// зеленел и ДО того, как копию убрали — таблицу в dnsref перенесли отсюда без
// изменений, и значения совпадали сами собой. Значит он не доказывает
// единственность источника, он сторожит РАСХОЖДЕНИЕ значений: копию убрали
// правкой defaults.go, а тест ловит, если кто-то заведёт её снова и та уедет.
// Выдавать его за доказательство единственности было бы фикцией.
func TestWatchdogDefaultsComeFromSingleSource(t *testing.T) {
	for _, c := range []struct {
		name string
		got  []string
		want []string
	}{
		{"ру-зоны", DefaultRUZones, dnsref.RUZones()},
		{"ру-кандидаты", DefaultRUCandidates, dnsref.RUCandidates()},
		{"заграничные кандидаты", DefaultForeignCandidates, dnsref.ForeignCandidates()},
		{"закреплённые зоны", DefaultPinnedZones, dnsref.PinnedZones()},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s разошлись с источником правды:\n сторож: %v\n dnsref: %v", c.name, c.got, c.want)
		}
	}
	if DefaultPinnedCandidate != dnsref.PinnedCandidate() {
		t.Errorf("закреплённый кандидат разошёлся: сторож %q, dnsref %q", DefaultPinnedCandidate, dnsref.PinnedCandidate())
	}
}

// Дефолты отдаются наружу как пакетные переменные, поэтому важно, что они
// держат СВОЮ копию таблицы: иначе правка конфигом уедет в источник правды и
// испортит набор всем остальным.
func TestWatchdogDefaultsDoNotShareSliceWithSource(t *testing.T) {
	if len(DefaultRUZones) == 0 {
		t.Fatal("зон нет")
	}
	saved := DefaultRUZones[0]
	DefaultRUZones[0] = "сломано"
	defer func() { DefaultRUZones[0] = saved }()

	if dnsref.RUZones()[0] == "сломано" {
		t.Error("дефолты сторожа делят срез с dnsref — правка одного портит другой")
	}
}
