// Package redact -- запасная маска адресов для журнала awg-manager, когда
// сама сборка не пометила запись как замаскированную. Основная маска --
// у awg-manager (sanitized:true по умолчанию, С1); эта грубее и ловит
// IPv4, IPv6 и имена хостов.
package redact

import "regexp"

var (
	reIPv4 = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\b`)
	// IPv6: либо четыре и больше групп подряд, либо сжатая форма с «::».
	// Время «15:04:05» (три группы, без «::») сюда не попадает.
	reIPv6 = regexp.MustCompile(`(?i:\b(?:[0-9a-f]{1,4}:){4,7}[0-9a-f]{1,4}\b|\b(?:[0-9a-f]{1,4}:)+:(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?|::(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*))`)
	reHost = regexp.MustCompile(`\b([a-zA-Z0-9])[a-zA-Z0-9-]*(?:\.[a-zA-Z0-9-]+)*\.([a-zA-Z]{2,24})\b`)
)

// Text маскирует адреса: 198.51.100.7 -> 198.*.*.7, IPv6 -> <IPv6>,
// vpn.example.com -> v***.com.
func Text(s string) string {
	s = reIPv4.ReplaceAllString(s, "$1.*.*.$4")
	s = reIPv6.ReplaceAllString(s, "<IPv6>")
	s = reHost.ReplaceAllString(s, "$1***.$2")
	return s
}
