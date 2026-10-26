package provision

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// v0.62 T10: коды метки отказа живут в двух местах -- err_code в
// awgm-relay.py и ErrCodes здесь. Код, который relay печатает, а backend не
// знает, молча выпадает в общую подсказку; обратное -- мёртвая ветка.
func TestErrCodes_MatchRelay(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "awgmrelay", "awgm-relay.py"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "\ndef err_code(")
	if start < 0 {
		t.Fatal("def err_code not found in awgm-relay.py")
	}
	body = body[start+1:]
	// Тело функции -- до следующей строки без отступа.
	if end := regexp.MustCompile(`\n\S`).FindStringIndex(body); end != nil {
		body = body[:end[0]]
	}
	var py []string
	for _, m := range regexp.MustCompile(`return "([a-z_]+)"`).FindAllStringSubmatch(body, -1) {
		if !slices.Contains(py, m[1]) {
			py = append(py, m[1])
		}
	}
	if len(py) == 0 {
		t.Fatal("no codes extracted from err_code")
	}
	goCodes := slices.Clone(errCodes)
	slices.Sort(py)
	slices.Sort(goCodes)
	if !slices.Equal(py, goCodes) {
		t.Fatalf("err_code codes %v != errCodes %v", py, goCodes)
	}
	for _, c := range py {
		if got, ok := ParseErrLine(ErrMarker+"abc "+c, "abc"); !ok || got != c {
			t.Errorf("ParseErrLine rejects relay code %q", c)
		}
	}
}
