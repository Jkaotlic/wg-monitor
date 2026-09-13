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

// Purpose -- зачем апстрим в наборе. Роль важна сама по себе: апстримы не
// равны между собой, и проверка, считающая их равными, молчит при отказе того
// единственного, что несёт все русские зоны.
type Purpose string

const (
	PurposeYandex  Purpose = "yandex"  // русские зоны, мимо VPN-туннеля
	PurposeForeign Purpose = "foreign" // всё остальное
	PurposePinned  Purpose = "pinned"  // CDN-зоны, закреплённые за одним резолвером
)

// yandexDoTHost -- единственный хост, которому доверены русские зоны.
const yandexDoTHost = "common.dot.dns.yandex.net"

// Таблица. Ни один срез наружу не отдаётся как есть — только копией: иначе
// вызывающий, дописавший элемент, испортит источник правды для всех.
var (
	ruZones = []string{"ru", "su", "xn--p1ai", "xn--80adxhks", "xn--d1acj3b", "xn--p1acf", "tatar"}

	// Кандидаты сторожа. Каждая строка -- готовая подкоманда ndmc без
	// префикса `dns-proxy`; сторож их пробует и НИКОГДА не сохраняет.
	ruCandidates = []string{
		"tls upstream " + yandexDoTHost,
		"https upstream https://" + yandexDoTHost + "/dns-query",
	}

	foreignCandidates = []string{
		"https upstream https://dns.quad9.net/dns-query",
		"https upstream https://freedns.controld.com/p0",
		"https upstream https://cloudflare-dns.com/dns-query",
		"tls upstream 1.1.1.1 sni cloudflare-dns.com",
		"tls upstream 9.9.9.9 sni dns.quad9.net",
	}

	pinnedZones = []string{"themoviedb.org", "tmdb.org", "b-cdn.net", "phncdn.com", "pornhub.com", "rncdn7.com"}

	// Заграничная часть эталона ручного сброса. В отличие от кандидатов
	// сторожа это DoT: набор сохраняется в конфиг роутера и должен работать
	// без сторожа вовсе.
	referenceForeignDoT = []string{
		"tls upstream 9.9.9.9 sni dns.quad9.net",
		"tls upstream 1.1.1.1 sni cloudflare-dns.com",
	}
)

const pinnedCandidate = "https upstream https://cloudflare-dns.com/dns-query"

// RUZones -- русские зоны, которые обязаны уходить к Яндексу.
func RUZones() []string { return copyOf(ruZones) }

// YandexDoTHost -- хост DoT Яндекса. Отдельной функцией, чтобы проверки
// сравнивали с источником, а не с литералом у себя.
func YandexDoTHost() string { return yandexDoTHost }

// ReferenceDoTLines -- то, что ручной сброс ставит на роутер и СОХРАНЯЕТ.
// Сначала заграничная часть (ей резолвится весь остальной мир), затем по
// строке на каждую русскую зону.
func ReferenceDoTLines() []string {
	out := make([]string, 0, len(referenceForeignDoT)+len(ruZones))
	out = append(out, referenceForeignDoT...)
	for _, z := range ruZones {
		out = append(out, "tls upstream "+yandexDoTHost+" domain "+z)
	}
	return out
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

func copyOf(src []string) []string {
	out := make([]string, len(src))
	copy(out, src)
	return out
}
