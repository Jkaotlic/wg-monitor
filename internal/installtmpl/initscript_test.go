package installtmpl

import "testing"

func TestInitScriptHasShebangAndService(t *testing.T) {
	s := InitScript()
	if len(s) == 0 {
		t.Fatal("InitScript() is empty")
	}
	for _, want := range []string{"#!/bin/sh", "wg-monitor"} {
		if !contains(s, want) {
			t.Fatalf("init script missing %q", want)
		}
	}
	if s[len(s)-1] == '\n' {
		t.Fatal("InitScript() must have trailing newline trimmed")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
