package main

import (
	"strings"
	"testing"
)

func TestRenderAWGMURLPatchScriptPatchesConfigAndRestarts(t *testing.T) {
	got := renderAWGMURLPatchScript("client-e", "https://old.example", "https://new.example")
	for _, want := range []string{
		"CFG=/opt/etc/wg-monitor/config.yaml",
		"OLD='https://old.example'",
		"NEW='https://new.example'",
		"sed -i",
		"/opt/etc/init.d/S99wg-monitor",
		"wg-monitor url patched for 'client-e'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("script missing %q:\n%s", want, got)
		}
	}
}

// -old и -new раньше имели одинаковый дефолт. Запуск без флагов давал OLD==NEW:
// sed ничего не менял, а следующая проверка `grep -Fq "$NEW"` валилась с exit 2 —
// то есть команда молча превращалась в неработающую. Оба адреса обязательны и
// должны различаться; дефолта у них быть не может, это всегда конкретная миграция.
func TestParseAWGMURLPatchArgsRequiresDistinctOldAndNewURLs(t *testing.T) {
	base := []string{"-url", "https://awg.example", "-nickname", "client-a",
		"-api-key", "k", "-terminal-password", "p"}

	if _, err := parseAWGMURLPatchArgs(base); err == nil {
		t.Fatal("без -old/-new должна быть ошибка, а разбор прошёл")
	}
	if _, err := parseAWGMURLPatchArgs(append(append([]string{}, base...),
		"-old", "https://same.example", "-new", "https://same.example")); err == nil {
		t.Fatal("одинаковые -old и -new должны быть ошибкой")
	}
	opts, err := parseAWGMURLPatchArgs(append(append([]string{}, base...),
		"-old", "https://old.example", "-new", "https://new.example"))
	if err != nil {
		t.Fatalf("корректные аргументы отвергнуты: %v", err)
	}
	if opts.OldURL != "https://old.example" || opts.NewURL != "https://new.example" {
		t.Fatalf("URL разобраны неверно: old=%q new=%q", opts.OldURL, opts.NewURL)
	}
}
