package dnsref_test

import (
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
)

// Обе формы -- строки ручного сброса и кандидаты сторожа -- обязаны выводиться
// из ОДНОЙ таблицы. До этого пакета копий было две, и они разошлись: сторож
// знал семь ру-зон и не знал Google, ручной сброс знал три зоны и держал
// Google. Расхождение означало, что «починить DNS» и «сторожить DNS» ставят
// на роутер разное.
func TestBothSetsDeriveFromOneTable(t *testing.T) {
	zones := dnsref.RUZones()
	if len(zones) != 6 {
		t.Fatalf("ру-зон %d, хотим 6 (решение оператора № 5; .tatar убран 18.09 -- лимит KeenOS)", len(zones))
	}
	lines := dnsref.ReferenceDoTLines()
	for _, z := range zones {
		var found bool
		for _, line := range lines {
			if strings.HasSuffix(line, " domain "+z) {
				found = true
			}
		}
		if !found {
			t.Errorf("зона %q не попала в эталон сброса", z)
		}
	}
}

// Держать в «эталоне» то, что оператор выкинул из своего AdGuard Home, значило
// бы ручным сбросом ломать раздельную схему: Google уводит CDN на американские
// узлы.
func TestGoogleAppearsNowhere(t *testing.T) {
	all := append(append(append([]string{},
		dnsref.ReferenceDoTLines()...),
		dnsref.RUCandidates()...),
		dnsref.ForeignCandidates()...)
	all = append(all, dnsref.PinnedCandidate())
	for _, line := range all {
		for _, g := range []string{"8.8.8.8", "8.8.4.4", "dns.google"} {
			if strings.Contains(line, g) {
				t.Errorf("Google остался в наборе: %q", line)
			}
		}
	}
}

// Требование оператора (решение № 1): ру-зоны резолвятся Яндексом ПО DoT.
// Любая зонная строка, ведущая не туда, ломает это свойство.
func TestRUZonesGoToYandexOverDoT(t *testing.T) {
	for _, line := range dnsref.ReferenceDoTLines() {
		if !strings.Contains(line, "domain ") {
			continue
		}
		if !strings.HasPrefix(line, "tls upstream "+dnsref.YandexDoTHost()) {
			t.Errorf("зонная строка ведёт не к Яндексу по DoT: %q", line)
		}
	}
}

// Заграничная часть эталона обязана существовать: без неё сброс оставил бы
// роутер с одними ру-зонами, то есть без резолвинга всего остального.
func TestReferenceKeepsForeignPart(t *testing.T) {
	var foreign int
	for _, line := range dnsref.ReferenceDoTLines() {
		if !strings.Contains(line, "domain ") {
			foreign++
		}
	}
	if foreign == 0 {
		t.Fatal("в эталоне сброса нет ни одной незонной строки — резолвить весь мир нечем")
	}
}

// Наборы сторожа не пустые и не совпадают друг с другом: ру-кандидаты ведут к
// Яндексу, заграничные -- нет. Совпадение означало бы, что раздельной схемы
// нет вовсе.
func TestWatchdogSetsAreDistinct(t *testing.T) {
	ru, foreign := dnsref.RUCandidates(), dnsref.ForeignCandidates()
	if len(ru) == 0 || len(foreign) == 0 {
		t.Fatalf("кандидаты пусты: ру %d, заграничных %d", len(ru), len(foreign))
	}
	for _, r := range ru {
		if !strings.Contains(r, dnsref.YandexDoTHost()) {
			t.Errorf("ру-кандидат ведёт не к Яндексу: %q", r)
		}
		for _, f := range foreign {
			if r == f {
				t.Errorf("кандидат в обоих наборах сразу: %q", r)
			}
		}
	}
}

// Таблицу отдают копией: вызывающий не должен уметь испортить источник правды
// для всех остальных, дописав или переписав элемент.
func TestTableIsNotSharedState(t *testing.T) {
	first := dnsref.RUZones()
	if len(first) == 0 {
		t.Fatal("зон нет")
	}
	first[0] = "сломано"
	if dnsref.RUZones()[0] == "сломано" {
		t.Error("RUZones отдаёт общий срез — правка у вызывающего портит источник")
	}
}

// Закреплённые зоны существуют и несутся своим кандидатом: этот кусок эталона
// оператор вынес отдельно, потому что CDN-зоны иначе уезжают не туда.
func TestPinnedZonesCarriedByOwnCandidate(t *testing.T) {
	if len(dnsref.PinnedZones()) == 0 {
		t.Fatal("закреплённых зон нет")
	}
	if dnsref.PinnedCandidate() == "" {
		t.Fatal("закреплённым зонам нечем резолвиться")
	}
}

