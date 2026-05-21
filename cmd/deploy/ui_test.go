package main

import (
	"strings"
	"testing"
)

func TestColorize_NoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := Colorize("hello", ColorGreen)
	if got != "hello" {
		t.Errorf("expected plain text when NO_COLOR set, got %q", got)
	}
}

func TestColorize_WithColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	UseColor = true // force-enable for test (bypass isatty)
	got := Colorize("ok", ColorGreen)
	if !strings.Contains(got, "\033[32m") || !strings.Contains(got, "\033[0m") {
		t.Errorf("expected ANSI green wrapping, got %q", got)
	}
}

func TestCleanPromptDefaultLeakTrimsTrailingBracket(t *testing.T) {
	got := cleanPromptDefaultLeak("wgmonitor.example.com]")
	if got != "wgmonitor.example.com" {
		t.Fatalf("got %q", got)
	}
}
