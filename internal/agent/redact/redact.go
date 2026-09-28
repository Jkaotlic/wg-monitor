// Package redact -- запасная маска адресов для журнала awg-manager, когда
// сама сборка не пометила запись как замаскированную. Основная маска --
// у awg-manager (sanitized:true по умолчанию, С1); эта грубее и ловит
// IPv4, IPv6 и имена хостов.
//
// Fix round 1 (ревью 28.09): границы больше не проверяются через \b. В
// Go/RE2 \b считает «словом» только [0-9A-Za-z_], поэтому адрес, приклеенный
// к подчёркиванию или к произвольной букве без пробела ("peer_198.51.100.7",
// "x198.51.100.7"), раньше проходил немаскированным, а кириллический хост
// ("сайт.рф") не матчился вовсе -- классы символов были чисто ASCII. RE2 не
// умеет в лукахэды, поэтому оба совпадения ищутся «кандидатом» без границ, а
// соседние руны проверяются вручную через unicode.IsLetter/IsDigit.
package redact

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// reIPv4Digits находит кандидатов «четыре числа через точку» без всякой
	// проверки границ -- границы проверяются вручную в maskIPv4, потому что
	// нужно смотреть по обе стороны совпадения одновременно.
	reIPv4Digits = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)
	// IPv6: либо четыре и больше групп подряд, либо сжатая форма с «::».
	// Время «15:04:05» (три группы, без «::») сюда не попадает. Без \b (как
	// и у IPv4): «_» для \b -- часть слова, и в «peer_2001:db8::1» первая
	// группа уходила открытой. Левая граница проверяется в maskIPv6.
	reIPv6 = regexp.MustCompile(`(?i:(?:[0-9a-f]{1,4}:){4,7}[0-9a-f]{1,4}|(?:[0-9a-f]{1,4}:)+:(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?|::(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*))`)
	// reHostCandidate находит «слово.слово(.TLD)», Unicode-aware (\p{L}), с
	// той же ручной проверкой границ, что у IPv4 -- «сайт.рф» обязан
	// маскироваться наравне с «vpn.example.com».
	reHostCandidate = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}-]*(?:\.[\p{L}\p{N}-]+)*\.\p{L}{2,24}`)
)

// Text маскирует адреса: 198.51.100.7 -> 198.*.*.7, IPv6 -> <IPv6>,
// vpn.example.com -> v***.com, сайт.рф -> с***.рф.
func Text(s string) string {
	s = maskIPv4(s)
	s = maskIPv6(s)
	s = maskHost(s)
	return s
}

// maskIPv4 маскирует только настоящие четырёхоктетные адреса: символ перед
// совпадением не должен быть цифрой или точкой (иначе это кусок более
// длинного числа -- версии вроде "2.19.9" не трогаем вообще, там всего три
// группы, и raw-паттерн туда не попадёт), а после четвёртого октета не
// должно идти ни цифры (жадный \d{1,3} мог откусить только часть более
// длинного числа), ни точки с цифрой следом (это уже пятый октет, не IPv4).
func maskIPv4(s string) string {
	idxs := reIPv4Digits.FindAllStringIndex(s, -1)
	if idxs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idxs {
		start, end := m[0], m[1]
		if start > 0 {
			pr, _ := utf8.DecodeLastRuneInString(s[:start])
			if pr == '.' || unicode.IsDigit(pr) {
				continue
			}
		}
		if end < len(s) {
			nr, sz := utf8.DecodeRuneInString(s[end:])
			if unicode.IsDigit(nr) {
				continue
			}
			if nr == '.' && end+sz < len(s) {
				nr2, _ := utf8.DecodeRuneInString(s[end+sz:])
				if unicode.IsDigit(nr2) {
					continue
				}
			}
		}
		octets := strings.SplitN(s[start:end], ".", 4)
		b.WriteString(s[last:start])
		b.WriteString(octets[0] + ".*.*." + octets[3])
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// maskIPv6 заменяет адрес на <IPv6>. Кандидат пропускается, только если
// перед ним цифра (любая, unicode.IsDigit) или шестнадцатеричная буква: тогда
// это хвост более длинной hex-строки, а не отдельный адрес. Буквы вне
// a-f, «_», кириллица и знаки препинания границей служат -- как у IPv4, адрес
// вплотную к префиксу маскируется. Справа не проверяется: регулярка жадная, и
// отказ по правому соседу только выпустил бы адрес наружу целиком.
func maskIPv6(s string) string {
	idxs := reIPv6.FindAllStringIndex(s, -1)
	if idxs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idxs {
		start, end := m[0], m[1]
		if start > 0 {
			pr, _ := utf8.DecodeLastRuneInString(s[:start])
			if unicode.IsDigit(pr) || isHexLetter(pr) {
				continue
			}
		}
		b.WriteString(s[last:start])
		b.WriteString("<IPv6>")
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

func isHexLetter(r rune) bool {
	return (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// maskHost маскирует хосты вида «имя.домен» первой буквой и TLD:
// vpn.example.com -> v***.com, сайт.рф -> с***.рф. Символ перед и после
// совпадения не должен быть буквой или цифрой -- иначе оно оказалось бы
// серединой более длинного слова, а не отдельным хостом.
func maskHost(s string) string {
	idxs := reHostCandidate.FindAllStringIndex(s, -1)
	if idxs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idxs {
		start, end := m[0], m[1]
		if start > 0 {
			pr, _ := utf8.DecodeLastRuneInString(s[:start])
			if unicode.IsLetter(pr) || unicode.IsDigit(pr) {
				continue
			}
		}
		if end < len(s) {
			nr, _ := utf8.DecodeRuneInString(s[end:])
			if unicode.IsLetter(nr) || unicode.IsDigit(nr) {
				continue
			}
		}
		host := s[start:end]
		dot := strings.LastIndexByte(host, '.')
		if dot < 0 {
			continue
		}
		firstRune, _ := utf8.DecodeRuneInString(host)
		b.WriteString(s[last:start])
		b.WriteRune(firstRune)
		b.WriteString("***.")
		b.WriteString(host[dot+1:])
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}
