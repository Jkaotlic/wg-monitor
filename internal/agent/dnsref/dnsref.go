// Package dnsref -- ОДИН источник правды об эталонном раздельном DNS.
//
// До него таблица жила в двух местах и разошлась: сторож
// (dnswatch/defaults.go) знал семь ру-зон и не держал Google, ручной сброс
// (actions/dns_reset.go) знал три зоны и Google держал. Значит «починить DNS»
// кнопкой и «сторожить DNS» ставили на роутер разное — а человек считал, что
// это одно и то же.
//
// Пакет отдельный, а не внутри dnswatch, намеренно: зависимость из сторожа в
// actions пошла бы против нынешнего направления (actions уже экспортирует
// помощники сторожу), и вышел бы цикл.
//
// Состав закреплён решением оператора № 5: семь русских зон резолвятся
// Яндексом по DoT, Google убран ВЕЗДЕ — он уводит CDN на американские узлы и
// ломает ровно ту раздельную схему, ради которой всё делается. Таблица
// повторяет живой upstream_dns AdGuard Home оператора (снят 11.09.2026).
//
// Настройки остаются в конфиге агента: значения отсюда — только ДЕФОЛТ для
// них. Второго места для тех же списков не заводим, иначе болезнь вернётся.
package dnsref

import "strings"

// Purpose -- зачем апстрим в наборе. Роль важна сама по себе: апстримы не
// равны между собой, и проверка, считающая их равными, молчит при отказе того
// единственного, что несёт все русские зоны.
type Purpose string

const (
	PurposeRU      Purpose = "ru"      // русские зоны (Яндекс по DoT), мимо VPN-туннеля
	PurposeForeign Purpose = "foreign" // всё остальное
	PurposePinned  Purpose = "pinned"  // CDN-зоны, закреплённые за одним резолвером
)

// ReferenceUpstream -- строка эталона ручного сброса вместе с её ролью.
type ReferenceUpstream struct {
	Line    string
	Purpose Purpose
}

// yandexDoTHost -- единственный хост, которому доверены русские зоны.
const yandexDoTHost = "common.dot.dns.yandex.net"

// Таблица. Ни один срез наружу не отдаётся как есть — только копией: иначе
// вызывающий, дописавший элемент, испортит источник правды для всех.
var (
	// .tatar убран 18.09.2026 решением оператора: KeenOS держит не больше
	// восьми DoT-серверов, а с ним эталон выходил на девять.
	ruZones = []string{"ru", "su", "xn--p1ai", "xn--80adxhks", "xn--d1acj3b", "xn--p1acf"}

	// Кандидаты сторожа. Каждая строка -- готовая подкоманда ndmc без
	// префикса `dns-proxy`; сторож их пробует и НИКОГДА не сохраняет.
	ruCandidates = []string{
		"tls upstream " + yandexDoTHost,
		"https upstream https://" + yandexDoTHost + "/dns-query",
	}

	// Quad9 убран 09.10.2026: из России не отвечает (решение оператора).
	foreignCandidates = []string{
		"https upstream https://freedns.controld.com/p0",
		"https upstream https://cloudflare-dns.com/dns-query",
		"tls upstream 1.1.1.1 sni cloudflare-dns.com",
		"tls upstream 1.0.0.1 sni cloudflare-dns.com",
	}

	pinnedZones = []string{"themoviedb.org", "tmdb.org", "b-cdn.net", "phncdn.com", "pornhub.com", "rncdn7.com"}

	// Заграничная часть эталона ручного сброса. В отличие от кандидатов
	// сторожа это DoT: набор сохраняется в конфиг роутера и должен работать
	// без сторожа вовсе.
	// Quad9 заменён вторым адресом Cloudflare 09.10.2026: из России Quad9 не
	// отвечает; два адреса -- запас, если один зарежут по IP.
	referenceForeignDoT = []string{
		"tls upstream 1.1.1.1 sni cloudflare-dns.com",
		"tls upstream 1.0.0.1 sni cloudflare-dns.com",
	}
)

// KeeneticDoTLimit -- сколько DoT-серверов держит dns-proxy KeenOS («server
// list limit exceeded, the maximum is 8 addresses»). Строка на каждую зону
// считается отдельным адресом.
const KeeneticDoTLimit = 8

