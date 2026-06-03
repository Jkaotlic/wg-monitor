package main

import (
	"strings"
	"testing"
)

func TestRenderAWGMURLPatchScriptPatchesConfigAndRestarts(t *testing.T) {
	got := renderAWGMURLPatchScript("router4car4", "https://old.example", "https://new.example")
	for _, want := range []string{
		"CFG=/opt/etc/wg-monitor/config.yaml",
		"OLD='https://old.example'",
		"NEW='https://new.example'",
		"sed -i",
		"/opt/etc/init.d/S99wg-monitor",
		"wg-monitor url patched for 'router4car4'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("script missing %q:\n%s", want, got)
		}
	}
}
