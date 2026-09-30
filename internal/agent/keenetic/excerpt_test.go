package keenetic

import (
	"strings"
	"testing"
)

func TestExcerpt_StripsANSIAndJoinsFirstLines(t *testing.T) {
	in := "\x1b[K\n\n  Components::Manager: not ready  \r\nsecond\nthird\nfourth\n"
	if got := Excerpt(in, 3); got != "Components::Manager: not ready | second | third" {
		t.Fatalf("got %q", got)
	}
}

func TestExcerpt_CapsRunes(t *testing.T) {
	got := Excerpt(strings.Repeat("я", 500), 1)
	if n := len([]rune(got)); n != ExcerptMaxRunes+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("len=%d %q", n, got)
	}
}

func TestErrExcerpt_ShortOutputKept(t *testing.T) {
	if got := ErrExcerpt("\x1b[KCommand::Base: not enough arguments.\n"); got != "Command::Base: not enough arguments." {
		t.Fatalf("got %q", got)
	}
}

func TestErrExcerpt_LongOutputDropped(t *testing.T) {
	cfg := "! $$$ Model: KN-1811\ninterface WifiMaster0/AccessPoint0\n    authentication wpa-psk ns3 SECRET-PSK\n" + strings.Repeat("x\n", 200)
	if got := ErrExcerpt(cfg); got != "" {
		t.Fatalf("длинный вывод попал в ошибку: %q", got)
	}
}

func TestErrExcerpt_Empty(t *testing.T) {
	if got := ErrExcerpt("  \n\x1b[K\n"); got != "" {
		t.Fatalf("got %q", got)
	}
}