// ruCanary -- имя в ру-зоне, которым проверка dns_split спрашивает dns-proxy
// роутера, отвечает ли он вообще.
const ruCanary = "ya.ru"

const pinnedCandidate = "https upstream https://cloudflare-dns.com/dns-query"

// RUZones -- русские зоны, которые обязаны уходить к Яндексу.
func RUZones() []string { return copyOf(ruZones) }

// YandexDoTHost -- хост DoT Яндекса. Отдельной функцией, чтобы проверки
// сравнивали с источником, а не с литералом у себя.
func YandexDoTHost() string { return yandexDoTHost }

// ReferenceUpstreams -- эталон ручного сброса с ролью каждой строки.
// Сначала заграничная часть (ей резолвится весь остальной мир), затем по
// строке на каждую русскую зону.
func ReferenceUpstreams() []ReferenceUpstream {
	out := make([]ReferenceUpstream, 0, len(referenceForeignDoT)+len(ruZones))
	for _, l := range referenceForeignDoT {
		out = append(out, ReferenceUpstream{Line: l, Purpose: PurposeForeign})
	}
	for _, z := range ruZones {
		out = append(out, ReferenceUpstream{Line: "tls upstream " + yandexDoTHost + " domain " + z, Purpose: PurposeRU})
	}
	return out
}

// ReferenceDoTLines -- то, что ручной сброс ставит на роутер и СОХРАНЯЕТ:
// строки ReferenceUpstreams без ролей, в том же порядке.
func ReferenceDoTLines() []string {
	ups := ReferenceUpstreams()
	out := make([]string, 0, len(ups))
	for _, u := range ups {
		out = append(out, u.Line)
	}
	return out
}

// ZonePurpose -- роль строки dns-proxy в НАСТРОЙКАХ роутера по её зоне
// (квалификатор `domain`). Кому роутер отдал русскую зону, тот и несёт
// русские сайты -- Яндекс это или нет; строка без зоны -- общий,
// заграничный апстрим.
func ZonePurpose(zone string) Purpose {
	z := strings.TrimRight(strings.ToLower(strings.TrimSpace(zone)), ".")
	for _, r := range ruZones {
		if z == r {
			return PurposeRU
		}
	}
	for _, p := range pinnedZones {
		if z == p {
			return PurposePinned
		}
	}
	return PurposeForeign
}

// RUCandidates -- формы для сторожа, в порядке предпочтения. Никогда не
// сохраняются: сторож правит конфиг только на время аварии.
func RUCandidates() []string { return copyOf(ruCandidates) }

// ForeignCandidates -- заграничные формы для сторожа, в порядке предпочтения.
func ForeignCandidates() []string { return copyOf(foreignCandidates) }

// PinnedZones -- CDN-зоны, закреплённые за одним резолвером: иначе их ответы
// уводят трафик на чужие узлы.
func PinnedZones() []string { return copyOf(pinnedZones) }

// PinnedCandidate -- резолвер, несущий PinnedZones, пока он жив.
func PinnedCandidate() string { return pinnedCandidate }

// zoneCanaries -- известные имена в русских зонах для пробы сервера, которому
// отдана зона. Для зоны без записи здесь берётся nic.<зона>: проба считает
// ответ «такого имени нет» живым сервером, так что имя может и не
// существовать -- важно, чтобы вопрос был из ЭТОЙ зоны.
var zoneCanaries = map[string]string{
	"ru":       ruCanary,
	"xn--p1ai": "xn--d1abbgf6aiiy.xn--p1ai", // президент.рф
}

// ZoneCanary -- имя для пробы сервера, которому роутер отдал зону.
func ZoneCanary(zone string) string {
	z := strings.TrimRight(strings.ToLower(strings.TrimSpace(zone)), ".")
	if n, ok := zoneCanaries[z]; ok {
		return n
	}
	if z == "" {
		return ruCanary
	}
	return "nic." + z
}

// RUCanary -- имя для пробы живости раздельного DNS.
func RUCanary() string { return ruCanary }

func copyOf(src []string) []string {
	out := make([]string, len(src))
	copy(out, src)
	return out
}