// Канарейка живости обязана лежать в ру-зоне: проба спрашивает dns-proxy
// роутера, и ответ должен пройти ровно тем путём, что и банки с госуслугами.
func TestRUCanaryLivesInRUZone(t *testing.T) {
	name := dnsref.RUCanary()
	if name == "" {
		t.Fatal("канарейки нет — проверке раздельного DNS нечем спрашивать роутер")
	}
	for _, z := range dnsref.RUZones() {
		if strings.HasSuffix(name, "."+z) {
			return
		}
	}
	t.Errorf("канарейка %q не лежит ни в одной ру-зоне эталона", name)
}

// KeenOS держит не больше восьми DoT-серверов («server list limit exceeded,
// the maximum is 8 addresses», workrouter 18.09): девятая строка эталона не
// вставала никогда, и любой сброс заканчивался «не полностью».
func TestReferenceFitsKeeneticDoTLimit(t *testing.T) {
	lines := dnsref.ReferenceDoTLines()
	if len(lines) > dnsref.KeeneticDoTLimit {
		t.Fatalf("эталон %d строк, KeenOS держит %d", len(lines), dnsref.KeeneticDoTLimit)
	}
	for _, l := range lines {
		if strings.HasSuffix(l, " domain tatar") {
			t.Fatalf(".tatar убран решением оператора 18.09: %q", l)
		}
	}
}

// Апстримы эталона несут роль (спека v0.55, C): падение того, кому отданы
// русские зоны, -- отдельная беда, и проверка обязана отличать его от
// заграничных. Роль -- часть самого эталона, а не догадка проверки.
func TestReferenceUpstreamsCarryRole(t *testing.T) {
	ups := dnsref.ReferenceUpstreams()
	lines := dnsref.ReferenceDoTLines()
	if len(ups) != len(lines) {
		t.Fatalf("строк с ролью %d, строк эталона %d -- источник обязан быть один", len(ups), len(lines))
	}
	var ru, foreign int
	for i, u := range ups {
		if u.Line != lines[i] {
			t.Errorf("строка %d: %q против %q", i, u.Line, lines[i])
		}
		switch u.Purpose {
		case dnsref.PurposeRU:
			ru++
			if !strings.Contains(u.Line, " domain ") || !strings.HasPrefix(u.Line, "tls upstream "+dnsref.YandexDoTHost()) {
				t.Errorf("ру-роль у строки не Яндекса по DoT с зоной: %q", u.Line)
			}
		case dnsref.PurposeForeign:
			foreign++
			if strings.Contains(u.Line, " domain ") {
				t.Errorf("заграничная роль у зонной строки: %q", u.Line)
			}
		default:
			t.Errorf("строка %q без роли ru/foreign: %q", u.Line, u.Purpose)
		}
	}
	if ru != len(dnsref.RUZones()) || foreign == 0 {
		t.Errorf("ru=%d foreign=%d", ru, foreign)
	}
	if dnsref.PurposeRU != "ru" || dnsref.PurposeForeign != "foreign" {
		t.Errorf("значения ролей: %q / %q", dnsref.PurposeRU, dnsref.PurposeForeign)
	}
}

// Роль строки НАСТРОЕК роутера определяется её зоной: кому роутер отдал
// русскую зону, тот и несёт русские сайты, будь это Яндекс или нет.
func TestZonePurpose(t *testing.T) {
	cases := map[string]dnsref.Purpose{
		"ru":             dnsref.PurposeRU,
		"RU.":            dnsref.PurposeRU,
		" xn--p1ai ":     dnsref.PurposeRU,
		"su":             dnsref.PurposeRU,
		"":               dnsref.PurposeForeign,
		"example.com":    dnsref.PurposeForeign,
		"themoviedb.org": dnsref.PurposePinned,
	}
	for zone, want := range cases {
		if got := dnsref.ZonePurpose(zone); got != want {
			t.Errorf("ZonePurpose(%q) = %q, хотим %q", zone, got, want)
		}
	}
}

// Имя пробы сервера русской зоны -- из ЭТОЙ зоны.
func TestZoneCanaryIsInTheZone(t *testing.T) {
	for _, z := range dnsref.RUZones() {
		if n := dnsref.ZoneCanary(z); !strings.HasSuffix(n, "."+z) {
			t.Errorf("зона %s: имя пробы %q не из неё", z, n)
		}
	}
	if dnsref.ZoneCanary("RU.") != dnsref.RUCanary() {
		t.Errorf("ru: %q", dnsref.ZoneCanary("RU."))
	}
}
