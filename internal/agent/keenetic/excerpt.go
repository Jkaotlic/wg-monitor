package keenetic

import "strings"

// ErrExcerptMaxBytes -- вывод длиннее не кладётся в ошибку вовсе: сообщение
// ndmc об ошибке короткое, а длинный вывод -- это кусок конфига, и в нём
// бывают ключи Wi-Fi (28.09 фильтр по «password» пропустил «wpa psk»).
const ErrExcerptMaxBytes = 300

// ExcerptMaxRunes -- потолок выдержки в знаках.
const ExcerptMaxRunes = 200

// Excerpt -- первые maxLines непустых строк вывода ndmc через « | », без
// \x1b[K и пробелов по краям, не длиннее ExcerptMaxRunes (плюс «…»).
func Excerpt(out string, maxLines int) string {
	var lines []string
	for _, raw := range strings.Split(out, "\n") {
		l := strings.TrimSpace(strings.ReplaceAll(raw, "\x1b[K", ""))
		if l == "" {
			continue
		}
		lines = append(lines, l)
		if len(lines) == maxLines {
			break
		}
	}
	s := strings.Join(lines, " | ")
	if r := []rune(s); len(r) > ExcerptMaxRunes {
		s = string(r[:ExcerptMaxRunes]) + "…"
	}
	return s
}

// ErrExcerpt -- выдержка для текста ошибки: первая строка короткого вывода,
// пусто для длинного.
func ErrExcerpt(out string) string {
	if len(out) > ErrExcerptMaxBytes {
		return ""
	}
	return Excerpt(out, 1)
}
