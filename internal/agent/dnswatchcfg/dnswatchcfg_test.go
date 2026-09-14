package dnswatchcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateEndpoint(t *testing.T) {
	long := "https://dns.example.com/" + strings.Repeat("a", EndpointMax)
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"https://dns.example.com/secret-path/dns-query", true},
		{"", false},
		{"http://dns.example.com/x", false},
		{"https://:443/x", false},              // Host=":443", Hostname пуст
		{"https://dns.example.com/***", false}, // маска из agent_config_get
		{long, false},
		{"dns.example.com/x", false},
	} {
		if err := ValidateEndpoint(tc.in); (err == nil) != tc.ok {
			t.Errorf("ValidateEndpoint(%q) err=%v, want ok=%v", tc.in, err, tc.ok)
		}
	}
	if err := ValidateEndpoint("https://dns.example.com/***"); err != nil && strings.Contains(err.Error(), "dns.example.com") {
		t.Errorf("текст ошибки не повторяет endpoint: %v", err)
	}
}

func TestValidateBootstrapIP(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"", true}, {"198.51.100.7", true}, {"2001:db8::1", false}, {"not-ip", false},
	} {
		if err := ValidateBootstrapIP(tc.in); (err == nil) != tc.ok {
			t.Errorf("ValidateBootstrapIP(%q) err=%v, want ok=%v", tc.in, err, tc.ok)
		}
	}
}

func TestValidateCanary(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"", true}, {"example.com", true}, {"Example.COM", true},
		{"localhost", false}, {"exa mple.com", false}, {"-bad.example.com", false},
		{"https://example.com", false}, {"*.example.com", false},
	} {
		if err := ValidateCanary(tc.in); (err == nil) != tc.ok {
			t.Errorf("ValidateCanary(%q) err=%v, want ok=%v", tc.in, err, tc.ok)
		}
	}
}

func TestHold(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name, path, want string
	}{
		{"пустой путь", "", ""},
		{"нет файла", filepath.Join(dir, "absent.json"), ""},
		{"мусор", write("junk.json", "{not json"), ""},
		{"чистый primary", write("clean.json", `{"mode":"primary","last_switch":"2026-09-14T10:00:00Z"}`), ""},
		{"fallback", write("fb.json", `{"mode":"fallback"}`), HoldFallback},
		{"pending", write("pend.json", `{"mode":"primary","pending":"return"}`), HoldPending},
		{"leftover", write("left.json", `{"mode":"primary","leftover":["x"]}`), HoldCleanup},
		{"missing", write("miss.json", `{"mode":"primary","missing":["x"]}`), HoldCleanup},
	} {
		got, err := Hold(tc.path)
		if err != nil || got != tc.want {
			t.Errorf("%s: Hold=%q err=%v, want %q", tc.name, got, err, tc.want)
		}
	}
	if _, err := Hold(dir); err == nil { // каталог не читается как файл
		t.Error("нечитаемая запись должна давать ошибку, а не «чисто»")
	}
}
